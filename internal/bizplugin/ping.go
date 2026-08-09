package bizplugin

import (
	"context"
	"fmt"
	"strings"

	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
)

// ============================================================
// PingPlugin Ping插件
// ============================================================

// PingPlugin 实现最简单的 Ping/Pong 响应功能。
//
// 功能：
//   - 用户发送 /ping，机器人回复 Pong!
//   - 通过 Conduit 行为树子树实现消息路由
//
// 行为树：
//
//	subtree.ping → Sequence(isPingCommand, Action("pipeline.ping.main"))
//
// 管线：
//
//	pipeline.ping.main → [pingReplyPass]
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
			{Name: "ping", Description: "测试机器人是否在线"},
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
	// 注册 Pass
	replyPassID := pluginpkg.PassID("ping", "reply")
	replyPass := &pingReplyPass{}

	if err := ctx.Engine.RegisterPass(replyPassID, replyPass); err != nil {
		return fmt.Errorf("register reply pass: %w", err)
	}

	// 跟踪 Pass，卸载时自动清理
	ctx.Registry.TrackPass("ping", replyPassID)

	// 注册管线
	pipelineID := pluginpkg.PipelineID("ping", "main")
	pl := conduit.NewPipelineFromIDs(
		pipelineID,
		replyPassID,
	)
	if err := ctx.Engine.RegisterPipeline(pl); err != nil {
		return fmt.Errorf("register pipeline: %w", err)
	}

	// 跟踪 Pipeline，卸载时自动清理
	ctx.Registry.TrackPipeline("ping", pipelineID)

	// 注册行为树子树：Ping 命令路由
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

// ============================================================
// 条件判断
// ============================================================

// isPingCommand 判断消息是否为 Ping 命令。
func isPingCommand(ctx *conduit.MessageContext) bool {
	return strings.TrimSpace(ctx.RawMsg) == "/ping"
}

// ============================================================
// Pass 实现
// ============================================================

// pingReplyPass 组装 Pong 回复消息
type pingReplyPass struct{}

func (pass *pingReplyPass) Execute(ctx *conduit.MessageContext) error {
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		IsGroup: ctx.IsGroup,
		Content: "Pong!",
	})
	return nil
}

// ============================================================
// AI 工具处理器
// ============================================================

// toolPing 是 AI 工具处理器，返回 Pong。
func (p *PingPlugin) toolPing(_ context.Context, _ string) (string, error) {
	return "Pong!", nil
}
