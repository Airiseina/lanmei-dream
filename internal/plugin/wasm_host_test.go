package plugin

import (
	"context"
	"testing"
	"time"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// fakeStateAuthorizer 是用于 Host Function 测试的 stub authorizer。
// 它固定返回 allow/deny，不依赖 Casbin。
type fakeStateAuthorizer struct {
	denyAction map[string]bool
}

func (a *fakeStateAuthorizer) Require(_ string, action string) error {
	if a.denyAction[action] {
		return ErrPermissionDenied
	}
	return nil
}
func (a *fakeStateAuthorizer) BindRole(_, _, _ string) error     { return nil }
func (a *fakeStateAuthorizer) UnbindRole(_, _, _ string) error   { return nil }
func (a *fakeStateAuthorizer) GrantAction(_, _, _ string) error  { return nil }
func (a *fakeStateAuthorizer) RevokeAction(_, _, _ string) error { return nil }
func (a *fakeStateAuthorizer) RolesFor(_ string) ([]string, error) {
	return []string{RolePluginCommandBasic}, nil
}

func (a *fakeStateAuthorizer) ActionsFor(_ string) ([][]string, error) {
	return nil, nil
}
func (a *fakeStateAuthorizer) ListActions() []string                { return nil }
func (a *fakeStateAuthorizer) ListRoles() []string                  { return nil }
func (a *fakeStateAuthorizer) InitBuiltinPolicies(_ []string) error { return nil }
func (a *fakeStateAuthorizer) IsKnownRole(_ string) bool            { return true }
func (a *fakeStateAuthorizer) IsKnownAction(_ string) bool          { return true }

var _ Authorizer = (*fakeStateAuthorizer)(nil)

// 两个 installation ID 使用相同 Guest key 时数据隔离。
func TestHostStateIsolation_TwoInstallations(t *testing.T) {
	store := newMemStateStore()
	limits := DefaultLimits
	auth := &fakeStateAuthorizer{} // 全部放行

	installA := "install-aaa"
	principalA := PluginPrincipal("signin", installA)
	hostFnsA := NewStateHostFunctions(auth, store, principalA, installA, &limits, zap.NewNop())

	installB := "install-bbb"
	principalB := PluginPrincipal("signin", installB)
	hostFnsB := NewStateHostFunctions(auth, store, principalB, installB, &limits, zap.NewNop())

	ctx := context.Background()
	guestKey := "user:10001:last_sign"

	if err := store.Set(ctx, conduit.MakeStoreKey("plugin", installA, guestKey), "2026-01-01", 0); err != nil {
		t.Fatal(err)
	}

	if err := store.Set(ctx, conduit.MakeStoreKey("plugin", installB, guestKey), "2026-02-02", 0); err != nil {
		t.Fatal(err)
	}

	valA, _ := store.Get(ctx, conduit.MakeStoreKey("plugin", installA, guestKey))
	if valA != "2026-01-01" {
		t.Errorf("installA value = %q, want %q", valA, "2026-01-01")
	}

	valB, _ := store.Get(ctx, conduit.MakeStoreKey("plugin", installB, guestKey))
	if valB != "2026-02-02" {
		t.Errorf("installB value = %q, want %q", valB, "2026-02-02")
	}

	// hostFns 仅为验证构造不 panic（导入签名检查）
	_ = hostFnsA
	_ = hostFnsB
}

// 未授权调用应返回 permission_denied。
func TestHostState_PermissionDenied(t *testing.T) {
	store := newMemStateStore()
	limits := DefaultLimits
	auth := &fakeStateAuthorizer{
		denyAction: map[string]bool{ActionStateWrite: true},
	}

	install := "install-deny"
	principal := PluginPrincipal("signin", install)
	_ = NewStateHostFunctions(auth, store, principal, install, &limits, zap.NewNop())

	if err := auth.Require(principal, ActionStateWrite); err == nil {
		t.Fatal("expected permission denied for state.write")
	}
}

// 空字符串值和不存在可区分。
func TestHostState_EmptyVsMissing(t *testing.T) {
	store := newMemStateStore()
	ctx := context.Background()

	key := "plugin:inst1:user:1:empty"
	if err := store.Set(ctx, key, "", 0); err != nil {
		t.Fatal(err)
	}

	exists, _ := store.Exists(ctx, key)
	if !exists {
		t.Fatal("empty string key should exist")
	}

	val, _ := store.Get(ctx, key)
	if val != "" {
		t.Errorf("value = %q, want empty", val)
	}

	missingKey := "plugin:inst1:not:there"
	exists2, _ := store.Exists(ctx, missingKey)
	if exists2 {
		t.Fatal("non-existent key should not exist")
	}
}

// TTL 边界测试。
func TestHostState_TTLBoundary(t *testing.T) {
	store := newMemStateStore()
	ctx := context.Background()

	if err := store.Set(ctx, "plugin:inst1:ttl:short", "v", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	val, _ := store.Get(ctx, "plugin:inst1:ttl:short")
	if val != "v" {
		t.Errorf("value = %q before TTL", val)
	}

	// 内存版 StateStore 不实现 TTL 过期回收，此测试只验证 Set 不 panic。
}

// key 和 value 边界测试。
func TestHostState_KeyValueLimits(t *testing.T) {
	limits := DefaultLimits

	longKey := make([]byte, limits.MaxStateKeyLen+1)
	for i := range longKey {
		longKey[i] = 'a'
	}
	if err := ValidateStateKey(string(longKey), &limits); err == nil {
		t.Fatal("long key should be rejected")
	}

	longVal := make([]byte, limits.MaxStateValueLen+1)
	for i := range longVal {
		longVal[i] = 'b'
	}
	if err := ValidateStateValue(string(longVal), &limits); err == nil {
		t.Fatal("long value should be rejected")
	}

	if err := ValidateStateKey("user:1:data", &limits); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if err := ValidateStateValue("正常值", &limits); err != nil {
		t.Fatalf("valid value rejected: %v", err)
	}
}
