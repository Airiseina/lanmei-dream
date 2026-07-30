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
