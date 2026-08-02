package llm

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// EinoOptions eino ChatModel 创建参数（provider 无关）
type EinoOptions struct {
	BaseURL     string  // API 基础地址（OpenAI/DeepSeek/Qwen/Moonshot/Ark/Ollama 通用）
	APIKey      string  // API 密钥
	Model       string  // 模型名
	MaxTokens   int     // 单次回复最大 token 数
	Temperature float64 // 生成温度
}

// EinoClient 基于 eino 的 LLMClient 实现。
// 通过 eino-ext 的 OpenAI 兼容层调用，天然支持多 provider。
type EinoClient struct {
	model model.BaseChatModel
}

// NewEinoClient 创建 eino LLM 客户端
func NewEinoClient(ctx context.Context, opts *EinoOptions) (*EinoClient, error) {
	mt := opts.MaxTokens
	t := float32(opts.Temperature)

	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL:     opts.BaseURL,
		APIKey:      opts.APIKey,
		Model:       opts.Model,
		MaxTokens:   &mt,
		Temperature: &t,
	})
	if err != nil {
		return nil, fmt.Errorf("llm: eino init: %w", err)
	}
	return &EinoClient{model: cm}, nil
}

// SupportsToolCalling 检查底层模型是否支持工具调用
func (c *EinoClient) SupportsToolCalling() bool {
	_, ok := c.model.(model.ToolCallingChatModel)
	return ok
}

// BaseModel 返回底层 eino BaseChatModel，供调用方直接调用 Stream/Generate。
// 用于流式工具调用循环中直接操作 schema.Message 列表。
func (c *EinoClient) BaseModel() model.BaseChatModel {
	return c.model
}

// ChatWithTools 返回绑定了工具的模型实例
func (c *EinoClient) ChatWithTools(tools []*schema.ToolInfo) (model.BaseChatModel, error) {
	tccm, ok := c.model.(model.ToolCallingChatModel)
	if !ok {
		return c.model, nil
	}
	return tccm.WithTools(tools)
}

// Chat 实现 LLMClient 接口
func (c *EinoClient) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	msgs := ToSchemaMessages(req.Messages)

	resp, err := c.model.Generate(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("llm: eino generate: %w", err)
	}

	tokensUsed := 0
	if resp.ResponseMeta != nil && resp.ResponseMeta.Usage != nil {
		tokensUsed = resp.ResponseMeta.Usage.TotalTokens
	}

	return &ChatResponse{
		Content:    resp.Content,
		TokensUsed: tokensUsed,
		ToolCalls:  ToToolCallPointers(resp.ToolCalls),
	}, nil
}

// StreamChat 实现 StreamingLLMClient 接口，以流式方式返回聊天补全。
func (c *EinoClient) StreamChat(ctx context.Context, req *ChatRequest) (*schema.StreamReader[*schema.Message], error) {
	msgs := ToSchemaMessages(req.Messages)
	reader, err := c.model.Stream(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("llm: eino stream: %w", err)
	}
	return reader, nil
}

// StreamChatWithTools 绑定工具后以流式方式返回聊天补全。
func (c *EinoClient) StreamChatWithTools(ctx context.Context, req *ChatRequest, tools []*schema.ToolInfo) (*schema.StreamReader[*schema.Message], error) {
	chatModel, err := c.ChatWithTools(tools)
	if err != nil {
		return nil, fmt.Errorf("llm: bind tools for stream: %w", err)
	}
	msgs := ToSchemaMessages(req.Messages)
	reader, err := chatModel.Stream(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("llm: eino stream with tools: %w", err)
	}
	return reader, nil
}

// ChatWithModel 使用指定模型执行对话（用于工具调用流程中切换绑定了工具的模型）
func (c *EinoClient) ChatWithModel(ctx context.Context, req *ChatRequest, chatModel model.BaseChatModel) (*ChatResponse, error) {
	msgs := ToSchemaMessages(req.Messages)

	resp, err := chatModel.Generate(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("llm: eino generate: %w", err)
	}

	tokensUsed := 0
	if resp.ResponseMeta != nil && resp.ResponseMeta.Usage != nil {
		tokensUsed = resp.ResponseMeta.Usage.TotalTokens
	}

	return &ChatResponse{
		Content:    resp.Content,
		TokensUsed: tokensUsed,
		ToolCalls:  ToToolCallPointers(resp.ToolCalls),
	}, nil
}

// ToSchemaMessages 将内部 llm.Message 列表转为 eino schema.Message 列表。
// 保留 ToolCallID 以支持多轮工具调用场景。
func ToSchemaMessages(msgs []Message) []*schema.Message {
	result := make([]*schema.Message, len(msgs))
	for i, m := range msgs {
		schemaMsg := &schema.Message{
			Role:    ToSchemaRole(m.Role),
			Content: m.Content,
		}
		if m.ToolCallID != "" {
			schemaMsg.ToolCallID = m.ToolCallID
		}
		result[i] = schemaMsg
	}
	return result
}

// ToSchemaRole 将内部 Role 转为 eino schema.RoleType
func ToSchemaRole(r Role) schema.RoleType {
	switch r {
	case RoleSystem:
		return schema.System
	case RoleAssistant:
		return schema.Assistant
	case RoleTool:
		return schema.Tool
	default:
		return schema.User
	}
}

// ToToolCallPointers 将 []schema.ToolCall 转为 []*schema.ToolCall
func ToToolCallPointers(calls []schema.ToolCall) []*schema.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	result := make([]*schema.ToolCall, len(calls))
	for i := range calls {
		result[i] = &calls[i]
	}
	return result
}
