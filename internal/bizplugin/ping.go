package bizplugin

import (
	"context"
	"fmt"
	"strings"

	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
)

// PingPlugin 实现 Ping/Pong 健康检查：收到 /ping 回复 Pong!。
//
// 插件 ID ping；命令 /ping，工具 ping；不依赖任何外部服务与插件状态存储。
type PingPlugin struct{}

// NewPingPlugin 创建 Ping 插件。
func NewPingPlugin() *PingPlugin {
	return &PingPlugin{}
}

// Info 返回 Ping 插件元信息。
func (p *PingPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "ping",
		Name:        "Ping",
		Description: "响应Ping命令",
		Version:     "1.0.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "ping", Description: "测试机器人是否在线", Order: 30},
		},
		SubtreeID: pluginpkg.SubtreeID("ping"),
		Tools: []pluginpkg.ToolDef{
			{
				Name:        "ping",
				Description: "测试机器人是否在线，返回Pong",
				Handler:     p.toolPing,
			},
		},
	}
}

// OnInit 初始化 Ping 插件，注册 Pass、Pipeline 和 Subtree。
func (p *PingPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	replyPassID := pluginpkg.PassID("ping", "reply")
	replyPass := &pingReplyPass{}

	if err := ctx.Engine.RegisterPass(replyPassID, replyPass); err != nil {
		return fmt.Errorf("register reply pass: %w", err)
	}

	ctx.Registry.TrackPass("ping", replyPassID)

	pipelineID := pluginpkg.PipelineID("ping", "main")
	pl := conduit.NewPipelineFromIDs(
		pipelineID,
		replyPassID,
	)
	if err := ctx.Engine.RegisterPipeline(pl); err != nil {
		return fmt.Errorf("register pipeline: %w", err)
	}

	ctx.Registry.TrackPipeline("ping", pipelineID)

	subtree := conduit.NewSequence(
		conduit.NewCondition(isPingCommand),
		conduit.NewAction(pipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("ping"), subtree); err != nil {
		return fmt.Errorf("register subtree: %w", err)
	}

	return nil
}

// OnStart Ping 插件无需后台任务。
func (p *PingPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop Ping 插件无需清理资源。
func (p *PingPlugin) OnStop(_ *pluginpkg.PluginContext) error { return nil }

// isPingCommand 判断消息是否为 Ping 命令。
func isPingCommand(ctx *conduit.MessageContext) bool {
	return strings.TrimSpace(ctx.RawMsg) == "/ping"
}

// pingReplyPass 组装 Pong 回复消息
type pingReplyPass struct{}

// Execute 回复一条文本 Pong!：写入当前会话的出站消息，由引擎统一发送。
// 由 pipeline.ping 在 isPingCommand 命中后调用，不读写任何上下文键。
func (pass *pingReplyPass) Execute(ctx *conduit.MessageContext) error {
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		IsGroup: ctx.IsGroup,
		Content: "Pong!",
	})
	return nil
}

// toolPing 是 AI 工具处理器，返回 Pong。
func (p *PingPlugin) toolPing(_ context.Context, _ string) (string, error) {
	return "Pong!", nil
}
