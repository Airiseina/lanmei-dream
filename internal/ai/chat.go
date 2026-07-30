// Package ai 提供对话编排服务，是整个 AI 对话系统的核心入口。
//
// 本包负责将 RAG 检索、LOD 多级上下文、LLM 调用和工具调用（Function Calling）
// 串联为完整的对话流程。核心设计原则：
//   - LOD（Level of Detail）上下文组装：按 L2→L1→L0 粒度逐步加载历史对话，
//     在 token 预算内最大化上下文信息量
//   - 工具调用循环：当 LLM 返回 ToolCalls 时，自动执行工具并将结果回传，
//     循环直至 LLM 产出最终文本回复或达到最大轮次限制
//   - 异步记忆与压缩：对话完成后异步存储向量记忆、触发对话压缩，不阻塞响应
package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/ai/memory"
	"github.com/DaWesen/lanmei-dream/internal/ai/tool"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"go.uber.org/zap"
)

// maxToolCallRounds 工具调用循环的最大轮次。
// 限制轮次是为了防止 LLM 在工具调用中陷入无限循环（例如工具返回的结果
// 又触发了新的工具调用）。5 轮通常足以覆盖大多数多步推理场景。
const maxToolCallRounds = 5

// ChatService 编排 RAG 流程：LOD 上下文组装 → RAG 检索 → 提示构建 → LLM 调用 → 异步压缩
type ChatService struct {
	client     llm.LLMClient
	embedder   embedding.Embedder
	memory     memory.MemoryStore
	db         *database.DB
	compressor *Compressor
	toolReg    *tool.Registry
	logger     *zap.Logger
}

// NewChatService 创建对话服务
func NewChatService(client llm.LLMClient, emb embedding.Embedder, mem memory.MemoryStore, db *database.DB, toolReg *tool.Registry, logger *zap.Logger) *ChatService {
	svc := &ChatService{
		client:   client,
		embedder: emb,
		memory:   mem,
		db:       db,
		toolReg:  toolReg,
		logger:   logger,
	}
	// 压缩器依赖 ChatService 的各组件
	if client != nil {
		svc.compressor = NewCompressor(client, emb, mem, db, logger)
	}
	return svc
}

// Compressor 暴露压缩器给外部调用
func (s *ChatService) Compressor() *Compressor {
	return s.compressor
}

// ToolRegistry 暴露工具注册表给外部调用
func (s *ChatService) ToolRegistry() *tool.Registry {
	return s.toolReg
}

// Chat 执行一次完整对话：
//  1. 按 LOD 多级上下文组装（L2→L1→L0，token 预算控制）
//  2. RAG 检索长期记忆
//  3. 拼装 system + LOD + RAG + 原始消息
//  4. 调用 LLM（支持工具调用循环）
func (s *ChatService) Chat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("chat: empty messages")
	}

	// ── LOD 多级上下文组装 ──
	msgs := make([]llm.Message, 0, len(req.Messages)+4)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: SystemPrompt})

	if s.db != nil {
		lod, err := s.db.GetLODContext(ctx, req.UserID, 3000) // 3000 token 预算给上下文
		if err != nil {
			s.logger.Error("ai.Chat: lod context", zap.Error(err))
		} else if lod != nil {
			if len(lod.TopicBriefs) > 0 {
				msgs = append(msgs, llm.Message{Role: llm.RoleSystem,
					Content: "历史话题概览：\n" + strings.Join(lod.TopicBriefs, "\n")})
			}
			if len(lod.EpisodeDetails) > 0 {
				msgs = append(msgs, llm.Message{Role: llm.RoleSystem,
					Content: "过往对话摘要：\n" + strings.Join(lod.EpisodeDetails, "\n")})
			} else if len(lod.EpisodeBriefs) > 0 {
				msgs = append(msgs, llm.Message{Role: llm.RoleSystem,
					Content: "过往对话概要：\n" + strings.Join(lod.EpisodeBriefs, "\n")})
			}

			// L0 原始对话（LOD 已经按预算筛选）
			for _, c := range lod.RawConversations {
				msgs = append(msgs, llm.Message{Role: llm.Role(c.Role), Content: c.Content})
			}
		}
	}

	// ── RAG 检索长期记忆 ──
	lastMsg := req.Messages[len(req.Messages)-1]
	var queryVec []float32
	if s.embedder != nil {
		var err error
		queryVec, err = s.embedder.Embed(ctx, lastMsg.Content)
		if err != nil {
			s.logger.Error("ai.Chat: embed failed", zap.Error(err))
		}
	}
	if queryVec != nil && s.memory != nil {
		memories, err := s.memory.Retrieve(ctx, queryVec, req.UserID, 5)
		if err != nil {
			s.logger.Error("ai.Chat: retrieve memory failed", zap.Error(err))
		}
		if ragCtx := BuildRAGContext(memories); ragCtx != "" {
			msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: ragCtx})
		}
	}

	// 追加用户请求消息。
	// 当 LOD 未返回 L0 原始对话时，req.Messages 是唯一的用户输入；
	// 当 LOD 返回了 L0 原始对话时，req.Messages 中的最新消息仍需追加（当前轮次）。
	msgs = append(msgs, req.Messages...)

	req.Messages = msgs

	// ── 工具调用判断 ──
	einoClient, isEino := s.client.(*llm.EinoClient)
	if isEino && s.toolReg != nil && len(s.toolReg.ToolInfos()) > 0 && einoClient.SupportsToolCalling() {
		return s.chatWithToolLoop(ctx, req, einoClient)
	}

	resp, err := s.client.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("chat: llm call: %w", err)
	}

	// ── 异步：存记忆 + 触发压缩 ──
	s.asyncStoreAndCompress(ctx, req.UserID, lastMsg.Content, queryVec)

	return resp, nil
}

