package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

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
	var invokedTools []string
	var lastToolResult string // 最近一次工具结果（LLM 工具轮后无文本时兜底输出）

	for round := 0; round < maxToolCallRounds; round++ {
		reader, streamErr := chatModel.Stream(ctx, schemaMsgs)
		if streamErr != nil {
			return nil, fmt.Errorf("chat stream: open stream: %w", streamErr)
		}

		// 读取首个 chunk 判定轮次类型
		firstChunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			reader.Close()
			s.logger.Warn("chat stream: 首 chunk 即 EOF（LLM 空响应）",
				zap.Int("round", round), zap.Int("msgs", len(schemaMsgs)))
			break // 空响应
		}
		if recvErr != nil {
			reader.Close()
			return nil, fmt.Errorf("chat stream: recv first chunk: %w", recvErr)
		}

		// 轮次判定日志：确认 LLM 实际返回的内容形态（文本 / 工具调用 / 仅 reasoning）
		s.logger.Info("chat stream: 轮次判定",
			zap.Int("round", round),
			zap.Int("tool_calls", len(firstChunk.ToolCalls)),
			zap.Int("content_len", len(firstChunk.Content)),
			zap.Int("reasoning_len", len(firstChunk.ReasoningContent)))

		// 工具轮执行闭包：拼接 assistant 消息 + 执行工具 + 回传结果。
		// 推理模型（如 deepseek-v4）先流 reasoning，工具调用可能出现在后续 chunk，
		// 因此"首 chunk 初判"与"文本轮中途检测"到的工具调用都走这里。
		runToolRound := func(chunks []*schema.Message) (bool, error) {
			accumulated, concatErr := schema.ConcatMessages(chunks)
			if concatErr != nil {
				return false, fmt.Errorf("chat stream: concat tool message: %w", concatErr)
			}
			if accumulated.ResponseMeta != nil && accumulated.ResponseMeta.Usage != nil {
				totalTokens += accumulated.ResponseMeta.Usage.TotalTokens
			}
			// DeepSeek 等实现要求 assistant 消息必须携带 content 字段，而 go-openai 序列化
			// 时空 content 会被 omitempty 省略；工具调用类 assistant 消息 content 常为空，
			// 补一个空格占位，避免下一轮请求被 400 拒绝。
			if accumulated.Content == "" && len(accumulated.ToolCalls) > 0 {
				accumulated.Content = " "
			}
			schemaMsgs = append(schemaMsgs, accumulated)
			for _, tc := range accumulated.ToolCalls {
				s.logger.Info("chat stream: 工具轮触发",
					zap.String("tool", tc.Function.Name), zap.String("args", tc.Function.Arguments))
				result, callErr := s.toolReg.Call(ctx, tc.Function.Name, tc.Function.Arguments)
				if callErr != nil {
					result = fmt.Sprintf("工具调用失败: %v", callErr)
				}
				s.logger.Info("chat stream: 工具结果", zap.String("tool", tc.Function.Name),
					zap.Int("result_len", len(result)), zap.String("result", truncateForLog(result, 120)))
				lastToolResult = result
				schemaMsgs = append(schemaMsgs, &schema.Message{
					Role:       schema.Tool,
					ToolCallID: tc.ID,
					Content:    result,
				})
				invokedTools = append(invokedTools, tc.Function.Name)
			}
			return true, nil
		}

		// 初判：首 chunk 即带工具调用 → 纯工具轮
		if len(firstChunk.ToolCalls) > 0 {
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
			if _, err := runToolRound(chunks); err != nil {
				return nil, err
			}
			continue // 下一轮
		}

		// ── 文本轮次：流式产出段落 ──
		// reasoning 型模型（如 deepseek-v4）可能只返回 reasoning_content 而无 content，
		// 收集 reasoning 作为空响应兜底。
		var reasoningBuf strings.Builder
		if firstChunk.ReasoningContent != "" {
			reasoningBuf.WriteString(firstChunk.ReasoningContent)
		}
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

		// 处理剩余 chunk；推理模型可能在 reasoning 后才发出工具调用（tool_calls 出现在后续 chunk），
		// 一旦检测到即切换为工具轮，避免把工具调用当纯文本轮处理导致"只想不做"。
		switchedToTool := false
	chunkLoop:
		for {
			chunk, err := reader.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				reader.Close()
				return nil, fmt.Errorf("chat stream: recv text chunk: %w", err)
			}
			if chunk.ReasoningContent != "" {
				reasoningBuf.WriteString(chunk.ReasoningContent)
			}
			if len(chunk.ToolCalls) > 0 {
				chunks := []*schema.Message{firstChunk, chunk}
				for {
					c, err := reader.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						reader.Close()
						return nil, fmt.Errorf("chat stream: recv tool chunk: %w", err)
					}
					chunks = append(chunks, c)
				}
				reader.Close()
				if _, err := runToolRound(chunks); err != nil {
					return nil, err
				}
				switchedToTool = true
				break chunkLoop
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
		// 工具切换分支已在内部 Close，此处避免二次 Close（会 panic: close of closed channel）
		if !switchedToTool {
			reader.Close()
		}
		if switchedToTool {
			continue // 本轮已切换为工具轮，进入下一轮生成
		}

		// flush 剩余缓冲为末段
		if last := segmenter.Flush(); last != "" {
			if sendErr := sendSegment(ctx, segmentCh, last); sendErr != nil {
				return nil, sendErr
			}
		}

		// 工具已执行但 LLM 未产出最终文本（部分模型认为工具结果即答案，工具轮后不再生成内容）：
		// 将最近一次工具结果作为回复输出，避免"调了工具却无响应"。
		if segmenter.FullText() == "" && lastToolResult != "" {
			for _, seg := range segmenter.Feed(lastToolResult) {
				if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
					return nil, sendErr
				}
			}
			if last := segmenter.Flush(); last != "" {
				if sendErr := sendSegment(ctx, segmentCh, last); sendErr != nil {
					return nil, sendErr
				}
			}
		} else if segmenter.FullText() == "" && reasoningBuf.Len() > 0 {
			// 仅返回 reasoning 而无 content 的空响应：输出思考内容兜底，避免用户看到"没听清"。
			s.logger.Info("chat stream: 使用 reasoning 兜底输出", zap.Int("reasoning_len", reasoningBuf.Len()))
			for _, seg := range segmenter.Feed(strings.TrimSpace(reasoningBuf.String())) {
				if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
					return nil, sendErr
				}
			}
			if last := segmenter.Flush(); last != "" {
				if sendErr := sendSegment(ctx, segmentCh, last); sendErr != nil {
					return nil, sendErr
				}
			}
		}

		// 异步存记忆 + 触发压缩
		s.asyncStoreAndCompress(ctx, req.UserID, req.GroupID, lastMsgContent, queryVec)

		return &llm.ChatResponse{
			Content:       segmenter.FullText(),
			TokensUsed:    totalTokens,
			InvolvedTools: invokedTools,
		}, nil
	}

	// 达到最大工具调用轮次，返回已有内容
	s.asyncStoreAndCompress(ctx, req.UserID, req.GroupID, lastMsgContent, queryVec)

	// 同上：工具已执行但未产出文本时，用最近一次工具结果兜底
	if segmenter.FullText() == "" && lastToolResult != "" {
		for _, seg := range segmenter.Feed(lastToolResult) {
			if sendErr := sendSegment(ctx, segmentCh, seg); sendErr != nil {
				return nil, sendErr
			}
		}
	}

	return &llm.ChatResponse{
		Content:       segmenter.FullText(),
		TokensUsed:    totalTokens,
		InvolvedTools: invokedTools,
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

	s.asyncStoreAndCompress(ctx, req.UserID, req.GroupID, lastMsgContent, queryVec)

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

// truncateForLog 截断过长的日志值（工具结果可能是完整图片 URL，全量打印会刷屏）。
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
