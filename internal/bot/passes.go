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
		Messages: []llm.Message{{Role: llm.RoleUser, Content: userMsg}},
		UserID:   user.ID,
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

	// 输出
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		Content: resp.Content,
		IsGroup: ctx.IsGroup,
	})

	return nil
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
