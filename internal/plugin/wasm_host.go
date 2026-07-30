package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	extism "github.com/extism/go-sdk"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// NewDenyAllHostFunctions 返回一组"全拒绝"的 Host Function 定义。
//
// 设计用途：
// 当创建 WASM 插件实例时，必须提供与插件导入签名匹配的 Host Function 集合。
// 在权限检查或初始化阶段，可以先使用全拒绝版本创建实例，
// 确保在权限未确认前，插件的任何宿主调用都会被安全拒绝。
//
// 安全意义：
//   - 默认拒绝（deny-by-default）是安全沙箱的基本原则
//   - 防止未授权的插件在初始化阶段就执行敏感操作
//   - 所有函数返回 ErrCodePermissionDenied 错误，明确告知调用未被授权
func NewDenyAllHostFunctions() []extism.HostFunction {
	return []extism.HostFunction{
		newHostFunction("state_get", func(context.Context, []byte) HostResponse {
			return HostErr(ErrCodePermissionDenied, "动作未被授权")
		}),
		newHostFunction("state_set", func(context.Context, []byte) HostResponse {
			return HostErr(ErrCodePermissionDenied, "动作未被授权")
		}),
		newHostFunction("state_delete", func(context.Context, []byte) HostResponse {
			return HostErr(ErrCodePermissionDenied, "动作未被授权")
		}),
		newHostFunction("compare_and_swap", func(context.Context, []byte) HostResponse {
			return HostErr(ErrCodePermissionDenied, "动作未被授权")
		}),
		newHostFunction("incr_by", func(context.Context, []byte) HostResponse {
			return HostErr(ErrCodePermissionDenied, "动作未被授权")
		}),
		newHostFunction("set_if_not_exists", func(context.Context, []byte) HostResponse {
			return HostErr(ErrCodePermissionDenied, "动作未被授权")
		}),
	}
}

