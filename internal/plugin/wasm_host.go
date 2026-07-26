package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	extism "github.com/extism/go-sdk"
	"github.com/zrurf/conduit"
)

// NewDenyAllHostFunctions 返回检查实例所需的完整导入签名，所有调用均默认拒绝。
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
	}
}

// NewStateHostFunctions 创建绑定到可信安装实例身份的状态 Host Functions。
func NewStateHostFunctions(
	authorizer Authorizer,
	store conduit.StateStore,
	principal string,
	installationID string,
	limits *RuntimeLimits,
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
			if err := requireHostAction(authorizer, principal, ActionStateRead); err != nil {
				return HostErr(HostCodeFrom(err), "动作未被授权")
			}
			key := conduit.MakeStoreKey("plugin", installationID, req.Key)
			found, err := store.Exists(ctx, key)
			if err != nil {
				return stateUnavailable(err)
			}
			if !found {
				return HostOK(StateGetData{Found: false, Value: ""})
			}
			value, err := store.Get(ctx, key)
			if err != nil {
				return stateUnavailable(err)
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
			if err := requireHostAction(authorizer, principal, ActionStateWrite); err != nil {
				return HostErr(HostCodeFrom(err), "动作未被授权")
			}
			key := conduit.MakeStoreKey("plugin", installationID, req.Key)
			if err := store.Set(ctx, key, req.Value, ttl); err != nil {
				return stateUnavailable(err)
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
			if err := requireHostAction(authorizer, principal, ActionStateDelete); err != nil {
				return HostErr(HostCodeFrom(err), "动作未被授权")
			}
			key := conduit.MakeStoreKey("plugin", installationID, req.Key)
			if err := store.Delete(ctx, key); err != nil {
				return stateUnavailable(err)
			}
			return HostOK(struct{}{})
		}),
	}
}

func requireHostAction(authorizer Authorizer, principal, action string) error {
	if authorizer == nil {
		return fmt.Errorf("%w: authorizer unavailable", ErrHostInternalError)
	}
	if err := authorizer.Require(principal, action); err != nil {
		if errors.Is(err, ErrPermissionDenied) {
			log.Printf("[wasm_audit] principal=%s action=%s decision=deny", principal, action)
		}
		return err
	}
	return nil
}

func stateUnavailable(err error) HostResponse {
	log.Printf("[wasm] StateStore 调用失败: %v", err)
	return HostErr(ErrCodeStateUnavailable, "状态服务暂不可用")
}

type hostHandler func(context.Context, []byte) HostResponse

func newHostFunction(name string, handler hostHandler) extism.HostFunction {
	return newLimitedHostFunction(name, DefaultLimits.MaxGuestInputJSON, handler)
}

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
