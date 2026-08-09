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
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/ai/memory"
	"github.com/DaWesen/lanmei-dream/internal/ai/prompt"
	"github.com/DaWesen/lanmei-dream/internal/ai/tool"
	"github.com/DaWesen/lanmei-dream/internal/database"
	kbpkg "github.com/DaWesen/lanmei-dream/internal/kb"
	"go.uber.org/zap"
)

// maxToolCallRounds 工具调用循环的最大轮次。
// 限制轮次是为了防止 LLM 在工具调用中陷入无限循环（例如工具返回的结果
// 又触发了新的工具调用）。5 轮通常足以覆盖大多数多步推理场景。
const maxToolCallRounds = 5

// ChatService 编排 RAG 流程：LOD 上下文组装 → RAG 检索 → 知识库召回 → 提示构建 → LLM 调用 → 异步压缩
type ChatService struct {
	client     llm.LLMClient
	embedder   embedding.Embedder
	memory     memory.MemoryStore
	retriever  *memory.MultiRetriever // 多路召回合并器（为 nil 时降级为单一向量召回）
	db         *database.DB
	compressor *Compressor
	toolReg    *tool.Registry
	promptMgr  *prompt.Manager // Prompt 管理器（可选，为 nil 时使用 DefaultSystemPrompt）
	knowledge  *kbpkg.Service  // 知识库系统（可选，为 nil 时跳过隐式召回）
	logger     *zap.Logger
}

// NewChatService 创建对话服务
func NewChatService(client llm.LLMClient, emb embedding.Embedder, mem memory.MemoryStore, db *database.DB, toolReg *tool.Registry, logger *zap.Logger) *ChatService {
	svc := &ChatService{
		client:    client,
		embedder:  emb,
		memory:    mem,
		retriever: memory.NewMultiRetriever(mem, memory.DefaultRecallWeight),
		db:        db,
		toolReg:   toolReg,
		logger:    logger,
	}
	// 压缩器依赖 ChatService 的各组件
	if client != nil {
		svc.compressor = NewCompressor(client, emb, mem, db, logger)
	}
	return svc
}

// SetPromptManager 设置 Prompt 管理器，用于动态组装 System Prompt。
// 可在初始化后调用，不设置时使用 DefaultSystemPrompt 兜底。
func (s *ChatService) SetPromptManager(pm *prompt.Manager) {
	s.promptMgr = pm
}

// SetKnowledge 注入知识库系统。注入后每轮对话自动执行隐式知识召回
// （作为 system 消息注入上下文），并暴露 kb_search/kb_add 工具给 LLM。
// 为 nil 时知识库能力整体关闭。
func (s *ChatService) SetKnowledge(svc *kbpkg.Service) {
	s.knowledge = svc
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

	queryVec, lastMsgContent, err := s.assembleContext(ctx, req)
	if err != nil {
		return nil, err
	}

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
	s.asyncStoreAndCompress(ctx, req.UserID, req.GroupID, lastMsgContent, queryVec)

	return resp, nil
}