// NewStateHostFunctions 创建绑定到可信安装实例身份的状态存储 Host Functions。
//
// Host Function 设计原理：
// WASM 插件运行在沙箱中，无法直接访问宿主的文件系统、网络或数据库。
// 所有对外部资源的访问都必须通过 Host Function 实现——插件调用导入的函数名，
// 宿主在对应的 Go 函数中执行实际操作并返回结果。
//
// 安全检查链（每个 Host Function 的执行流程）：
//  1. 输入验证：反序列化 JSON 输入，校验 key/value 合法性
//  2. 权限检查：通过 requireHostAction 调用 Authorizer，验证 principal 是否拥有对应 action 的权限
//  3. 命名空间隔离：使用 conduit.MakeStoreKey("plugin", installationID, key) 构造隔离 key，
//     确保不同插件的存储互不可见
//  4. 执行操作：调用 StateStore 的对应方法
//  5. 返回结果：通过 HostOK/HostErr 编码为标准化的响应格式
//
// 参数：
//   - authorizer: 权限检查器，验证 principal 是否有权限执行对应 action
//   - store: 状态存储后端（conduit.StateStore）
//   - principal: 操作主体标识（格式 "plugin:<pluginID>:<installationID>"）
//   - installationID: 安装实例 ID，用于构造隔离的存储 key
//   - limits: 运行时限制（输入大小、key 长度等），nil 时使用默认值
//   - logger: 日志记录器
func NewStateHostFunctions(
	authorizer Authorizer,
	store conduit.StateStore,
	principal string,
	installationID string,
	limits *RuntimeLimits,
	logger *zap.Logger,
) []extism.HostFunction {
	if limits == nil {
		limits = &DefaultLimits
	}

	return []extism.HostFunction{
		newLimitedHostFunction("state_get", limits.MaxGuestInputJSON, func(ctx context.Context, input []byte) HostResponse {
			var req StateGetRequest
			if err := json.Unmarshal(input, &req); err != nil {
				return HostErr(ErrCodeInvalidRequest, "请求 JSON 无效")
			}
			if err := ValidateStateKey(req.Key, limits); err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			if err := requireHostAction(authorizer, principal, ActionStateRead, logger); err != nil {
				return HostErr(HostCodeFrom(err), "动作未被授权")
			}
			key := conduit.MakeStoreKey("plugin", installationID, req.Key)
			found, err := store.Exists(ctx, key)
			if err != nil {
				return stateUnavailable(err, logger)
			}
			if !found {
				return HostOK(StateGetData{Found: false, Value: ""})
			}
			value, err := store.Get(ctx, key)
			if err != nil {
				return stateUnavailable(err, logger)
			}
			return HostOK(StateGetData{Found: true, Value: value})
		}),
		newLimitedHostFunction("state_set", limits.MaxGuestInputJSON, func(ctx context.Context, input []byte) HostResponse {
			var req StateSetRequest
			if err := json.Unmarshal(input, &req); err != nil {
				return HostErr(ErrCodeInvalidRequest, "请求 JSON 无效")
			}
			if err := ValidateStateKey(req.Key, limits); err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			if err := ValidateStateValue(req.Value, limits); err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			ttl, err := ValidateTTL(req.TTLMs, limits)
			if err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			if err := requireHostAction(authorizer, principal, ActionStateWrite, logger); err != nil {
				return HostErr(HostCodeFrom(err), "动作未被授权")
			}
			key := conduit.MakeStoreKey("plugin", installationID, req.Key)
			if err := store.Set(ctx, key, req.Value, ttl); err != nil {
				return stateUnavailable(err, logger)
			}
			return HostOK(struct{}{})
		}),
		newLimitedHostFunction("state_delete", limits.MaxGuestInputJSON, func(ctx context.Context, input []byte) HostResponse {
			var req StateDeleteRequest
			if err := json.Unmarshal(input, &req); err != nil {
				return HostErr(ErrCodeInvalidRequest, "请求 JSON 无效")
			}
			if err := ValidateStateKey(req.Key, limits); err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			if err := requireHostAction(authorizer, principal, ActionStateDelete, logger); err != nil {
				return HostErr(HostCodeFrom(err), "动作未被授权")
			}
			key := conduit.MakeStoreKey("plugin", installationID, req.Key)
			if err := store.Delete(ctx, key); err != nil {
				return stateUnavailable(err, logger)
			}
			return HostOK(struct{}{})
		}),
		newLimitedHostFunction("compare_and_swap", limits.MaxGuestInputJSON, func(ctx context.Context, input []byte) HostResponse {
			var req CompareAndSwapRequest
			if err := json.Unmarshal(input, &req); err != nil {
				return HostErr(ErrCodeInvalidRequest, "请求 JSON 无效")
			}
			if err := ValidateStateKey(req.Key, limits); err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			if err := ValidateStateValue(req.NewValue, limits); err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			ttl, err := ValidateTTL(req.TTLMs, limits)
			if err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			if err := requireHostAction(authorizer, principal, ActionStateWrite, logger); err != nil {
				return HostErr(HostCodeFrom(err), "动作未被授权")
			}
			key := conduit.MakeStoreKey("plugin", installationID, req.Key)
			swapped, err := store.CompareAndSwap(ctx, key, req.OldValue, req.NewValue, ttl)
			if err != nil {
				return stateUnavailable(err, logger)
			}
			return HostOK(CompareAndSwapData{Swapped: swapped})
		}),
		newLimitedHostFunction("incr_by", limits.MaxGuestInputJSON, func(ctx context.Context, input []byte) HostResponse {
			var req IncrByRequest
			if err := json.Unmarshal(input, &req); err != nil {
				return HostErr(ErrCodeInvalidRequest, "请求 JSON 无效")
			}
			if err := ValidateStateKey(req.Key, limits); err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			if err := requireHostAction(authorizer, principal, ActionStateWrite, logger); err != nil {
				return HostErr(HostCodeFrom(err), "动作未被授权")
			}
			key := conduit.MakeStoreKey("plugin", installationID, req.Key)
			value, err := store.IncrBy(ctx, key, req.Delta)
			if err != nil {
				return stateUnavailable(err, logger)
			}
			return HostOK(IncrByData{Value: value})
		}),
		newLimitedHostFunction("set_if_not_exists", limits.MaxGuestInputJSON, func(ctx context.Context, input []byte) HostResponse {
			var req SetIfNotExistsRequest
			if err := json.Unmarshal(input, &req); err != nil {
				return HostErr(ErrCodeInvalidRequest, "请求 JSON 无效")
			}
			if err := ValidateStateKey(req.Key, limits); err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			if err := ValidateStateValue(req.Value, limits); err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			ttl, err := ValidateTTL(req.TTLMs, limits)
			if err != nil {
				return HostErr(HostCodeFrom(err), err.Error())
			}
			if err := requireHostAction(authorizer, principal, ActionStateWrite, logger); err != nil {
				return HostErr(HostCodeFrom(err), "动作未被授权")
			}
			key := conduit.MakeStoreKey("plugin", installationID, req.Key)
			set, err := store.SetIfNotExists(ctx, key, req.Value, ttl)
			if err != nil {
				return stateUnavailable(err, logger)
			}
			return HostOK(SetIfNotExistsData{Set: set})
		}),
	}
}

