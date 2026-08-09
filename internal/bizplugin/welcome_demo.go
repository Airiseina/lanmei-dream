package bizplugin

import (
	"fmt"

	"github.com/DaWesen/lanmei-dream/internal/gateway"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// ============================================================
// WelcomeDemoPlugin 互动事件演示插件
// ============================================================

// WelcomeDemoPlugin 演示"插件子树消费 notice 事件"的标准范式：
//   - 子树订阅进群事件（group_member_increase）
//   - 从黑板块读取事件上下文（消息类型 / notice 类型 / 事件详情）
//   - 本轮仅记录日志（占位），不实现真实欢迎语业务
//
// 行为树：
//
//	subtree.plugin.welcome_demo → Sequence(
//	    IsNotice,                        // 仅处理 notice 事件
//	    IsGroupMemberIncrease,           // 仅处理进群事件
//	    Action("pipeline.plugin.welcome_demo.notice"),
//	)
//
// 真实插件可在此范式上扩展：通过 Hub 发送欢迎语（如 SendAtText）、
// 配合 EventOnce（进群 24h 内只欢迎一次）实现防刷。
type WelcomeDemoPlugin struct {
	logger *zap.Logger
}

// NewWelcomeDemoPlugin 创建欢迎演示插件。
func NewWelcomeDemoPlugin(logger *zap.Logger) *WelcomeDemoPlugin {
	return &WelcomeDemoPlugin{logger: logger}
}

// 黑板键常量（与 internal/bot 包保持一致；插件不 import bot 以避免循环依赖）
const (
	extraMessageType  = "bot.message_type"  // gateway.MessageType
	extraNoticeType   = "bot.notice_type"   // gateway.NoticeType
	extraNoticeDetail = "bot.notice_detail" // *gateway.NoticeDetail
)

// Info 返回插件元信息。
func (p *WelcomeDemoPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "welcome_demo",
		Name:        "欢迎演示",
		Description: "演示插件消费进群等互动事件（占位实现，仅日志）",
		Version:     "1.0.0",
		SubtreeID:   pluginpkg.SubtreeID("welcome_demo"),
	}
}

// OnInit 注册事件处理管线与订阅子树。
func (p *WelcomeDemoPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	// 注册事件处理 Pass
	executePassID := pluginpkg.PassID("welcome_demo", "execute")
	if err := ctx.Engine.RegisterPass(executePassID, &welcomeDemoPass{logger: p.logger}); err != nil {
		return fmt.Errorf("register welcome_demo pass: %w", err)
	}
	ctx.Registry.TrackPass("welcome_demo", executePassID)

	// 注册事件处理管线
	pipelineID := pluginpkg.PipelineID("welcome_demo", "notice")
	if err := ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(pipelineID, executePassID)); err != nil {
		return fmt.Errorf("register welcome_demo pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("welcome_demo", pipelineID)

	// 注册订阅子树：仅处理进群事件
	subtree := conduit.NewSequence(
		conduit.NewCondition(isNoticeEvent),
		conduit.NewCondition(isGroupMemberIncrease),
		conduit.NewAction(pipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("welcome_demo"), subtree); err != nil {
		return fmt.Errorf("register welcome_demo subtree: %w", err)
	}

	return nil
}

// OnStart 无需后台任务。
func (p *WelcomeDemoPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 无需清理资源。
func (p *WelcomeDemoPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// ============================================================
// 条件与 Pass 实现
// ============================================================

// isNoticeEvent 判断是否为 notice 事件。
func isNoticeEvent(ctx *conduit.MessageContext) bool {
	mt, _ := ctx.Extra[extraMessageType].(gateway.MessageType)
	return mt == gateway.MessageTypeNotice
}

// isGroupMemberIncrease 判断是否为进群事件。
func isGroupMemberIncrease(ctx *conduit.MessageContext) bool {
	nt, _ := ctx.Extra[extraNoticeType].(gateway.NoticeType)
	return nt == gateway.NoticeGroupMemberIncrease
}

// welcomeDemoPass 进群事件处理 Pass（占位：仅记录日志，不回复）。
type welcomeDemoPass struct {
	logger *zap.Logger
}

// Execute 记录进群事件上下文。
func (p *welcomeDemoPass) Execute(ctx *conduit.MessageContext) error {
	detail, _ := ctx.Extra[extraNoticeDetail].(*gateway.NoticeDetail)

	logger := p.logger
	if logger == nil {
		logger = zap.NewNop()
	}

	fields := []zap.Field{
		zap.String("group", ctx.GroupID),
		zap.String("new_member", ctx.UserID),
	}
	if detail != nil {
		if detail.OperatorID != "" {
			fields = append(fields, zap.String("operator", detail.OperatorID))
		}
	}
	// 占位：真实欢迎语业务（如 SendAtText 发送欢迎消息 + EventOnce 防刷）
	// 将在插件内按此范式实现，本轮不实现。
	logger.Info("welcome_demo: 捕获进群事件（占位，未实现真实欢迎语）", fields...)
	return nil
}
