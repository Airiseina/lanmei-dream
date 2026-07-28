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

// Chat 实现 LLMClient 接口
func (c *EinoClient) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	msgs := make([]*schema.Message, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = &schema.Message{
			Role:    toSchemaRole(m.Role),
			Content: m.Content,
		}
	}

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
	}, nil
}

// toSchemaRole 将内部 Role 转为 eino schema.RoleType
func toSchemaRole(r Role) schema.RoleType {
	switch r {
	case RoleSystem:
		return schema.System
	case RoleAssistant:
		return schema.Assistant
	default:
		return schema.User
	}
}