// requireHostAction 在 Host Function 中执行权限检查。
//
// 这是所有 Host Function 的统一权限门控：
//   - 如果 authorizer 为 nil，返回内部错误（说明系统配置有误）
//   - 调用 authorizer.Require(principal, action) 检查权限
//   - 权限拒绝时记录 Warn 级别审计日志，并返回 ErrPermissionDenied
//   - 权限通过时返回 nil
//
// 参数：
//   - authorizer: 权限检查器
//   - principal: 操作主体标识
//   - action: 请求的操作（如 ActionStateRead、ActionStateWrite）
//   - logger: 用于记录权限拒绝事件
func requireHostAction(authorizer Authorizer, principal, action string, logger *zap.Logger) error {
	if authorizer == nil {
		return fmt.Errorf("%w: authorizer unavailable", ErrHostInternalError)
	}
	if err := authorizer.Require(principal, action); err != nil {
		if errors.Is(err, ErrPermissionDenied) {
			logger.Warn("[wasm_audit] permission denied", zap.String("principal", principal), zap.String("action", action))
		}
		return err
	}
	return nil
}

// stateUnavailable 处理 StateStore 调用失败的情况。
// 记录 Error 级别日志并返回 ErrCodeStateUnavailable 错误，
// 告知插件状态服务暂不可用（而非权限拒绝）。
func stateUnavailable(err error, logger *zap.Logger) HostResponse {
	logger.Error("[wasm] StateStore 调用失败", zap.Error(err))
	return HostErr(ErrCodeStateUnavailable, "状态服务暂不可用")
}

// hostHandler 是 Host Function 的业务逻辑签名。
// 接收上下文和 WASM 侧传入的 JSON 字节，返回标准化的 HostResponse。
type hostHandler func(context.Context, []byte) HostResponse

// newHostFunction 创建一个使用默认输入大小限制的 Host Function。
func newHostFunction(name string, handler hostHandler) extism.HostFunction {
	return newLimitedHostFunction(name, DefaultLimits.MaxGuestInputJSON, handler)
}

// newLimitedHostFunction 创建一个带输入大小限制的 Host Function。
//
// 这是所有 Host Function 的底层构造器，封装了 Extism SDK 的调用约定：
//  1. 从 WASM 栈中读取输入指针（stack[0]）
//  2. 读取输入长度并检查是否超过 maxInput 限制（防止恶意超大输入）
//  3. 读取输入字节并调用 handler 处理
//  4. 将 handler 返回的 HostResponse 序列化为 JSON
//  5. 将 JSON 写回 WASM 内存并通过栈返回指针
//
// 安全措施：
//   - 输入大小限制（maxInput）防止单个请求耗尽宿主内存
//   - 所有 handler 返回值通过 json.Marshal 序列化，确保格式一致
//   - 设置 HostNamespace 命名空间，避免与其他可能的 WASM 导入冲突
func newLimitedHostFunction(name string, maxInput int, handler hostHandler) extism.HostFunction {
	fn := extism.NewHostFunctionWithStack(
		name,
		func(ctx context.Context, plugin *extism.CurrentPlugin, stack []uint64) {
			length, err := plugin.Length(stack[0])
			if err != nil {
				panic(fmt.Errorf("读取 Host Function 输入长度: %w", err))
			}

			var response HostResponse
			if length > uint64(maxInput) {
				response = HostErr(ErrCodeInvalidRequest, "请求超过大小限制")
			} else {
				input, err := plugin.ReadBytes(stack[0])
				if err != nil {
					panic(fmt.Errorf("读取 Host Function 输入: %w", err))
				}
				response = handler(ctx, input)
			}

			encoded, err := json.Marshal(response)
			if err != nil {
				panic(fmt.Errorf("编码 Host Function 响应: %w", err))
			}
			stack[0], err = plugin.WriteBytes(encoded)
			if err != nil {
				panic(fmt.Errorf("写入 Host Function 响应: %w", err))
			}
		},
		[]extism.ValueType{extism.ValueTypePTR},
		[]extism.ValueType{extism.ValueTypePTR},
	)
	fn.SetNamespace(HostNamespace)
	return fn
}
