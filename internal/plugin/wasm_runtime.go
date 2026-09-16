package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	extism "github.com/extism/go-sdk"
	"go.uber.org/zap"
)

// Runtime 封装 Extism 实例创建和所有 Guest Export 调用，并统一施加 RuntimeLimits 限制。
// 同一实例的所有调用需经调用方持有的互斥锁串行化。
type Runtime struct {
	limits RuntimeLimits
	logger *zap.Logger
}

// NewRuntime 创建 Extism 运行时。
//
// 参数：
//   - limits：运行时限制；为 nil 时使用 DefaultLimits，非 nil 时按值复制、不做零值补齐
//   - logger：日志器（CallStop 失败时记录日志）
//
// 返回：运行时实例。
func NewRuntime(limits *RuntimeLimits, logger *zap.Logger) *Runtime {
	if limits == nil {
		limits = &DefaultLimits
	}
	return &Runtime{limits: *limits, logger: logger}
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

// CreateCheckInstance 创建元数据检查实例：导入签名与正式实例一致，但所有 Host Function 默认拒绝，
// 供安装阶段调用 plugin_info 校验元数据，避免未授权插件在检查期执行敏感操作。
//
// 参数：
//   - ctx：创建上下文
//   - wasmPath：Wasm 文件路径
//   - wasmHash：Wasm 文件的 SHA-256 摘要，写入 Extism manifest
//
// 返回：实例句柄；创建失败返回错误。调用方负责关闭实例。
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

// CreateProductionInstance 创建正式实例，Host Function 由调用方按可信安装实例身份绑定。
//
// 参数：
//   - ctx：创建上下文
//   - wasmPath：Wasm 文件路径
//   - wasmHash：Wasm 文件的 SHA-256 摘要，写入 Extism manifest
//   - hostFunctions：绑定可信主体与安装实例的 Host Function 集合（由 NewStateHostFunctions 生成）
//
// 返回：实例句柄；创建失败返回错误。调用方负责关闭实例。
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

// CheckExports 验证三个必需 Guest 导出（lanmei_plugin_info、lanmei_init、lanmei_handle）是否存在。
//
// 参数：
//   - plugin：Extism 实例
//
// 返回：缺少任一导出时返回包装 ErrMissingExport 的错误。
func (rt *Runtime) CheckExports(plugin *extism.Plugin) error {
	for _, name := range []string{ExportPluginInfo, ExportInit, ExportHandle} {
		if !plugin.FunctionExists(name) {
			return fmt.Errorf("%w: %s", ErrMissingExport, name)
		}
	}
	return nil
}

// CallExport 串行调用 Guest Export，并限制输入、输出和执行时间。
//
// 参数：
//   - ctx：调用上下文，叠加 limits.CallTimeoutSec 形成单次调用超时
//   - plugin：Extism 实例
//   - mu：与实例绑定的互斥锁，保证同一实例的导出调用串行执行
//   - exportName：导出函数名
//   - input：JSON 输入字节，超过 MaxGuestInputJSON 直接拒绝
//
// 返回：Guest 输出字节；超时返回包装 ErrCallTimeout 的错误，trap/非零退出码返回包装 ErrGuestFailed 的错误，
// 输出超过 MaxGuestOutputJSON 返回包装 ErrOutputInvalid 的错误。
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

// CallPluginInfo 调用 lanmei_plugin_info 读取并校验插件元数据；该导出应可重复调用且无副作用。
//
// 参数：
//   - ctx：调用上下文
//   - plugin：Extism 实例
//   - mu：实例调用互斥锁
//
// 返回：通过 Validate 的元数据；调用失败、响应解码失败或元数据无效时返回错误。
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

// CallInit 调用 lanmei_init 完成一次性初始化。
//
// 参数：
//   - ctx：调用上下文
//   - plugin：Extism 实例
//   - mu：实例调用互斥锁
//   - req：初始化请求，含安装实例身份、配置与宿主最终授予的角色/动作
//
// 返回：Guest 返回 ok=false、调用失败或响应解码失败时返回错误。
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

// CallHandle 调用 lanmei_handle 处理命令或工具调用事件。
//
// 参数：
//   - ctx：调用上下文
//   - plugin：Extism 实例
//   - mu：实例调用互斥锁
//   - req：事件请求（command 或 tool_call）
//
// 返回：经 Validate 校验的响应；调用失败、解码失败或输出违反 ABI 约束时返回错误。
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

// CallStart 调用可选的 lanmei_start；导出不存在时直接返回 nil，表示插件未使用启动钩子。
//
// 参数：
//   - ctx：调用上下文
//   - plugin：Extism 实例
//   - mu：实例调用互斥锁
//
// 返回：Guest 返回 ok=false、调用失败或响应解码失败时返回错误。
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

// CallStop 调用可选的 lanmei_stop 通知停止原因；导出不存在时直接返回。
// 失败只记录日志，不阻止资源回收，因此无返回值。
//
// 参数：
//   - ctx：调用上下文
//   - plugin：Extism 实例
//   - mu：实例调用互斥锁
//   - reason：停止原因，透传给 Guest
func (rt *Runtime) CallStop(ctx context.Context, plugin *extism.Plugin, mu *sync.Mutex, reason StopReason) {
	if !plugin.FunctionExists(ExportStop) {
		return
	}
	input, err := json.Marshal(StopRequest{Reason: string(reason)})
	if err != nil {
		rt.logger.Error("[wasm] 编码 stop 请求失败", zap.Error(err))
		return
	}
	output, err := rt.CallExport(ctx, plugin, mu, ExportStop, input)
	if err != nil {
		rt.logger.Error("[wasm] lanmei_stop 失败", zap.String("reason", string(reason)), zap.Error(err))
		return
	}
	var resp GenericOKResponse
	if err := UnmarshalGuestInput(output, &resp, &rt.limits); err != nil {
		rt.logger.Error("[wasm] 解码 stop 响应失败", zap.Error(err))
		return
	}
	if !resp.OK {
		rt.logger.Error("[wasm] lanmei_stop 返回失败", zap.String("error", resp.Error))
	}
}

// Close 串行关闭插件实例并回收资源。
//
// 参数：
//   - ctx：关闭上下文
//   - plugin：Extism 实例
//   - mu：实例调用互斥锁，与调用共用以避免并发关闭
//
// 返回：Extism 关闭失败时返回错误。
func (rt *Runtime) Close(ctx context.Context, plugin *extism.Plugin, mu *sync.Mutex) error {
	mu.Lock()
	defer mu.Unlock()
	if err := plugin.Close(ctx); err != nil {
		return fmt.Errorf("关闭 Extism 实例: %w", err)
	}
	return nil
}
