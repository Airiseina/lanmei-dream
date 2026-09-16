package plugin

import (
	"testing"
)

// fakeAuthz 是用于纯单元测试的 stub Authorizer，不依赖 Casbin。
type fakeAuthz struct {
	roles map[string][]string        // principal → roles
	allow map[string]map[string]bool // role → set(action)
}

func newFakeAuthz() *fakeAuthz {
	return &fakeAuthz{
		roles: make(map[string][]string),
		allow: make(map[string]map[string]bool),
	}
}

func (a *fakeAuthz) Require(principal, action string) error {
	for _, role := range a.roles[principal] {
		if a.allow[role] != nil && a.allow[role][action] {
			return nil
		}
	}
	return ErrPermissionDenied
}

func (a *fakeAuthz) BindRole(_, principal, role string) error {
	a.roles[principal] = append(a.roles[principal], role)
	return nil
}

func (a *fakeAuthz) UnbindRole(_, principal, role string) error {
	idx := -1
	for i, r := range a.roles[principal] {
		if r == role {
			idx = i
			break
		}
	}
	if idx >= 0 {
		a.roles[principal] = append(a.roles[principal][:idx], a.roles[principal][idx+1:]...)
	}
	return nil
}

func (a *fakeAuthz) GrantAction(_, role, action string) error {
	if a.allow[role] == nil {
		a.allow[role] = make(map[string]bool)
	}
	a.allow[role][action] = true
	return nil
}

func (a *fakeAuthz) RevokeAction(_, role, action string) error {
	if a.allow[role] != nil {
		delete(a.allow[role], action)
	}
	return nil
}

func (a *fakeAuthz) RolesFor(principal string) ([]string, error) {
	return a.roles[principal], nil
}

func (a *fakeAuthz) ActionsFor(principal string) ([][]string, error) {
	seen := make(map[string]bool)
	var result [][]string
	for _, role := range a.roles[principal] {
		for action := range a.allow[role] {
			if !seen[action] {
				seen[action] = true
				result = append(result, []string{role, action})
			}
		}
	}
	return result, nil
}
func (a *fakeAuthz) ListActions() []string                { return allActions() }
func (a *fakeAuthz) ListRoles() []string                  { return []string{RolePluginCommandBasic, RoleBotOwner} }
func (a *fakeAuthz) InitBuiltinPolicies(_ []string) error { return nil }
func (a *fakeAuthz) IsKnownRole(role string) bool {
	return role == RolePluginCommandBasic || role == RoleBotOwner
}
func (a *fakeAuthz) IsKnownAction(_ string) bool { return true }

var _ Authorizer = (*fakeAuthz)(nil)

// TestFakeAuthz_BindUnbindImmediate 验证绑定角色后可执行动作、解绑后立即被拒绝。
func TestFakeAuthz_BindUnbindImmediate(t *testing.T) {
	a := newFakeAuthz()
	_ = a.GrantAction("system::test", RolePluginCommandBasic, ActionStateRead)

	principal := "plugin::signin::001"

	if err := a.Require(principal, ActionStateRead); err == nil {
		t.Fatal("expected deny before bind")
	}

	_ = a.BindRole("system::test", principal, RolePluginCommandBasic)
	if err := a.Require(principal, ActionStateRead); err != nil {
		t.Fatalf("expected allow after bind: %v", err)
	}

	_ = a.UnbindRole("system::test", principal, RolePluginCommandBasic)
	if err := a.Require(principal, ActionStateRead); err == nil {
		t.Fatal("expected deny after unbind")
	}
}

// TestBuiltinActionsNoWildcards 校验内置动作集不含通配符。
func TestBuiltinActionsNoWildcards(t *testing.T) {
	for _, action := range allActions() {
		if action == "state.*" || action == "plugin.*" {
			t.Errorf("wildcard action defined: %s", action)
		}
	}
}

// TestValidatePrincipal 校验主体格式。
func TestValidatePrincipal(t *testing.T) {
	tests := []struct {
		p     string
		valid bool
	}{
		{"user::123", true},
		{"plugin::signin::01J4", true},
		{"system::startup", true},
		{"signin", false},
		{"::abc", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ValidatePrincipal(tt.p); got != tt.valid {
			t.Errorf("ValidatePrincipal(%q) = %v, want %v", tt.p, got, tt.valid)
		}
	}
}

// TestValidateRoleName 校验角色名格式。
func TestValidateRoleName(t *testing.T) {
	tests := []struct {
		role  string
		valid bool
	}{
		{"role::plugin_command_basic", true},
		{"role::bot_owner", true},
		{"plugin_command_basic", false},
		{"role::", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ValidateRoleName(tt.role); got != tt.valid {
			t.Errorf("ValidateRoleName(%q) = %v, want %v", tt.role, got, tt.valid)
		}
	}
}
