package plugin

import (
	"context"
	"fmt"
	"sync"

	extism "github.com/extism/go-sdk"
	"github.com/zrurf/conduit"
)

// WasmRuntime 是 WasmPlugin 所需的 Runtime 能力接口。
// 测试可用 fake 替代真实 *Runtime。
type WasmRuntime interface {
	CallHandle(ctx context.Context, instance *extism.Plugin, mu *sync.Mutex, req HandleRequest) (*HandleResponse, error)
	CallStart(ctx context.Context, instance *extism.Plugin, mu *sync.Mutex) error
	CallStop(ctx context.Context, instance *extism.Plugin, mu *sync.Mutex, reason StopReason)
	Close(ctx context.Context, instance *extism.Plugin, mu *sync.Mutex) error
}

// WasmPlugin 将一个正式 Extism 安装实例适配为宿主 Plugin。
type WasmPlugin struct {
	metadata       PluginInfoResponse
	installationID string
	principal      string
	runtime        WasmRuntime
	instance       *extism.Plugin
	authorizer     Authorizer
	callMu         sync.Mutex
	closeOnce      sync.Once
	closeErr       error
	stopReason     StopReason
}

var _ Plugin = (*WasmPlugin)(nil)

// NewWasmPlugin 创建已实例化的 Wasm 插件适配器。
func NewWasmPlugin(
	metadata PluginInfoResponse,
	installationID string,
	runtime WasmRuntime,
	instance *extism.Plugin,
	authorizer Authorizer,
) *WasmPlugin {
	return &WasmPlugin{
		metadata:       metadata,
		installationID: installationID,
		principal:      PluginPrincipal(metadata.ID, installationID),
		runtime:        runtime,
		instance:       instance,
		authorizer:     authorizer,
		stopReason:     StopReasonShutdown,
	}
}

// InstallationID 返回宿主可信安装实例 ID。
func (p *WasmPlugin) InstallationID() string { return p.installationID }

// Principal 返回宿主可信 Casbin 主体。
func (p *WasmPlugin) Principal() string { return p.principal }

// SetStopReason 设置下一次停止通知原因。
func (p *WasmPlugin) SetStopReason(reason StopReason) { p.stopReason = reason }

// Info 返回宿主生成的路由元数据。
func (p *WasmPlugin) Info() PluginInfo {
	commands := make([]CommandDef, 0, len(p.metadata.Commands))
	for _, command := range p.metadata.Commands {
		commands = append(commands, CommandDef{Name: command.Name, Description: command.Description})
	}
	return PluginInfo{
		ID:          p.metadata.ID,
		Name:        p.metadata.Name,
		Description: p.metadata.Description,
		Version:     p.metadata.Version,
		Commands:    commands,
		SubtreeID:   SubtreeID(p.metadata.ID),
	}
}

// OnInit 注册宿主控制的共享 Pass、Pipeline 和行为树子树。
func (p *WasmPlugin) OnInit(ctx *PluginContext) error {
	passID := PassID(p.metadata.ID, "command")
	pipelineID := PipelineID(p.metadata.ID, "command")

	if err := ctx.Engine.RegisterPass(passID, &WasmCommandPass{plugin: p}); err != nil {
		return fmt.Errorf("注册 Wasm command Pass: %w", err)
	}
	ctx.Registry.TrackPass(p.metadata.ID, passID)

	if err := ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(pipelineID, passID)); err != nil {
		return fmt.Errorf("注册 Wasm command Pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline(p.metadata.ID, pipelineID)

	subtree := conduit.NewSequence(
		conduit.NewCondition(func(message *conduit.MessageContext) bool {
			pluginID, pluginOK := message.Extra["plugin_id"].(string)
			installationID, installationOK := message.Extra["installation_id"].(string)
			skip, skipOK := message.Extra[SkipCommandKey].(bool)
			return pluginOK && installationOK && skipOK && skip &&
				pluginID == p.metadata.ID && installationID == p.installationID
		}),
		conduit.NewAction(pipelineID),
	)
	if err := ctx.Engine.RegisterSubtree(SubtreeID(p.metadata.ID), subtree); err != nil {
		return fmt.Errorf("注册 Wasm command Subtree: %w", err)
	}
	return nil
}

// OnStart 发送一次性启动通知。
func (p *WasmPlugin) OnStart(ctx *PluginContext) error {
	return p.runtime.CallStart(ctx.Ctx, p.instance, &p.callMu)
}

// OnStop 停止 Guest 并关闭实例。关闭保持幂等。
func (p *WasmPlugin) OnStop(ctx *PluginContext) error {
	p.closeOnce.Do(func() {
		p.runtime.CallStop(ctx.Ctx, p.instance, &p.callMu, p.stopReason)
		p.closeErr = p.runtime.Close(ctx.Ctx, p.instance, &p.callMu)
	})
	return p.closeErr
}

// Close 在 Registry 注册前失败时回收实例。
func (p *WasmPlugin) Close(ctx context.Context) error {
	p.closeOnce.Do(func() {
		p.closeErr = p.runtime.Close(ctx, p.instance, &p.callMu)
	})
	return p.closeErr
}
