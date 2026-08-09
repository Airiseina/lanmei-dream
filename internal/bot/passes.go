package bot

import (
	"fmt"
	"strings"

	"github.com/zrurf/conduit"

	"github.com/DaWesen/lanmei-dream/internal/command"
)

// ── 上下文键 ──

const (
	KeyPlatform       = "platform"         // string 平台标识（qq/wechat/telegram/...）
	KeyPlatformUserID = "platform_user_id" // string 平台用户 ID
	KeyNickname       = "nickname"         // string 昵称
	KeyMessageID      = "message_id"       // string 消息 ID
	KeyConnID         = "conn_id"          // string 来源连接 ID
	KeySelfID         = "self_id"          // string 机器人自身 ID
	KeyIsSegment      = "bot.is_segment"   // bool 标记流式段落重入消息
	KeyStreamChannel  = "bot.stream.ch"    // chan string 流式段落通道

	// 通知事件键：普通消息下 EventType 为空串、EventData 为 nil
	KeyEventType    = "bot.event.type"     // string 规范化事件类型（空=普通消息）
	KeyEventSubType = "bot.event.sub_type" // string 事件子类型
	KeyEventData    = "bot.event.data"     // map[string]any 事件全字段
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

// IsSegment 判断消息是否为流式段落重入消息。
// 由 Bot.streamSegments 在提交子消息时通过 InputMessage.Extra 标记，
// BT 据此路由到段落交付管线。
//
// 注意：KeyIsSegment 设置在 ctx.Extra（来自 InputMessage.Extra），
// 不能用 conduit.Get（从 ctx.data 读取），必须直接读 ctx.Extra。
func IsSegment(ctx *conduit.MessageContext) bool {
	if raw, ok := ctx.Extra[KeyIsSegment]; ok {
		if b, ok := raw.(bool); ok {
			return b
		}
	}
	return false
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

// nicknameFromCtx 从 ctx.Extra 读取用户昵称。
// KeyNickname 由 OnMessage 通过 InputMessage.Extra 设置，存储在 ctx.Extra 中，
// 不能用 conduit.Get（从 ctx.data 读取）。
func nicknameFromCtx(ctx *conduit.MessageContext) string {
	if raw, ok := ctx.Extra[KeyNickname]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ""
}
