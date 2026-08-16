package tool

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// Tool 描述一个可供 AI 调用的工具
type Tool struct {
	// Info Eino 标准工具元信息，可直接传给 ToolCallingChatModel.WithTools()
	Info *schema.ToolInfo
	// Handler 工具执行函数，argsJSON 为 LLM 传来的 JSON 编码参数
	Handler func(ctx context.Context, argsJSON string) (string, error)
}

// CallerIdentity 工具调用者身份：当前对话发送者的平台标识。
// 由工具调用循环在执行 handler 前注入 ctx，工具据此识别"现在是谁在对话"，
// 无需 LLM 传递 user_id（平台 ID 字符串，与 conduit MessageContext.UserID 同源，
// 也即插件 KV 键所用的用户标识），从根上避免模型抄写 ID 出错。
type CallerIdentity struct {
	// Platform 消息平台（qq/wechat/telegram/napcat…）
	Platform string
	// PlatformUserID 发送者平台用户 ID 字符串（如 QQ 号 "123456"）
	PlatformUserID string
}

// callerKey ctx 键类型（私有，避免与其他包的 WithValue 键冲突）
type callerKey struct{}

// WithCaller 将调用者身份注入 ctx，供工具 handler 读取。
func WithCaller(ctx context.Context, id CallerIdentity) context.Context {
	return context.WithValue(ctx, callerKey{}, id)
}

// CallerFrom 从 ctx 读取调用者身份；未注入时返回零值与 false。
func CallerFrom(ctx context.Context) (CallerIdentity, bool) {
	id, ok := ctx.Value(callerKey{}).(CallerIdentity)
	return id, ok
}
