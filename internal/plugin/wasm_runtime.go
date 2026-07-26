package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	extism "github.com/extism/go-sdk"
)

// Runtime 封装 Extism 实例创建和所有 Guest Export 调用。
type Runtime struct {
	limits RuntimeLimits
}

// NewRuntime 创建 Extism 运行时。
func NewRuntime(limits *RuntimeLimits) *Runtime {
	if limits == nil {
		limits = &DefaultLimits
	}
	return &Runtime{limits: *limits}
}

func (rt *Runtime) manifest(wasmPath, wasmHash string) extism.Manifest {
	return extism.Manifest{
		Wasm: []extism.Wasm{extism.WasmFile{
			Path: wasmPath,
			Hash: wasmHash,
		}},
		Memory: &extism.ManifestMemory{
			MaxPages:             uint32(rt.limits.MaxMemoryPages),
			MaxHttpResponseBytes: 0,
			MaxVarBytes:          int64(rt.limits.MaxExtismVars),
		},
		AllowedHosts: []string{},
		AllowedPaths: map[string]string{},
		Timeout:      uint64(rt.limits.CallTimeoutSec * 1000),
	}
}

// CreateCheckInstance 创建元数据检查实例。导入签名与正式实例一致，但所有能力默认拒绝。
func (rt *Runtime) CreateCheckInstance(ctx context.Context, wasmPath, wasmHash string) (*extism.Plugin, error) {
	plugin, err := extism.NewPlugin(
		ctx,
		rt.manifest(wasmPath, wasmHash),
		extism.PluginConfig{EnableWasi: true},
		NewDenyAllHostFunctions(),
	)
	if err != nil {
		return nil, fmt.Errorf("创建检查实例: %w", err)
	}
	return plugin, nil
}

// CreateProductionInstance 创建带可信安装身份 Host Functions 的正式实例。
func (rt *Runtime) CreateProductionInstance(
	ctx context.Context,
	wasmPath, wasmHash string,
	hostFunctions []extism.HostFunction,
) (*extism.Plugin, error) {
	plugin, err := extism.NewPlugin(
		ctx,
		rt.manifest(wasmPath, wasmHash),
		extism.PluginConfig{EnableWasi: true},
		hostFunctions,
	)
	if err != nil {
		return nil, fmt.Errorf("创建正式实例: %w", err)
	}
	return plugin, nil
}

// CheckExports 验证所有必需 Guest Export。
func (rt *Runtime) CheckExports(plugin *extism.Plugin) error {
	for _, name := range []string{ExportPluginInfo, ExportInit, ExportHandle} {
		if !plugin.FunctionExists(name) {
			return fmt.Errorf("%w: %s", ErrMissingExport, name)
		}
	}
	return nil
}

// CallExport 串行调用 Guest Export，并限制输入、输出和执行时间。
func (rt *Runtime) CallExport(
	ctx context.Context,
	plugin *extism.Plugin,
	mu *sync.Mutex,
	exportName string,
	input []byte,
) ([]byte, error) {
	if len(input) > rt.limits.MaxGuestInputJSON {
		return nil, fmt.Errorf("Guest 输入超过限制: %d > %d", len(input), rt.limits.MaxGuestInputJSON)
	}

	mu.Lock()
	defer mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(rt.limits.CallTimeoutSec)*time.Second)
	defer cancel()

	exitCode, output, err := plugin.CallWithContext(callCtx, exportName, input)
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: export=%s", ErrCallTimeout, exportName)
		}
		return nil, fmt.Errorf("%w: export=%s: %v", ErrGuestFailed, exportName, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("%w: export=%s exit_code=%d", ErrGuestFailed, exportName, exitCode)
	}
	if len(output) > rt.limits.MaxGuestOutputJSON {
		return nil, fmt.Errorf("%w: export=%s 输出 %d > %d", ErrOutputInvalid, exportName, len(output), rt.limits.MaxGuestOutputJSON)
	}
	return output, nil
}

