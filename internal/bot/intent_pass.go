package bot

import (
	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/intent"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/ai/tool"
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
	logger   *zap.Logger
}

func (p *IntentPass) Execute(ctx *conduit.MessageContext) error {
	result, err := p.Analyzer.Analyze(ctx.Ctx, ctx.RawMsg)
	if err != nil {
		p.logger.Error("intent: analyze failed, falling back to chat", zap.Error(err))
		// 意图分析失败，降级为聊天
		return p.routeToChat(ctx)
	}

	p.logger.Info("intent: result",
		zap.String("msg", truncate(ctx.RawMsg, 20)),
		zap.String("intent", string(result.Intent)),
		zap.String("command", result.CommandName),
		zap.Float64("confidence", result.Confidence),
	)

	switch result.Intent {
	case intent.IntentCommand:
		return p.routeToCommand(ctx, result.CommandName)
	case intent.IntentTool:
		// 工具意图：让 AI 在对话中调用对应工具
		return p.routeToChat(ctx)
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
	// 将自然语言命令转换为命令处理，通过 MessageContext 传递命令信息
	if cmdName != "" {
		// Set command info directly in MessageContext
		conduit.Set(ctx, commandNameKey, cmdName)
		cmd, ok := p.Command.CmdSys.Lookup(cmdName)
		if !ok {
			return p.routeToChat(ctx)
		}
		conduit.Set(ctx, commandHandlerKey, cmd.Handler)
		conduit.Set(ctx, commandArgsKey, []string{})
		return (&ExecuteCommandPass{}).Execute(ctx)
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

// BuildIntentTools 从 tool.Registry 提取工具定义，用于意图分析 prompt
func BuildIntentTools(toolReg *tool.Registry) []intent.ToolDef {
	if toolReg == nil {
		return nil
	}
	infos := toolReg.ToolInfos()
	defs := make([]intent.ToolDef, len(infos))
	for i, info := range infos {
		defs[i] = intent.ToolDef{
			Name:        info.Name,
			Description: info.Desc,
		}
	}
	return defs
}

// NewIntentPass 创建完整的 IntentPass（需要 LLM 可用）
func NewIntentPass(llmClient llm.LLMClient, cmdSys *command.System, chatSvc *ai.ChatService, db *database.DB, toolReg *tool.Registry, logger *zap.Logger) *IntentPass {
	cmdDefs := BuildIntentCommands(cmdSys)
	toolDefs := BuildIntentTools(toolReg)
	analyzer := intent.NewAnalyzer(llmClient, cmdDefs, toolDefs)

	ip := &IntentPass{
		Analyzer: analyzer,
		Command:  &CommandPass{CmdSys: cmdSys},
		Fallback: &FallbackPass{},
		logger:   logger,
	}

	if chatSvc != nil && db != nil {
		ip.Roleplay = &RoleplayPass{Chat: chatSvc, DB: db}
	}

	return ip
}
