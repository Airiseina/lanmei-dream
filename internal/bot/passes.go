package bot

import (
	"fmt"
	"strings"

	"github.com/zrurf/conduit"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/database"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
)

// ── 上下文键 ──

const (
	KeyPlatform       = "platform"        // string 平台标识（qq/wechat/telegram/...）
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
// 跳过由插件命令处理器发起的请求（SkipCommandKey），防止无限递归
func IsCommand(ctx *conduit.MessageContext) bool {
	if _, ok := ctx.Extra[pluginpkg.SkipCommandKey]; ok {
		return false
	}
	return strings.HasPrefix(ctx.RawMsg, "/")
}

// IsAdminCommand 判断消息是否以 /admin 开头
// 同样跳过插件命令处理器发起的请求
func IsAdminCommand(ctx *conduit.MessageContext) bool {
	if _, ok := ctx.Extra[pluginpkg.SkipCommandKey]; ok {
		return false
	}
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
