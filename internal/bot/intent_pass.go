package bot

import (
	"log"

	"github.com/zrurf/conduit"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/intent"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/database"
)

// IntentPass 通过 LLM 意图分析决定路由：command → CommandPass，chat → RoleplayPass
// 当 LLM 不可用时降级为纯聊天（与之前行为一致）
type IntentPass struct {
	Analyzer *intent.Analyzer
	Command  *CommandPass
	Roleplay *RoleplayPass
	Fallback *FallbackPass
}

func (p *IntentPass) Execute(ctx *conduit.MessageContext) error {
	result, err := p.Analyzer.Analyze(ctx.Ctx, ctx.RawMsg)
	if err != nil {
		log.Printf("intent: analyze failed: %v, falling back to chat", err)
		// 意图分析失败，降级为聊天
		return p.routeToChat(ctx)
	}

	log.Printf("intent: msg=%q → intent=%s command=%s confidence=%.2f",
		truncate(ctx.RawMsg, 20), result.Intent, result.CommandName, result.Confidence)

	switch result.Intent {
	case intent.IntentCommand:
		return p.routeToCommand(ctx, result.CommandName)
	case intent.IntentIgnore:
		return nil // 静默忽略
	case intent.IntentChat:
		fallthrough
	default:
		return p.routeToChat(ctx)
	}
}

// routeToCommand 把消息路由到命令处理
func (p *IntentPass) routeToCommand(ctx *conduit.MessageContext, cmdName string) error {
	// 将自然语言命令转换为 /命令 格式，复用 CommandPass
	// 例如 "帮我签到" → "/签到"
	if cmdName != "" {
		// 保存原始消息，修改为 /命令 格式
		originalMsg := ctx.RawMsg
		ctx.RawMsg = "/" + cmdName
		defer func() { ctx.RawMsg = originalMsg }()
	}
	return p.Command.Execute(ctx)
}

// routeToChat 把消息路由到角色扮演
func (p *IntentPass) routeToChat(ctx *conduit.MessageContext) error {
	if p.Roleplay != nil {
		return p.Roleplay.Execute(ctx)
	}
	return p.Fallback.Execute(ctx)
}

// truncate 截断字符串用于日志
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ── 构建辅助 ──

// BuildIntentCommands 从 command.System 提取命令定义，用于意图分析 prompt
func BuildIntentCommands(cmdSys *command.System) []intent.CommandDef {
	cmds := cmdSys.List()
	defs := make([]intent.CommandDef, len(cmds))
	for i, cmd := range cmds {
		defs[i] = intent.CommandDef{
			Name:        cmd.Name,
			Description: cmd.Description,
		}
	}
	return defs
}

// NewIntentPass 创建完整的 IntentPass（需要 LLM 可用）
func NewIntentPass(llmClient llm.LLMClient, cmdSys *command.System, chatSvc *ai.ChatService, db *database.DB) *IntentPass {
	cmdDefs := BuildIntentCommands(cmdSys)
	analyzer := intent.NewAnalyzer(llmClient, cmdDefs)

	ip := &IntentPass{
		Analyzer: analyzer,
		Command:  &CommandPass{CmdSys: cmdSys},
		Fallback: &FallbackPass{},
	}

	if chatSvc != nil && db != nil {
		ip.Roleplay = &RoleplayPass{Chat: chatSvc, DB: db}
	}

	return ip
}
