package ai

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
)

// ChatStream 以流式方式执行对话，将段落增量写入 segmentCh。
//
// 流程：
//  1. 与 Chat 相同的 LOD/RAG/Prompt 组装（assembleContext）
//  2. 若客户端为 EinoClient：启动流式工具调用循环（chatStreamWithToolLoop），
//     边接收 LLM token 边检测段落边界（\n\n），每完整一段就写入 segmentCh
//  3. 若客户端不支持流式：降级为 Chat + StreamSegmenter 分段，逐段写入 segmentCh
//
// 返回的 ChatResponse.Content 为完整回复文本（供存储）。
// segmentCh 由调用方创建，本方法不关闭它（由调用方在流结束后关闭）。
func (s *ChatService) ChatStream(ctx context.Context, req *llm.ChatRequest, segmentCh chan<- string) (*llm.ChatResponse, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("chat stream: empty messages")
	}

	queryVec, lastMsgContent, err := s.assembleContext(ctx, req)
	if err != nil {
		return nil, err
	}

	// EinoClient 流式路径（支持工具调用循环）
	if einoClient, isEino := s.client.(*llm.EinoClient); isEino {
		return s.chatStreamWithToolLoop(ctx, req, einoClient, segmentCh, queryVec, lastMsgContent)
	}

	// 降级路径：非流式客户端 → Chat + 逐段投递
	return s.chatStreamFallback(ctx, req, segmentCh, queryVec, lastMsgContent)
}

// chatStreamWithToolLoop 执行带工具调用循环的流式对话。
//
// 算法：
//  1. 绑定工具（若可用）获取 chatModel
//  2. 每轮用 chatModel.Stream 打开流
//  3. 读取首个 chunk 判定轮次类型：
//     - ToolCalls 非空 → 工具轮次：缓冲全部 chunk，执行工具，进入下一轮
//     - Content 非空 → 文本轮次：边接收边通过 StreamSegmenter 检测边界，产出段落
//  4. 文本轮次结束后 flush 段落缓冲，返回完整文本
//
// 这基于 OpenAI 兼容 API 的行为：工具调用响应的首个 chunk 携带 tool_calls delta
// 且 content 为空；纯文本响应的首个 chunk 携带 content delta 且无 tool_calls。
func (s *ChatService) chatStreamWithToolLoop(
	ctx context.Context,
	req *llm.ChatRequest,
	einoClient *llm.EinoClient,
	segmentCh chan<- string,
	queryVec []float32,
	lastMsgContent string,
) (*llm.ChatResponse, error) {
	// 获取 chatModel（绑定工具或使用 base model）
	chatModel, err := s.getStreamChatModel(einoClient)
	if err != nil {
		return nil, err
	}

	schemaMsgs := llm.ToSchemaMessages(req.Messages)
	segmenter := NewStreamSegmenter()
	totalTokens := 0

	for round := 0; round < maxToolCallRounds; round++ {
		reader, streamErr := chatModel.Stream(ctx, schemaMsgs)
		if streamErr != nil {
			return nil, fmt.Errorf("chat stream: open stream: %w", streamErr)
		}

		// 读取首个 chunk 判定轮次类型
		firstChunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			reader.Close()
			break // 空响应
		}
		if recvErr != nil {
			reader.Close()
			return nil, fmt.Errorf("chat stream: recv first chunk: %w", recvErr)
		}

		isToolRound := len(firstChunk.ToolCalls) > 0

		if isToolRound {
			// ── 工具轮次：缓冲全部 chunk，拼接完整消息，执行工具 ──
			chunks := []*schema.Message{firstChunk}
			for {
				chunk, err := reader.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					reader.Close()
					return nil, fmt.Errorf("chat stream: recv tool chunk: %w", err)
				}
				chunks = append(chunks, chunk)
			}
			reader.Close()

			// 拼接所有 chunk 为完整消息（ConcatMessages 处理 tool_call delta 合并）
			accumulated, concatErr := schema.ConcatMessages(chunks)
			if concatErr != nil {
				return nil, fmt.Errorf("chat stream: concat tool message: %w", concatErr)
			}

			// 累计 token
			if accumulated.ResponseMeta != nil && accumulated.ResponseMeta.Usage != nil {
				totalTokens += accumulated.ResponseMeta.Usage.TotalTokens
			}

			// 将 assistant 消息追加到消息列表
			schemaMsgs = append(schemaMsgs, accumulated)

			// 执行工具调用
			for _, tc := range accumulated.ToolCalls {
				result, callErr := s.toolReg.Call(ctx, tc.Function.Name, tc.Function.Arguments)
				if callErr != nil {
					result = fmt.Sprintf("工具调用失败: %v", callErr)
				}
				schemaMsgs = append(schemaMsgs, &schema.Message{
					Role:       schema.Tool,
					ToolCallID: tc.ID,
					Content:    result,
				})
			}
			continue // 下一轮
		}

		// ── 文本轮次：流式产出段落 ──
		// 处理首 chunk
		if firstChunk.Content != "" {
			for _, seg := range segmenter.Feed(firstChunk.Content) {
				if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
					reader.Close()
					return nil, sendErr
				}
			}
		}
		if firstChunk.ResponseMeta != nil && firstChunk.ResponseMeta.Usage != nil {
			totalTokens += firstChunk.ResponseMeta.Usage.TotalTokens
		}

		// 处理剩余 chunk
		for {
			chunk, err := reader.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				reader.Close()
				return nil, fmt.Errorf("chat stream: recv text chunk: %w", err)
			}
			if chunk.Content != "" {
				for _, seg := range segmenter.Feed(chunk.Content) {
					if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
						reader.Close()
						return nil, sendErr
					}
				}
			}
			if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
				totalTokens += chunk.ResponseMeta.Usage.TotalTokens
			}
		}
		reader.Close()

		// flush 剩余缓冲为末段
		if last := segmenter.Flush(); last != "" {
			if sendErr := sendSegment(ctx, segmentCh, last); sendErr != nil {
				return nil, sendErr
			}
		}

		// 异步存记忆 + 触发压缩
		s.asyncStoreAndCompress(ctx, req.UserID, lastMsgContent, queryVec)

		return &llm.ChatResponse{
			Content:    segmenter.FullText(),
			TokensUsed: totalTokens,
		}, nil
	}

	// 达到最大工具调用轮次，返回已有内容
	s.asyncStoreAndCompress(ctx, req.UserID, lastMsgContent, queryVec)

	return &llm.ChatResponse{
		Content:    segmenter.FullText(),
		TokensUsed: totalTokens,
	}, nil
}