// chatWithToolLoop 执行带工具调用循环的对话流程。
//
// 设计思路：
// 当 LLM 支持 Function Calling 时，一次用户请求可能触发多轮 LLM ↔ 工具 的交互。
// 整体流程为：
//  1. 将工具定义绑定到 Eino ChatModel（ChatWithTools）
//  2. 将内部消息格式（llm.Message）转换为 Eino schema.Message 格式
//  3. 进入 processToolCalls 循环：LLM 生成 → 检查是否有 ToolCalls → 执行工具 →
//     将工具结果追加到消息列表 → 再次调用 LLM → … 直至 LLM 不再请求工具
//  4. 从最终消息列表中提取最后一条 assistant 消息作为回复
//
// 降级策略：如果绑定工具失败，回退到普通 Chat 调用（不使用工具）。
//
// 参数：
//   - ctx: 上下文，用于取消和超时控制
//   - req: 对话请求，包含组装后的消息列表
//   - einoClient: 支持 Function Calling 的 Eino 客户端
//
// 返回：
//   - ChatResponse: 包含最终回复内容和累计 token 用量
//   - error: 工具绑定失败、LLM 调用失败等错误
func (s *ChatService) chatWithToolLoop(ctx context.Context, req *llm.ChatRequest, einoClient *llm.EinoClient) (*llm.ChatResponse, error) {
	toolInfos := s.toolReg.ToolInfos()
	chatModel, err := einoClient.ChatWithTools(toolInfos)
	if err != nil {
		s.logger.Error("ai.Chat: bind tools failed, falling back to plain chat", zap.Error(err))
		resp, err := s.client.Chat(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("chat: llm call: %w", err)
		}
		s.asyncStoreAndCompress(ctx, req.UserID, req.Messages[len(req.Messages)-1].Content, nil)
		return resp, nil
	}

	// 将内部消息格式转换为 Eino schema.Message 格式。
	// 这一步是必要的，因为 Eino 的 ChatModel.Generate 接口要求 schema.Message 类型，
	// 而上层使用的是 llm.Message 内部类型。转换时保留 ToolCallID 以支持
	// 多轮工具调用场景（工具结果消息需要关联到对应的 ToolCall）。
	schemaMsgs := make([]*schema.Message, len(req.Messages))
	for i, m := range req.Messages {
		schemaMsg := &schema.Message{
			Role:    llm.ToSchemaRole(m.Role),
			Content: m.Content,
		}
		if m.ToolCallID != "" {
			schemaMsg.ToolCallID = m.ToolCallID
		}
		schemaMsgs[i] = schemaMsg
	}

	// 进入工具调用循环：LLM 生成 → 执行工具 → 回传结果 → 再次生成，直至完成
	schemaMsgs, totalTokens, err := s.processToolCalls(ctx, chatModel, schemaMsgs)
	if err != nil {
		return nil, fmt.Errorf("chat: tool call loop: %w", err)
	}

	// 从消息列表中提取最终 assistant 回复。
	// 工具调用循环结束后，消息列表包含多轮的 assistant/tool 消息，
	// 我们需要找到最后一条 assistant 消息的文本内容作为用户可见的回复。
	// 如果所有 assistant 消息都为空（理论上不应发生），则取最后一条消息兜底。
	var finalContent string
	for i := len(schemaMsgs) - 1; i >= 0; i-- {
		if schemaMsgs[i].Role == schema.Assistant {
			finalContent = schemaMsgs[i].Content
			break
		}
	}
	if finalContent == "" && len(schemaMsgs) > 0 {
		finalContent = schemaMsgs[len(schemaMsgs)-1].Content
	}

	// ── 异步：存记忆 + 触发压缩 ──
	s.asyncStoreAndCompress(ctx, req.UserID, req.Messages[len(req.Messages)-1].Content, nil)

	return &llm.ChatResponse{
		Content:    finalContent,
		TokensUsed: totalTokens,
	}, nil
}

