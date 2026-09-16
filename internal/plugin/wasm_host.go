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

// NewDenyAllHostFunctions 返回一组"全拒绝"的 Host Function。
// 用于在权限确认前创建实例：导入签名与正式实例一致，任何宿主调用都返回 ErrCodePermissionDenied，
// 遵循沙箱的默认拒绝（deny-by-default）原则，避免未授权插件在初始化阶段执行敏感操作。
//
// 返回：state_get、state_set、state_delete、compare_and_swap、incr_by、set_if_not_exists
// 六个统一返回 permission_denied 的 Host Function。
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

// NewStateHostFunctions 创建绑定到可信安装实例身份的状态存储 Host Function。
// 每次调用依次做输入校验、Authorizer 权限检查，再用 conduit.MakeStoreKey 按 installationID
// 隔离存储 key，保证不同插件以及同一插件的不同安装实例状态互不可见。
//
// 参数：
//   - authorizer：权限判定器；为 nil 时所有调用返回 internal_error（视为宿主配置错误）
//   - store：状态存储；物理 key 由宿主按 installationID 加前缀生成
//   - principal：宿主可信的插件安装实例主体，须由 PluginPrincipal 构造
//   - installationID：安装实例 ID，用于状态隔离
//   - limits：运行时限制；为 nil 时使用 DefaultLimits
//   - logger：日志器
//
// 返回：state_get、state_set、state_delete、compare_and_swap、incr_by、set_if_not_exists
// 六个 Host Function；权限检查依次使用 state.read、state.write、state.delete 动作，
// 原子操作统一归入 state.write。
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

// requireHostAction 是所有 Host Function 的统一权限门控。
// authorizer 为 nil 视为系统配置错误；权限拒绝时记 Warn 审计日志并原样返回 ErrPermissionDenied。
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

// stateUnavailable 记录 Error 日志并返回 ErrCodeStateUnavailable，
// 表示状态服务暂不可用，以便与权限拒绝区分。
func stateUnavailable(err error, logger *zap.Logger) HostResponse {
	logger.Error("[wasm] StateStore 调用失败", zap.Error(err))
	return HostErr(ErrCodeStateUnavailable, "状态服务暂不可用")
}

// hostHandler 是 Host Function 的业务逻辑签名：接收上下文与 Guest 传入的 JSON 字节，返回 HostResponse。
type hostHandler func(context.Context, []byte) HostResponse

// newHostFunction 创建一个使用默认输入大小限制的 Host Function。
func newHostFunction(name string, handler hostHandler) extism.HostFunction {
	return newLimitedHostFunction(name, DefaultLimits.MaxGuestInputJSON, handler)
}

// newLimitedHostFunction 构造带输入大小限制的 Host Function，封装 Extism SDK 的调用约定：
// 从栈读取输入指针与长度，超过 maxInput 直接拒绝（避免恶意超大输入耗尽宿主内存），
// 处理结果统一 json.Marshal 后写回 Guest 内存；命名空间固定为 HostNamespace，避免与其他 Wasm 导入冲突。
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
