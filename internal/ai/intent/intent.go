package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
)

// Intent 表示意图分析的结果
type Intent string

const (
	IntentChat    Intent = "chat"    // 闲聊/角色扮演
	IntentCommand Intent = "command" // 命令调用
	IntentIgnore  Intent = "ignore"  // 无需回复（如系统通知、表情包等）
)

// Result 是意图分析的完整结果
type Result struct {
	Intent      Intent  `json:"intent"`     // 意图类型
	CommandName string  `json:"command"`    // 当 Intent=command 时，命中的命令名
	Confidence  float64 `json:"confidence"` // 置信度 0~1
}

// Analyzer 通过 LLM 分析用户消息的意图
type Analyzer struct {
	llmClient llm.LLMClient
	commands  []CommandDef // 可用命令列表
}

// CommandDef 描述一个可用命令，用于构建意图分析 prompt
type CommandDef struct {
	Name        string // 命令名（如 "签到"）
	Description string // 命令描述（如 "每日签到领取积分"）
}

// NewAnalyzer 创建意图分析器
// llmClient 为 nil 时 Analyze 返回 IntentChat（降级为纯聊天模式）
func NewAnalyzer(llmClient llm.LLMClient, commands []CommandDef) *Analyzer {
	return &Analyzer{
		llmClient: llmClient,
		commands:  commands,
	}
}

// Analyze 分析用户消息意图
// 当 LLM 不可用时降级返回 IntentChat（所有非 / 消息都走聊天）
func (a *Analyzer) Analyze(ctx context.Context, userMsg string) (*Result, error) {
	if a.llmClient == nil {
		// LLM 未配置，降级：所有消息都当聊天处理
		return &Result{Intent: IntentChat, Confidence: 1.0}, nil
	}

	prompt := a.buildPrompt()

	resp, err := a.llmClient.Chat(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompt},
			{Role: llm.RoleUser, Content: userMsg},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("intent: llm call: %w", err)
	}

	return parseResult(resp.Content)
}

// buildPrompt 构建意图分析的 system prompt
func (a *Analyzer) buildPrompt() string {
	var sb strings.Builder

	sb.WriteString(`你是一个意图分类器。根据用户消息判断其意图，返回 JSON 格式结果。

## 可用意图
- "chat": 闲聊、提问、角色扮演对话
- "command": 用户想执行某个操作/命令
- "ignore": 无需回复的内容（纯表情、系统通知、无意义重复等）

## 可用命令
`)

	if len(a.commands) == 0 {
		sb.WriteString("（暂无可用命令）\n")
	} else {
		for _, cmd := range a.commands {
			fmt.Fprintf(&sb, "- %s: %s\n", cmd.Name, cmd.Description)
		}
	}

	sb.WriteString(`
## 输出格式
仅输出 JSON，不要其他内容：
{"intent":"chat|command|ignore","command":"命令名（仅 intent=command 时填写）","confidence":0.95}

## 示例
用户: "你好呀" → {"intent":"chat","command":"","confidence":0.98}
用户: "帮我签到" → {"intent":"command","command":"签到","confidence":0.95}
用户: "[动画表情]" → {"intent":"ignore","command":"","confidence":0.9}
`)

	return sb.String()
}

// parseResult 解析 LLM 返回的 JSON
func parseResult(raw string) (*Result, error) {
	// 提取 JSON 部分（容错：LLM 可能包裹在 ```json ... ``` 中）
	raw = strings.TrimSpace(raw)
	if start := strings.Index(raw, "{"); start != -1 {
		if end := strings.LastIndex(raw, "}"); end > start {
			raw = raw[start : end+1]
		}
	}

	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		// 解析失败，降级为 chat
		return &Result{Intent: IntentChat, Confidence: 0.5}, nil
	}

	// 校验意图值
	switch result.Intent {
	case IntentChat, IntentCommand, IntentIgnore:
		// valid
	default:
		result.Intent = IntentChat
	}

	return &result, nil
}