// processToolCalls 执行 LLM 工具调用的循环处理。
//
// 核心循环逻辑：
//
//	for round := 0; round < maxToolCallRounds; round++ {
//	    1. 将当前消息列表发送给 LLM（chatModel.Generate）
//	    2. 将 LLM 的回复追加到消息列表
//	    3. 累计 token 用量
//	    4. 如果 LLM 没有请求工具调用（len(resp.ToolCalls) == 0），循环结束
//	    5. 否则，逐个执行 LLM 请求的工具调用，将工具结果作为 Tool 角色消息追加
//	    6. 继续下一轮循环，让 LLM 基于工具结果继续生成
//	}
//
// 设计要点：
//   - 工具调用失败时不会中断循环，而是将错误信息作为工具结果回传给 LLM，
//     让 LLM 有机会自行决定下一步（例如换一种方式、或向用户报告错误）
//   - 每个工具结果消息携带 ToolCallID，确保 LLM 能将结果与对应的请求关联
//   - 达到最大轮次后强制退出，此时 LLM 可能仍有未完成的工具调用，
//     但已有的消息列表仍包含有价值的中间结果
//
// 参数：
//   - ctx: 上下文，用于取消和超时控制
//   - chatModel: 已绑定工具定义的 Eino ChatModel
//   - msgs: 初始消息列表（system + LOD + RAG + 用户消息）
//
// 返回：
//   - []*schema.Message: 包含所有中间轮次消息的完整消息列表
//   - int: 累计消耗的 token 总量
//   - error: LLM 调用失败时的错误
func (s *ChatService) processToolCalls(ctx context.Context, chatModel model.BaseChatModel, msgs []*schema.Message) ([]*schema.Message, int, error) {
	totalTokens := 0
	for round := 0; round < maxToolCallRounds; round++ {
		// 调用 LLM 生成回复（可能包含工具调用请求）
		resp, err := chatModel.Generate(ctx, msgs)
		if err != nil {
			return msgs, totalTokens, err
		}
		// 将 LLM 回复追加到消息列表，作为下一轮的上下文
		msgs = append(msgs, resp)

		// 累计 token 用量（用于计费和监控）
		if resp.ResponseMeta != nil && resp.ResponseMeta.Usage != nil {
			totalTokens += resp.ResponseMeta.Usage.TotalTokens
		}

		// 如果 LLM 没有请求任何工具调用，说明已经产出最终文本回复，循环结束
		if len(resp.ToolCalls) == 0 {
			return msgs, totalTokens, nil
		}

		// 逐个执行 LLM 请求的工具调用
		for _, tc := range resp.ToolCalls {
			// 通过工具注册表调用对应工具
			result, callErr := s.toolReg.Call(ctx, tc.Function.Name, tc.Function.Arguments)
			// 工具调用失败时，将错误信息作为结果回传给 LLM，
			// 而不是直接中断——这允许 LLM 自行决策（如重试、换工具、告知用户）
			if callErr != nil {
				result = fmt.Sprintf("工具调用失败: %v", callErr)
			}
			// 将工具结果作为 Tool 角色消息追加，ToolCallID 用于关联 LLM 的请求
			msgs = append(msgs, &schema.Message{
				Role:       schema.Tool,
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}
	// 达到最大轮次限制，强制退出循环
	return msgs, totalTokens, nil
}

// asyncStoreAndCompress 异步存记忆 + 触发压缩
func (s *ChatService) asyncStoreAndCompress(ctx context.Context, userID int64, content string, queryVec []float32) {
	if s.memory != nil && queryVec != nil {
		go func() {
			bgCtx := context.Background()
			_ = s.memory.Store(bgCtx, &memory.Memory{
				UserID:  userID,
				Content: content,
				Vector:  queryVec,
			})
		}()
	}
	if s.compressor != nil {
		go s.compressor.MaybeCompress(context.Background(), userID)
	}
}
