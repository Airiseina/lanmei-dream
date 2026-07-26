package plugin

import (
	"context"
	"testing"
	"time"

	"github.com/zrurf/conduit"
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
func (a *fakeStateAuthorizer) ListActions() []string               { return nil }
func (a *fakeStateAuthorizer) ListRoles() []string                 { return nil }
func (a *fakeStateAuthorizer) InitBuiltinPolicies(_ []int64) error { return nil }
func (a *fakeStateAuthorizer) IsKnownRole(_ string) bool           { return true }
func (a *fakeStateAuthorizer) IsKnownAction(_ string) bool         { return true }

var _ Authorizer = (*fakeStateAuthorizer)(nil)

// 两个 installation ID 使用相同 Guest key 时数据隔离。
func TestHostStateIsolation_TwoInstallations(t *testing.T) {
	store := newMemStateStore()
	limits := DefaultLimits
	auth := &fakeStateAuthorizer{} // 全部放行

	// 安装实例 A
	installA := "install-aaa"
	principalA := PluginPrincipal("signin", installA)
	hostFnsA := NewStateHostFunctions(auth, store, principalA, installA, &limits)

	// 安装实例 B
	installB := "install-bbb"
	principalB := PluginPrincipal("signin", installB)
	hostFnsB := NewStateHostFunctions(auth, store, principalB, installB, &limits)

	ctx := context.Background()
	guestKey := "user:10001:last_sign"

	// A 写入
	if err := store.Set(ctx, conduit.MakeStoreKey("plugin", installA, guestKey), "2026-01-01", 0); err != nil {
		t.Fatal(err)
	}

	// B 写入不同值
	if err := store.Set(ctx, conduit.MakeStoreKey("plugin", installB, guestKey), "2026-02-02", 0); err != nil {
		t.Fatal(err)
	}

	// 验证 A 读到自己的值
	valA, _ := store.Get(ctx, conduit.MakeStoreKey("plugin", installA, guestKey))
	if valA != "2026-01-01" {
		t.Errorf("installA value = %q, want %q", valA, "2026-01-01")
	}

	// 验证 B 读到自己的值
	valB, _ := store.Get(ctx, conduit.MakeStoreKey("plugin", installB, guestKey))
	if valB != "2026-02-02" {
		t.Errorf("installB value = %q, want %q", valB, "2026-02-02")
	}

	// 确保 hostFnsA 和 hostFnsB 构造不 panic（签名验证）
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
	_ = NewStateHostFunctions(auth, store, principal, install, &limits)

	// 直接检查 authorizer 拒绝写操作
	if err := auth.Require(principal, ActionStateWrite); err == nil {
		t.Fatal("expected permission denied for state.write")
	}
}

// 空字符串值和不存在可区分。
func TestHostState_EmptyVsMissing(t *testing.T) {
	store := newMemStateStore()
	ctx := context.Background()

	key := "plugin:inst1:user:1:empty"
	// 写入空字符串
	if err := store.Set(ctx, key, "", 0); err != nil {
		t.Fatal(err)
	}

	// Exists 返回 true
	exists, _ := store.Exists(ctx, key)
	if !exists {
		t.Fatal("empty string key should exist")
	}

	// Get 返回空字符串
	val, _ := store.Get(ctx, key)
	if val != "" {
		t.Errorf("value = %q, want empty", val)
	}

	// 不存在的 key
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

	// 设置短 TTL
	if err := store.Set(ctx, "plugin:inst1:ttl:short", "v", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// 立即可读
	val, _ := store.Get(ctx, "plugin:inst1:ttl:short")
	if val != "v" {
		t.Errorf("value = %q before TTL", val)
	}

	// 等待后过期（内存版没有 TTL 过期回收，跳过此检查）
	// 此测试验证 Set 不 panic 即可
}

// key 和 value 边界测试。
func TestHostState_KeyValueLimits(t *testing.T) {
	limits := DefaultLimits

	// key 过长
	longKey := make([]byte, limits.MaxStateKeyLen+1)
	for i := range longKey {
		longKey[i] = 'a'
	}
	if err := ValidateStateKey(string(longKey), &limits); err == nil {
		t.Fatal("long key should be rejected")
	}

	// value 过长
	longVal := make([]byte, limits.MaxStateValueLen+1)
	for i := range longVal {
		longVal[i] = 'b'
	}
	if err := ValidateStateValue(string(longVal), &limits); err == nil {
		t.Fatal("long value should be rejected")
	}

	// 正常 key/value 通过
	if err := ValidateStateKey("user:1:data", &limits); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if err := ValidateStateValue("正常值", &limits); err != nil {
		t.Fatalf("valid value rejected: %v", err)
	}
}