// getStreamChatModel 获取用于流式对话的 chatModel。
// 若模型支持工具调用且有注册工具，绑定工具后返回；否则返回 base model。
func (s *ChatService) getStreamChatModel(einoClient *llm.EinoClient) (model.BaseChatModel, error) {
	if s.toolReg != nil && len(s.toolReg.ToolInfos()) > 0 && einoClient.SupportsToolCalling() {
		chatModel, err := einoClient.ChatWithTools(s.toolReg.ToolInfos())
		if err != nil {
			s.logger.Error("ai.ChatStream: bind tools failed, using base model", zap.Error(err))
			return einoClient.BaseModel(), nil
		}
		return chatModel, nil
	}
	return einoClient.BaseModel(), nil
}

// chatStreamFallback 非流式客户端的降级路径。
// 调用 Chat 获取完整响应，用 StreamSegmenter 拆分后逐段写入 segmentCh。
func (s *ChatService) chatStreamFallback(
	ctx context.Context,
	req *llm.ChatRequest,
	segmentCh chan<- string,
	queryVec []float32,
	lastMsgContent string,
) (*llm.ChatResponse, error) {
	resp, err := s.client.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("chat stream fallback: llm call: %w", err)
	}

	// 用 StreamSegmenter 拆分完整响应（等价于 splitResponse 的效果）
	segmenter := NewStreamSegmenter()
	for _, seg := range segmenter.Feed(resp.Content) {
		if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
			return nil, sendErr
		}
	}
	if last := segmenter.Flush(); last != "" {
		if sendErr := sendSegment(ctx, segmentCh, last); sendErr != nil {
			return nil, sendErr
		}
	}

	s.asyncStoreAndCompress(ctx, req.UserID, lastMsgContent, queryVec)

	return resp, nil
}

// sendSegment 将一个段落写入 segmentCh，支持 context 取消。
func sendSegment(ctx context.Context, ch chan<- string, seg string) error {
	select {
	case ch <- seg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
