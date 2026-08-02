package bot

import (
	"fmt"
	"strings"

	"github.com/zrurf/conduit"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/database"
)

// ── 上下文键 ──

const (
	KeyPlatform       = "platform"         // string 平台标识（qq/wechat/telegram/...）
	KeyPlatformUserID = "platform_user_id" // string 平台用户 ID
	KeyNickname       = "nickname"         // string 昵称
	KeyMessageID      = "message_id"       // string 消息 ID
	KeyConnID         = "conn_id"          // string 来源连接 ID
	KeySelfID         = "self_id"          // string 机器人自身 ID
)

// ── CommandPass：处理斜杠命令 ──

// CommandPass 把 command.System 包装成 Conduit Pass
type CommandPass struct {
	CmdSys *command.System
}

func (p *CommandPass) Execute(ctx *conduit.MessageContext) error {
	// 收集命令回复
	var replies []string
	err := p.CmdSys.Process(ctx.RawMsg, &command.Context{
		Platform:       platformFromCtx(ctx),
		PlatformUserID: platformUserIDFromCtx(ctx),
		GroupID:        ctx.GroupID,
		IsGroup:        ctx.IsGroup,
		Message:        ctx.RawMsg,
		Reply:          func(s string) { replies = append(replies, s) },
	})

	// 将回复追加到输出
	for _, r := range replies {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID:  ctx.UserID,
			GroupID: ctx.GroupID,
			Content: r,
			IsGroup: ctx.IsGroup,
		})
	}

	return err
}

// ── CommandRouterPass：解析斜杠命令并将命令信息写入 MessageContext ──

// CommandRouterPass 解析斜杠命令并将命令信息写入 MessageContext
type CommandRouterPass struct {
	CmdSys *command.System
}

const (
	commandNameKey    = "bot.command.name"
	commandArgsKey    = "bot.command.args"
	commandHandlerKey = "bot.command.handler"
)

func (p *CommandRouterPass) Execute(ctx *conduit.MessageContext) error {
	name := strings.TrimPrefix(ctx.RawMsg, "/")
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return fmt.Errorf("empty command")
	}

	cmdName := parts[0]
	cmd, ok := p.CmdSys.Lookup(cmdName)
	if !ok {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: fmt.Sprintf("未知命令: /%s\n输入 /帮助 查看可用命令", cmdName),
		})
		return nil
	}

	// 将命令信息写入 MessageContext
	args := parts[1:]
	conduit.Set(ctx, commandNameKey, cmdName)
	conduit.Set(ctx, commandArgsKey, args)
	conduit.Set(ctx, commandHandlerKey, cmd.Handler)

	return nil
}

// ── ExecuteCommandPass：从 MessageContext 读取命令信息并执行 ──

// ExecuteCommandPass 从 MessageContext 读取命令信息并执行
type ExecuteCommandPass struct{}

func (p *ExecuteCommandPass) Execute(ctx *conduit.MessageContext) error {
	nameRaw, _ := conduit.Get[string](ctx, commandNameKey)
	argsRaw, _ := conduit.Get[[]string](ctx, commandArgsKey)
	handlerRaw, _ := conduit.Get[func(*command.Context) error](ctx, commandHandlerKey)

	if handlerRaw == nil {
		return nil
	}

	var replies []string
	cmdCtx := &command.Context{
		Platform:       platformFromCtx(ctx),
		PlatformUserID: platformUserIDFromCtx(ctx),
		GroupID:        ctx.GroupID,
		IsGroup:        ctx.IsGroup,
		CommandName:    nameRaw,
		CommandArgs:    argsRaw,
		Message:        ctx.RawMsg,
		Reply:          func(s string) { replies = append(replies, s) },
	}

	if err := handlerRaw(cmdCtx); err != nil {
		return err
	}

	for _, r := range replies {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: r,
		})
	}

	return nil
}

// ── RoleplayPass：AI 角色扮演对话 ──

// RoleplayPass 调用 AI 对话服务
type RoleplayPass struct {
	Chat *ai.ChatService
	DB   *database.DB
}

