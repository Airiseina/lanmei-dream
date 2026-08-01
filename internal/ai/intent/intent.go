// Package intent 实现意图分析系统，用于在消息进入对话流程前判断用户意图。
//
// 意图分析是消息路由的核心环节：用户消息可能不是简单的聊天，
// 而是命令调用（如"帮我签到"）或工具调用（如"今天天气怎么样"）。
// 意图分析器通过 LLM 对消息进行分类，将消息路由到正确的处理通道：
//   - IntentChat → 进入 ChatService 的 RAG 对话流程
//   - IntentCommand → 路由到命令处理器执行对应命令
//   - IntentTool → 路由到工具注册表调用对应 AI 工具
//   - IntentIgnore → 丢弃消息（如表情包、系统通知等无需回复的内容）
//
// 降级策略：当 LLM 不可用或返回无效结果时，所有消息降级为 IntentChat，
// 确保系统在异常情况下仍能正常提供对话服务。
package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
)

// Intent 表示意图分析的分类结果。
//
// 四种意图类型覆盖了消息路由的所有场景：
//   - chat：通用对话，进入 LLM 对话流程
//   - command：命令调用，匹配 CommandDef 中的命令名
//   - tool：工具调用，匹配 ToolDef 中的工具名
//   - ignore：无需回复，跳过后续处理
type Intent string

const (
	IntentChat    Intent = "chat"    // 闲聊/角色扮演/一般提问
	IntentCommand Intent = "command" // 命令调用（如"帮我签到"→命中"签到"命令）
	IntentTool    Intent = "tool"    // 工具调用（如"今天天气怎么样"→命中"weather"工具）
	IntentIgnore  Intent = "ignore"  // 无需回复（如系统通知、表情包、无意义重复等）
)

// Result 是意图分析的完整结果，包含意图分类和附加信息。
//
// 字段说明：
//   - Intent：意图类型（chat/command/tool/ignore）
//   - CommandName：当 Intent=command 时，命中的命令名；否则为空
//   - ToolName：当 Intent=tool 时，命中的工具名；否则为空
//   - Confidence：LLM 对此次分类的置信度（0~1），低于阈值时可触发二次确认
type Result struct {
	Intent      Intent  `json:"intent"`     // 意图类型
	CommandName string  `json:"command"`    // 当 Intent=command 时，命中的命令名
	ToolName    string  `json:"tool"`       // 当 Intent=tool 时，命中的工具名
	Confidence  float64 `json:"confidence"` // 置信度 0~1
}

// Analyzer 通过 LLM 分析用户消息的意图。
//
// 工作原理：
//   - 将可用命令列表和工具列表注入到 system prompt 中
//   - LLM 根据用户消息内容，从 chat/command/tool/ignore 四种意图中选择最匹配的
//   - 返回结构化的 JSON 结果，包含意图类型、命中的命令/工具名和置信度
//
// 与命令/工具系统的集成：
//   - commands 参数来自命令注册表，定义了所有可用的用户命令
//   - tools 参数来自工具注册表（AI Tool），定义了所有可调用的 AI 工具
//   - Analyzer 不执行命令或工具，只负责分类——执行由上层路由逻辑完成
type Analyzer struct {
	llmClient llm.LLMClient
	commands  []CommandDef // 可用命令列表（注入到 prompt 中供 LLM 匹配）
	tools     []ToolDef    // 可用工具列表（注入到 prompt 中供 LLM 匹配）
}

// CommandDef 描述一个可用命令，用于构建意图分析 prompt。
// LLM 会根据命令名和描述判断用户消息是否匹配某个命令。
type CommandDef struct {
	Name        string // 命令名（如 "签到"）
	Description string // 命令描述（如 "每日签到领取积分"）
}

// ToolDef 描述一个 AI 工具，用于构建意图分析 prompt。
// LLM 会根据工具名和描述判断用户消息是否需要调用某个工具。
type ToolDef struct {
	Name        string // 工具名（如 "weather"）
	Description string // 工具描述（如 "查询天气信息"）
}

// NewAnalyzer 创建意图分析器。
//
// 参数：
//   - llmClient: LLM 客户端，为 nil 时 Analyze 降级返回 IntentChat
//   - commands: 可用命令定义列表
//   - tools: 可用工具定义列表
func NewAnalyzer(llmClient llm.LLMClient, commands []CommandDef, tools []ToolDef) *Analyzer {
	return &Analyzer{
		llmClient: llmClient,
		commands:  commands,
		tools:     tools,
	}
}

// UpdateCommands 动态更新命令列表，供插件注册新命令后同步。
func (a *Analyzer) UpdateCommands(commands []CommandDef) {
	a.commands = commands
}