// assembleContext 执行 LOD 多级上下文组装、RAG 检索和 System Prompt 拼装。
// 组装后的消息列表直接写入 req.Messages，供 Chat 和 ChatStream 共用。
//
// 返回值：
//   - queryVec: 用户消息的向量嵌入（供异步存记忆），可能为 nil
//   - lastMsgContent: 最后一条用户消息的文本（供异步存记忆）
//   - err: 组装过程中的致命错误
//
// 此方法是 Chat 和 ChatStream 的共享前置步骤，确保两条路径的上下文组装逻辑一致。
func (s *ChatService) assembleContext(ctx context.Context, req *llm.ChatRequest) (queryVec []float32, lastMsgContent string, err error) {
	msgs := make([]llm.Message, 0, len(req.Messages)+4)
	lastMsg := req.Messages[len(req.Messages)-1]
	lastMsgContent = lastMsg.Content

	// ── LOD 多级上下文（必须在 System Prompt 组装之前加载，用于构建 Conversation 文本）──
	// 按 req.GroupID 隔离：群聊只加载本群历史，私聊加载个人历史，互不污染。
	var lod *database.LODContext
	if s.db != nil {
		lod, err = s.db.GetLODContext(ctx, req.UserID, req.GroupID, 3000)
		if err != nil {
			s.logger.Error("ai: lod context", zap.Error(err))
			err = nil // LOD 失败不中断流程
		}
	}

	// 从 LOD 构建 conversation 文本（仅 L2 话题 + L1 摘要，不含 L0 原文，
	// L0 原文后续作为独立消息追加以保持正确的 role 格式）。
	var conversationText string
	if lod != nil {
		var b strings.Builder
		if len(lod.TopicBriefs) > 0 {
			b.WriteString("## 历史话题\n")
			for _, t := range lod.TopicBriefs {
				b.WriteString("- ")
				b.WriteString(t)
				b.WriteString("\n")
			}
		}
		if len(lod.EpisodeBriefs) > 0 {
			b.WriteString("## 对话摘要\n")
			for _, e := range lod.EpisodeBriefs {
				b.WriteString("- ")
				b.WriteString(e)
				b.WriteString("\n")
			}
		}
		conversationText = b.String()
	}

	// ── 组装 System Prompt（必须放在消息列表最前面）──
	systemContent := DefaultSystemPrompt
	if s.promptMgr != nil {
		assembled, assembleErr := s.promptMgr.Assemble(prompt.AssemblyContext{
			Vars:         s.promptMgr.Vars(),
			CurrentTime:  time.Now().Format("2006-01-02 15:04:05"),
			UserName:     req.UserName,
			GroupName:    req.GroupName,
			Conversation: conversationText,
		})
		if assembleErr != nil {
			s.logger.Error("ai: prompt assembly failed, using default", zap.Error(assembleErr))
		} else {
			systemContent = assembled
		}
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: systemContent})

	// ── L0 原始对话保持独立 role 消息（user/assistant），不混入 system prompt ──
	// 群聊话题场景下由话题近期消息（TopicContext.Recent）替代 L0 原文（更贴近当前话题），
	// L2/L1 摘要仍保留在 system prompt 中作为补充。
	if lod != nil && req.TopicContext == nil {
		for _, c := range lod.RawConversations {
			msgs = append(msgs, llm.Message{Role: llm.Role(c.Role), Content: c.Content})
		}
		// 在历史对话后追加强化指令，抵消历史中 assistant 旧风格对当前行为的模式污染。
		// LLM 对 concrete example 的敏感度高于抽象规则，若不加固化指令，
		// 历史中带 emoji 的 assistant 回复会让 LLM 认为"这是预期风格"。
		msgs = append(msgs, llm.Message{
			Role:    llm.RoleSystem,
			Content: "注意：以上是历史对话记录，其中 assistant 的回复风格可能不完全符合当前规范。请严格遵守本 prompt 开头的「关键行为规则」，特别是 Emoji 使用规范和长回复分段规则，不要被历史中的回复模式带偏。",
		})
	}

	// ── 群聊话题上下文注入（TopicGatePass 命中话题时写入）──
	// 话题近期消息作为主历史（user/assistant 交替），并附加话题约束，防止 Bot 越界回复无关内容。
	if req.TopicContext != nil {
		for _, tm := range req.TopicContext.Recent {
			role := llm.RoleUser
			if tm.IsBot {
				role = llm.RoleAssistant
			}
			msgs = append(msgs, llm.Message{Role: role, Content: tm.Content})
		}
		label := req.TopicContext.Label
		if label == "" {
			label = "群聊话题"
		}
		members := strings.Join(req.TopicContext.Members, "、")
		if members == "" {
			members = "群内成员"
		}
		msgs = append(msgs, llm.Message{
			Role: llm.RoleSystem,
			Content: "当前正处于群聊话题「" + label + "」中，参与成员：" + members +
				"。请围绕该话题与成员们对话；只回应与话题相关的消息，如果用户在谈论其他事情，可以简短回应或不必回复。",
		})
	}

	// ── RAG 检索长期记忆（多路召回）──
	if queryVec == nil && s.embedder != nil {
		queryVec, err = s.embedder.Embed(ctx, lastMsg.Content)
		if err != nil {
			s.logger.Error("ai: embed failed", zap.Error(err))
			err = nil // 嵌入失败不中断流程
		}
	}
	var memories []*memory.Memory
	if s.retriever != nil {
		// 多路召回：向量 + 关键词 + 时间（按 req.GroupID 隔离群级/个人记忆）
		var retrieveErr error
		memories, retrieveErr = s.retriever.Retrieve(ctx, queryVec, lastMsg.Content, req.UserID, req.GroupID, 5)
		if retrieveErr != nil {
			s.logger.Error("ai: multi-retrieve memory failed", zap.Error(retrieveErr))
		}
	} else if queryVec != nil && s.memory != nil {
		// 降级：仅向量召回
		var retrieveErr error
		memories, retrieveErr = s.memory.Retrieve(ctx, queryVec, req.UserID, req.GroupID, 5)
		if retrieveErr != nil {
			s.logger.Error("ai: retrieve memory failed", zap.Error(retrieveErr))
		}
	}
	if ragCtx := BuildRAGContext(memories); ragCtx != "" {
		msgs = append(msgs, llm.Message{
			Role:    llm.RoleSystem,
			Content: "以下是与当前对话相关的记忆：\n" + ragCtx,
		})
	}

	// ── 知识库隐式召回（RAG 增强，可选）──
	// 每轮对话按默认模式自动召回少量相关条目注入上下文；
	// 复用 RAG 阶段已算好的 queryVec，避免同一句话重复向量化；
	// 失败（网络/向量化异常）仅记日志，不中断主流程。
	if s.knowledge != nil {
		kbResults, kbErr := s.knowledge.Recall(ctx, &kbpkg.RecallRequest{
			Query:       lastMsg.Content,
			QueryVector: queryVec,
			Modes:       s.knowledge.DefaultModes(),
			Limit:       s.knowledge.AutoRecallLimit(),
		})
		if kbErr != nil {
			s.logger.Warn("ai: 知识库隐式召回失败", zap.Error(kbErr))
		} else if kbCtx := BuildKBContext(kbResults); kbCtx != "" {
			msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: kbCtx})
		}
	}

	// 追加用户请求消息（当前轮次）
	msgs = append(msgs, req.Messages...)

	req.Messages = msgs

	return queryVec, lastMsgContent, nil
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
		s.asyncStoreAndCompress(ctx, req.UserID, req.GroupID, req.Messages[len(req.Messages)-1].Content, nil)
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
	schemaMsgs, totalTokens, invokedTools, err := s.processToolCalls(ctx, chatModel, schemaMsgs)
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
	s.asyncStoreAndCompress(ctx, req.UserID, req.GroupID, req.Messages[len(req.Messages)-1].Content, nil)

	return &llm.ChatResponse{
		Content:       finalContent,
		TokensUsed:    totalTokens,
		InvolvedTools: invokedTools,
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
func (s *ChatService) processToolCalls(ctx context.Context, chatModel model.BaseChatModel, msgs []*schema.Message) ([]*schema.Message, int, []string, error) {
	totalTokens := 0
	var invokedTools []string
	for round := 0; round < maxToolCallRounds; round++ {
		// 调用 LLM 生成回复（可能包含工具调用请求）
		resp, err := chatModel.Generate(ctx, msgs)
		if err != nil {
			return msgs, totalTokens, invokedTools, err
		}
		// 将 LLM 回复追加到消息列表，作为下一轮的上下文
		msgs = append(msgs, resp)

		// 累计 token 用量（用于计费和监控）
		if resp.ResponseMeta != nil && resp.ResponseMeta.Usage != nil {
			totalTokens += resp.ResponseMeta.Usage.TotalTokens
		}

		// 如果 LLM 没有请求任何工具调用，说明已经产出最终文本回复，循环结束
		if len(resp.ToolCalls) == 0 {
			return msgs, totalTokens, invokedTools, nil
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
			// 记录实际调用的工具名
			invokedTools = append(invokedTools, tc.Function.Name)
		}
	}
	// 达到最大轮次限制，强制退出循环
	return msgs, totalTokens, invokedTools, nil
}

// asyncStoreAndCompress 异步存记忆 + 触发压缩。
// groupID 标识来源群：群聊消息写入带群标签的记忆（避免污染个人记忆），
// 个人记忆压缩（Compressor）仍仅针对私聊维度。
func (s *ChatService) asyncStoreAndCompress(ctx context.Context, userID int64, groupID, content string, queryVec []float32) {
	if s.memory != nil && queryVec != nil {
		go func() {
			bgCtx := context.Background()
			_ = s.memory.Store(bgCtx, &memory.Memory{
				UserID:  userID,
				GroupID: groupID,
				Content: content,
				Vector:  queryVec,
			})
		}()
	}
	if s.compressor != nil {
		go s.compressor.MaybeCompress(context.Background(), userID)
	}
}