func (p *RoleplayPass) Execute(ctx *conduit.MessageContext) error {
	userMsg := ctx.RawMsg
	if userMsg == "" {
		return nil
	}

	// 防御性检查：LLM 未配置时 chatSvc 为 nil，直接返回错误以触发 fallback
	if p.Chat == nil {
		return fmt.Errorf("roleplay: chat service not available")
	}

	platform := platformFromCtx(ctx)
	platformUserID := platformUserIDFromCtx(ctx)
	nickname, _ := conduit.Get[string](ctx, KeyNickname)

	// 确保用户存在
	user, err := p.DB.GetOrCreateUser(ctx.Ctx, platform, platformUserID, nickname)
	if err != nil {
		return fmt.Errorf("roleplay: get_or_create_user: %w", err)
	}

	// ChatService 内部通过 LOD 组装上下文（L2→L1→L0 + RAG）
	// 这里只传当前用户消息，不再手动拉对话历史
	resp, err := p.Chat.Chat(ctx.Ctx, &llm.ChatRequest{
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: userMsg}},
		UserID:    user.ID,
		UserName:  nickname,
		GroupName: ctx.GroupID, // 目前只有 GroupID，暂时作为 group 标识传入
	})
	if err != nil {
		return fmt.Errorf("roleplay: chat: %w", err)
	}

	// 存储本轮对话（L0 原始记录，后续由 Compressor 自动压缩）
	if err := p.DB.SaveConversation(ctx.Ctx, user.ID, "user", userMsg); err != nil {
		return conduit.NewSoftError(fmt.Errorf("roleplay: save user conversation: %w", err))
	}
	if err := p.DB.SaveConversation(ctx.Ctx, user.ID, "assistant", resp.Content); err != nil {
		return conduit.NewSoftError(fmt.Errorf("roleplay: save assistant conversation: %w", err))
	}

	// 分割输出：优先按 \n\n\n 分割（AI 主动分段），回退到按 \n\n 分割（长回复）
	// 短回复（无分隔符）保持单条消息不变
	segments := splitResponse(resp.Content)
	for _, seg := range segments {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID:  ctx.UserID,
			GroupID: ctx.GroupID,
			Content: seg,
			IsGroup: ctx.IsGroup,
		})
	}

	return nil
}

// ── 回复分割 ──

const (
	responseDelimiter    = "\n\n\n" // AI 长回复的主动分段标记
	maxResponseSegments  = 6        // 最大分段数，防止滥用
	minSegmentLen        = 30       // 最小分段长度（字符），避免过短分段
	doubleNewlineBufSize = 200      // 触发 \n\n 回退分割的最小内容长度
)

// splitResponse 将 AI 回复拆分为多条消息。
//
// 分割策略（优先级从高到低）：
//  1. 显式分段：按 \n\n\n（三个换行，AI 主动分段）分割
//  2. 隐式分段：内容较长（>200 字符）且含 \n\n（段落分隔），按 \n\n 分割
//  3. 无分段：直接返回单条消息
//
// 所有分段结果都会经过：去空 → 过滤过短分段 → 限数。
func splitResponse(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	// 策略 1：显式分段标记 \n\n\n
	if strings.Contains(content, responseDelimiter) {
		parts := splitAndFilter(content, responseDelimiter, 0)
		if len(parts) >= 2 {
			return capSegments(parts)
		}
		// 只有一段（AI 误用了分隔符），降级到策略 2
	}

	// 策略 2：长内容按段落 \n\n 隐式分割
	if len(content) >= doubleNewlineBufSize && strings.Contains(content, "\n\n") {
		parts := splitAndFilter(content, "\n\n", minSegmentLen)
		if len(parts) >= 2 {
			return capSegments(parts)
		}
	}

	// 策略 3：单条消息
	return []string{content}
}

// splitAndFilter 按分隔符分割后去空、过滤过短分段。
func splitAndFilter(content, delimiter string, minLen int) []string {
	raw := strings.Split(content, delimiter)
	result := make([]string, 0, len(raw))
	for _, p := range raw {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" && len(trimmed) >= minLen {
			result = append(result, trimmed)
		}
	}
	return result
}

// capSegments 限制分段数量。
func capSegments(segments []string) []string {
	if len(segments) > maxResponseSegments {
		return segments[:maxResponseSegments]
	}
	return segments
}

// ── FallbackPass：兜底回复 ──

// FallbackPass 超时或未匹配时的兜底
type FallbackPass struct{}

func (p *FallbackPass) Execute(ctx *conduit.MessageContext) error {
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		Content: "蓝妹现在有点迷糊，稍等一下...",
		IsGroup: ctx.IsGroup,
	})
	return nil
}

// ── 条件判断函数 ──

// IsCommand 判断消息是否以 / 开头
func IsCommand(ctx *conduit.MessageContext) bool {
	return strings.HasPrefix(ctx.RawMsg, "/")
}

// IsAdminCommand 判断消息是否以 /admin 开头
func IsAdminCommand(ctx *conduit.MessageContext) bool {
	return strings.HasPrefix(ctx.RawMsg, "/admin")
}

// ── 辅助函数 ──

func platformFromCtx(ctx *conduit.MessageContext) string {
	if raw, ok := ctx.Extra[KeyPlatform]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return "unknown"
}

func platformUserIDFromCtx(ctx *conduit.MessageContext) string {
	if raw, ok := ctx.Extra[KeyPlatformUserID]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ctx.UserID
}