// UpdateTools 动态更新工具列表，供插件注册新工具后同步。
func (a *Analyzer) UpdateTools(tools []ToolDef) {
	a.tools = tools
}

// Analyze 分析用户消息的意图。
//
// 处理流程：
//  1. LLM 未配置 → 降级返回 IntentChat（所有消息走聊天）
//  2. 构建意图分析 system prompt（包含可用命令和工具列表）
//  3. 调用 LLM 进行意图分类
//  4. 解析 LLM 返回的 JSON 结果
//
// 降级策略：LLM 调用失败或返回无效结果时，降级为 IntentChat，
// 确保系统不会因为意图分析故障而无法响应用户。
//
// 参数：
//   - ctx: 上下文
//   - userMsg: 用户消息文本
//
// 返回：
//   - *Result: 意图分析结果
//   - error: LLM 调用失败时返回错误
func (a *Analyzer) Analyze(ctx context.Context, userMsg string) (*Result, error) {
	if a.llmClient == nil {
		// LLM 未配置，降级：所有消息都当聊天处理
		return &Result{Intent: IntentChat, Confidence: 1.0}, nil
	}

	// 构建包含命令和工具信息的 system prompt
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

// buildPrompt 构建意图分析的 system prompt。
//
// prompt 包含四个部分：
//  1. 角色定义：告知 LLM 它是一个意图分类器
//  2. 可用意图：列出 chat/command/tool/ignore 四种意图及含义
//  3. 可用命令/工具：从 Analyzer 的 commands 和 tools 列表动态注入
//  4. 输出格式：要求 LLM 仅输出 JSON，并提供示例
//
// 这种 prompt 设计使 LLM 能基于命令/工具的语义描述进行匹配，
// 而非简单的关键词匹配，从而提高意图识别的准确率。
func (a *Analyzer) buildPrompt() string {
	var sb strings.Builder

	sb.WriteString(`你是一个意图分类器。根据用户消息判断其意图，返回 JSON 格式结果。

## 可用意图
- "chat": 闲聊、提问、角色扮演对话
- "command": 用户想执行某个操作/命令
- "tool": 用户想使用某个工具/功能
- "ignore": 无需回复的内容（纯表情、系统通知、无意义重复等）

## 可用命令
`)

	// 动态注入命令列表，让 LLM 了解每个命令的语义
	if len(a.commands) == 0 {
		sb.WriteString("（暂无可用命令）\n")
	} else {
		for _, cmd := range a.commands {
			fmt.Fprintf(&sb, "- %s: %s\n", cmd.Name, cmd.Description)
		}
	}

	// 动态注入工具列表，让 LLM 了解每个工具的功能
	sb.WriteString("\n## 可用工具\n")
	if len(a.tools) == 0 {
		sb.WriteString("（暂无可用工具）\n")
	} else {
		for _, t := range a.tools {
			fmt.Fprintf(&sb, "- %s: %s\n", t.Name, t.Description)
		}
	}

	sb.WriteString(`
## 输出格式
仅输出 JSON，不要其他内容：
{"intent":"chat|command|tool|ignore","command":"命令名（仅 intent=command 时填写）","tool":"工具名（仅 intent=tool 时填写）","confidence":0.95}

## 示例
用户: "你好呀" → {"intent":"chat","command":"","tool":"","confidence":0.98}
用户: "帮我签到" → {"intent":"command","command":"签到","tool":"","confidence":0.95}
用户: "今天天气怎么样" → {"intent":"tool","command":"","tool":"weather","confidence":0.9}
用户: "[动画表情]" → {"intent":"ignore","command":"","tool":"","confidence":0.9}
`)

	return sb.String()
}

// parseResult 解析 LLM 返回的意图分析 JSON。
//
// 容错处理：
//   - LLM 可能将 JSON 包裹在 markdown 代码块（```json ... ```）中，
//     通过定位第一个 "{" 和最后一个 "}" 来提取有效 JSON
//   - JSON 解析失败时降级为 IntentChat（置信度 0.5，表示不确定）
//   - 意图值不在预定义范围内时降级为 IntentChat
//
// 这些容错措施确保即使 LLM 输出格式不规范，系统也能正常工作。
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
		// 解析失败，降级为 chat（置信度 0.5 表示低确定性）
		return &Result{Intent: IntentChat, Confidence: 0.5}, nil
	}

	// 校验意图值，防止 LLM 返回非法意图类型
	switch result.Intent {
	case IntentChat, IntentCommand, IntentTool, IntentIgnore:
		// valid
	default:
		// 未知意图降级为 chat
		result.Intent = IntentChat
	}

	return &result, nil
}