// CallPluginInfo 读取并校验插件元数据。
func (rt *Runtime) CallPluginInfo(ctx context.Context, plugin *extism.Plugin, mu *sync.Mutex) (*PluginInfoResponse, error) {
	input, err := json.Marshal(PluginInfoRequest{HostABIVersion: ABIVersion})
	if err != nil {
		return nil, fmt.Errorf("编码 plugin_info 请求: %w", err)
	}
	output, err := rt.CallExport(ctx, plugin, mu, ExportPluginInfo, input)
	if err != nil {
		return nil, err
	}

	var info PluginInfoResponse
	if err := UnmarshalGuestInput(output, &info, &rt.limits); err != nil {
		return nil, fmt.Errorf("%w: 解码 plugin_info: %w", ErrInvalidMetadata, err)
	}
	if err := info.Validate(); err != nil {
		return nil, err
	}
	return &info, nil
}

// CallInit 调用一次性初始化导出。
func (rt *Runtime) CallInit(ctx context.Context, plugin *extism.Plugin, mu *sync.Mutex, req InitRequest) error {
	input, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("编码 init 请求: %w", err)
	}
	output, err := rt.CallExport(ctx, plugin, mu, ExportInit, input)
	if err != nil {
		return err
	}

	var resp InitResponse
	if err := UnmarshalGuestInput(output, &resp, &rt.limits); err != nil {
		return fmt.Errorf("解码 init 响应: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("%w: init: %s", ErrGuestFailed, resp.Error)
	}
	return nil
}

// CallHandle 调用统一命令事件入口。
func (rt *Runtime) CallHandle(ctx context.Context, plugin *extism.Plugin, mu *sync.Mutex, req HandleRequest) (*HandleResponse, error) {
	input, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("编码 handle 请求: %w", err)
	}
	output, err := rt.CallExport(ctx, plugin, mu, ExportHandle, input)
	if err != nil {
		return nil, err
	}

	var resp HandleResponse
	if err := UnmarshalGuestInput(output, &resp, &rt.limits); err != nil {
		return nil, fmt.Errorf("%w: 解码 handle 响应: %w", ErrOutputInvalid, err)
	}
	if err := resp.Validate(&rt.limits); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CallStart 调用可选启动导出。
func (rt *Runtime) CallStart(ctx context.Context, plugin *extism.Plugin, mu *sync.Mutex) error {
	if !plugin.FunctionExists(ExportStart) {
		return nil
	}
	input, err := json.Marshal(StartRequest{StartedAt: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return fmt.Errorf("编码 start 请求: %w", err)
	}
	output, err := rt.CallExport(ctx, plugin, mu, ExportStart, input)
	if err != nil {
		return err
	}
	var resp GenericOKResponse
	if err := UnmarshalGuestInput(output, &resp, &rt.limits); err != nil {
		return fmt.Errorf("解码 start 响应: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("%w: start: %s", ErrGuestFailed, resp.Error)
	}
	return nil
}

// CallStop 调用可选停止导出。失败只记录，不阻止资源回收。
func (rt *Runtime) CallStop(ctx context.Context, plugin *extism.Plugin, mu *sync.Mutex, reason StopReason) {
	if !plugin.FunctionExists(ExportStop) {
		return
	}
	input, err := json.Marshal(StopRequest{Reason: string(reason)})
	if err != nil {
		log.Printf("[wasm] 编码 stop 请求失败: %v", err)
		return
	}
	output, err := rt.CallExport(ctx, plugin, mu, ExportStop, input)
	if err != nil {
		log.Printf("[wasm] lanmei_stop 失败 reason=%s: %v", reason, err)
		return
	}
	var resp GenericOKResponse
	if err := UnmarshalGuestInput(output, &resp, &rt.limits); err != nil {
		log.Printf("[wasm] 解码 stop 响应失败: %v", err)
		return
	}
	if !resp.OK {
		log.Printf("[wasm] lanmei_stop 返回失败: %s", resp.Error)
	}
}

// Close 串行关闭插件实例。
func (rt *Runtime) Close(ctx context.Context, plugin *extism.Plugin, mu *sync.Mutex) error {
	mu.Lock()
	defer mu.Unlock()
	if err := plugin.Close(ctx); err != nil {
		return fmt.Errorf("关闭 Extism 实例: %w", err)
	}
	return nil
}
