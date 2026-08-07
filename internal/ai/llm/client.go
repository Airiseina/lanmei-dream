package llm

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// Role 表示对话消息的角色
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 表示一条对话消息
type Message struct {
	Role         Role   `json:"role"`
	Content      string `json:"content"`
	ToolCallID   string `json:"tool_call_id,omitempty"`   // tool 角色消息必须携带
	ToolCallName string `json:"tool_call_name,omitempty"` // 工具名称
}

// ChatRequest 是一次对话请求的入参
type ChatRequest struct {
	Messages  []Message `json:"messages"`
	UserID    int64     `json:"user_id"`
	UserName  string    `json:"user_name"`  // 用户昵称，供 prompt 组装使用
	GroupName string    `json:"group_name"` // 群组名称，供 prompt 组装使用
}

// ChatResponse 是对话服务的返回
type ChatResponse struct {
	Content       string             `json:"content"`
	TokensUsed    int                `json:"tokens_used"`
	ToolCalls     []*schema.ToolCall `json:"tool_calls,omitempty"`      // LLM 返回的工具调用
	InvolvedTools []string           `json:"involved_tools,omitempty"`  // 本次对话中实际调用的工具名列表
}

// LLMClient 抽象大语言模型的对话能力。
// 具体实现（OpenAI / 本地模型等）由外部注入。
type LLMClient interface {
	// Chat 执行一次对话补全
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
}

// StreamingLLMClient 是 LLMClient 的可选扩展接口，表示支持流式响应。
// EinoClient 默认实现此接口（eino BaseChatModel.Stream）。
// 调用方通过类型断言检查是否支持流式：
//
//	if sc, ok := client.(StreamingLLMClient); ok { ... }
type StreamingLLMClient interface {
	LLMClient
	// StreamChat 以流式方式返回聊天补全。
	// 调用方负责通过 StreamReader.Recv 消费 chunk，并在结束时调用 Close。
	StreamChat(ctx context.Context, req *ChatRequest) (*schema.StreamReader[*schema.Message], error)
	// StreamChatWithTools 绑定工具后以流式方式返回聊天补全。
	StreamChatWithTools(ctx context.Context, req *ChatRequest, tools []*schema.ToolInfo) (*schema.StreamReader[*schema.Message], error)
}
