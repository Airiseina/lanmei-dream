// Package bot 实现 Conduit 行为树的意图分析节点。
//
// 设计说明：
// IntentAnalysisPass 实现 conduit.RouterPass 接口，在 Execute 中执行 LLM 意图分析，
// 在 Route 中根据分析结果动态路由到对应管线（roleplay / command_exec / ignore / fallback）。
// 相比传统的 Condition + Selector 方案，RouterPass 消除了「BT Tick 时分析结果尚未写入」
// 的时序问题，因为 Route 在 Execute 之后才被引擎调用。
package bot

import (
	"fmt"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai"
	"github.com/DaWesen/lanmei-dream/internal/ai/intent"
	"github.com/DaWesen/lanmei-dream/internal/ai/tool"
	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// intentResultKey 存储意图分析结果的上下文键
const intentResultKey = "bot.intent.result"

// ── IntentAnalysisPass（RouterPass：LLM 意图分析 + 动态路由） ──

// IntentAnalysisPass 实现 conduit.RouterPass。
// Execute 调用 LLM 分析意图并写入 MessageContext，
// Route 根据分析结果返回对应的管线 ID。
type IntentAnalysisPass struct {
	Analyzer *intent.Analyzer
	ChatSvc  *ai.ChatService // nil 时 IntentChat/IntentTool 走 fallback
	logger   *zap.Logger
}

func (p *IntentAnalysisPass) Execute(ctx *conduit.MessageContext) error {
	if p.Analyzer == nil {
		p.logger.Warn("intent: analyzer is nil, defaulting to chat")
		conduit.Set(ctx, intentResultKey, &intent.Result{Intent: intent.IntentChat, Confidence: 1.0})
		return nil
	}
	result, err := p.Analyzer.Analyze(ctx.Ctx, ctx.RawMsg)
	if err != nil {
		p.logger.Error("intent: analyze failed, defaulting to chat",
			zap.Error(err),
			zap.String("msg", truncate(ctx.RawMsg, 32)),
		)
		result = &intent.Result{Intent: intent.IntentChat, Confidence: 0.5}
	} else {
		p.logger.Info("intent: result",
			zap.String("msg", truncate(ctx.RawMsg, 20)),
			zap.String("intent", string(result.Intent)),
			zap.String("command", result.CommandName),
			zap.Float64("confidence", result.Confidence),
		)
	}
	conduit.Set(ctx, intentResultKey, result)
	return nil
}

// Route 实现 conduit.RouterPass，根据意图分析结果动态路由到对应管线。
func (p *IntentAnalysisPass) Route(ctx *conduit.MessageContext) (string, error) {
	result, ok := conduit.Get[*intent.Result](ctx, intentResultKey)
	if !ok || result == nil {
		return "pipeline.fallback", nil
	}
	switch result.Intent {
	case intent.IntentChat, intent.IntentTool:
		if p.ChatSvc == nil {
			return "pipeline.fallback", nil
		}
		return "pipeline.roleplay", nil
	case intent.IntentCommand:
		return "pipeline.intent_command_exec", nil
	case intent.IntentIgnore:
		return "pipeline.intent_ignore", nil
	default:
		return "pipeline.fallback", nil
	}
}

// ── IntentIgnorePass：静默忽略 ──

// IntentIgnorePass 保存消息到对话历史但不生成回复。
// 适用于用户发送了无需回复的内容（表情包、系统通知等）。
type IntentIgnorePass struct {
	DB *database.DB
}

func (p *IntentIgnorePass) Execute(ctx *conduit.MessageContext) error {
	// 保存用户消息到对话历史（供后续压缩/记忆使用）
	platform := platformFromCtx(ctx)
	platformUserID := platformUserIDFromCtx(ctx)
	nickname := nicknameFromCtx(ctx)
	user, err := p.DB.GetOrCreateUser(ctx.Ctx, platform, platformUserID, nickname)
	if err != nil {
		return conduit.NewSoftError(fmt.Errorf("intent_ignore: get or create user: %w", err))
	}
	if err := p.DB.SaveConversation(ctx.Ctx, user.ID, ctx.GroupID, "user", ctx.RawMsg, model.SourceChat, ""); err != nil {
		return conduit.NewSoftError(fmt.Errorf("intent_ignore: save conversation: %w", err))
	}
	// 不生成任何回复 → 静默忽略
	return nil
}

// ── IntentCommandExecPass：从意图结果执行命令 ──

// IntentCommandExecPass 从意图分析结果中提取命令名并执行对应命令。
// 其内部复用 ExecuteCommandPass 的执行逻辑。
type IntentCommandExecPass struct {
	CmdSys *command.System
}

func (p *IntentCommandExecPass) Execute(ctx *conduit.MessageContext) error {
	result, ok := conduit.Get[*intent.Result](ctx, intentResultKey)
	if !ok || result == nil || result.CommandName == "" {
		return nil
	}

	cmd, ok := p.CmdSys.Lookup(result.CommandName)
	if !ok {
		// 命令不存在时回复提示消息，避免静默降级到 fallback
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID: ctx.UserID, GroupID: ctx.GroupID, IsGroup: ctx.IsGroup,
			Content: fmt.Sprintf("抱歉，我不认识命令「%s」", result.CommandName),
		})
		return nil
	}

	// 将命令信息写入 MessageContext，复用 ExecuteCommandPass
	conduit.Set(ctx, commandNameKey, result.CommandName)
	conduit.Set(ctx, commandArgsKey, []string{})
	conduit.Set(ctx, commandHandlerKey, cmd.Handler)

	return (&ExecuteCommandPass{}).Execute(ctx)
}

// ── 构建辅助 ──

// BuildIntentCommands 从 command.System 提取命令定义列表，用于意图分析 prompt。
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

// BuildIntentTools 从 tool.Registry 提取工具定义列表，用于意图分析 prompt。
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

// truncate 按 rune 截断字符串用于日志输出，避免切断多字节 UTF-8 字符。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}
