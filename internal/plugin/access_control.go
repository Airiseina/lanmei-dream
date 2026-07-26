package plugin

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"gorm.io/gorm"
)

const (
	ActionCommandHandle = "command.handle"
	ActionMessageReply  = "message.reply"
	ActionStateRead     = "state.read"
	ActionStateWrite    = "state.write"
	ActionStateDelete   = "state.delete"

	ActionPluginList       = "plugin.list"
	ActionPluginInspect    = "plugin.inspect"
	ActionPluginInstall    = "plugin.install"
	ActionPluginLoad       = "plugin.load"
	ActionPluginStart      = "plugin.start"
	ActionPluginStop       = "plugin.stop"
	ActionPluginUnload     = "plugin.unload"
	ActionPluginDelete     = "plugin.delete"
	ActionPluginConfigure  = "plugin.configure"
	ActionPluginUpgrade    = "plugin.upgrade"
	ActionPluginRoleBind   = "plugin.role.bind"
	ActionPluginRoleUnbind = "plugin.role.unbind"
	ActionRoleRead         = "role.read"
	ActionRoleManage       = "role.manage"
	ActionAuditRead        = "audit.read"
)

const (
	RolePluginCommandBasic = "role::plugin_command_basic"
	RoleBotOwner           = "role::bot_owner"
	RolePluginRuntime      = "role::plugin_runtime"
)

const rbacModel = `
[request_definition]
r = sub, act

[policy_definition]
p = sub, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.act == p.act && g(r.sub, p.sub)
`

// Authorizer 集中管理权限判定和策略变更。
type Authorizer interface {
	Require(principal, action string) error
	BindRole(actor, principal, role string) error
	UnbindRole(actor, principal, role string) error
	GrantAction(actor, role, action string) error
	RevokeAction(actor, role, action string) error
	RolesFor(principal string) ([]string, error)
	ActionsFor(principal string) ([][]string, error)
	ListActions() []string
	ListRoles() []string
	InitBuiltinPolicies(superUsers []int64) error
	IsKnownRole(role string) bool
	IsKnownAction(action string) bool
}

var _ Authorizer = (*Service)(nil)

// Service 是并发安全的 Casbin 授权服务。
type Service struct {
	enforcer     *casbin.SyncedEnforcer
	mu           sync.RWMutex
	knownRoles   map[string]struct{}
	knownActions map[string]struct{}
}

// NewService 复用现有 GORM 连接并从 PostgreSQL 加载策略。
func NewService(db *gorm.DB) (*Service, error) {
	adapter, err := gormadapter.NewAdapterByDBUseTableName(db, "", "plugin_casbin_rule")
	if err != nil {
		return nil, fmt.Errorf("创建 Casbin adapter: %w", err)
	}
	m, err := model.NewModelFromString(rbacModel)
	if err != nil {
		return nil, fmt.Errorf("解析 Casbin 模型: %w", err)
	}
	enforcer, err := casbin.NewSyncedEnforcer(m, adapter)
	if err != nil {
		return nil, fmt.Errorf("创建 Casbin enforcer: %w", err)
	}
	enforcer.EnableAutoSave(true)

	service := &Service{
		enforcer:     enforcer,
		knownRoles:   make(map[string]struct{}),
		knownActions: make(map[string]struct{}),
	}
	for _, action := range allActions() {
		service.knownActions[action] = struct{}{}
	}
	for _, role := range []string{RolePluginCommandBasic, RoleBotOwner, RolePluginRuntime} {
		service.knownRoles[role] = struct{}{}
	}
	return service, nil
}

// Require 执行精确动作匹配；无策略时默认拒绝。
func (s *Service) Require(principal, action string) error {
	allowed, err := s.enforcer.Enforce(principal, action)
	if err != nil {
		return fmt.Errorf("执行 Casbin 鉴权: %w", err)
	}
	if !allowed {
		return fmt.Errorf("%w: principal=%s action=%s", ErrPermissionDenied, principal, action)
	}
	return nil
}

func (s *Service) BindRole(actor, principal, role string) error {
	if err := s.Require(actor, ActionPluginRoleBind); err != nil {
		return fmt.Errorf("绑定角色权限校验: %w", err)
	}
	if !s.IsKnownRole(role) {
		return fmt.Errorf("未知角色: %s", role)
	}
	if !ValidatePrincipal(principal) {
		return fmt.Errorf("主体格式无效: %s", principal)
	}
	if _, err := s.enforcer.AddRoleForUser(principal, role); err != nil {
		return fmt.Errorf("绑定角色 %s -> %s: %w", principal, role, err)
	}
	log.Printf("[access_control] actor=%s bind principal=%s role=%s", actor, principal, role)
	return nil
}

func (s *Service) UnbindRole(actor, principal, role string) error {
	if err := s.Require(actor, ActionPluginRoleUnbind); err != nil {
		return fmt.Errorf("解绑角色权限校验: %w", err)
	}
	if !s.IsKnownRole(role) {
		return fmt.Errorf("未知角色: %s", role)
	}
	if !ValidatePrincipal(principal) {
		return fmt.Errorf("主体格式无效: %s", principal)
	}
	if _, err := s.enforcer.DeleteRoleForUser(principal, role); err != nil {
		return fmt.Errorf("解绑角色 %s -> %s: %w", principal, role, err)
	}
	log.Printf("[access_control] actor=%s unbind principal=%s role=%s", actor, principal, role)
	return nil
}

func (s *Service) GrantAction(actor, role, action string) error {
	if err := s.Require(actor, ActionRoleManage); err != nil {
		return fmt.Errorf("授予动作权限校验: %w", err)
	}
	if !s.IsKnownRole(role) {
		return fmt.Errorf("未知角色: %s", role)
	}
	if !s.IsKnownAction(action) {
		return fmt.Errorf("未知动作: %s", action)
	}
	if _, err := s.enforcer.AddPolicy(role, action); err != nil {
		return fmt.Errorf("授予动作 %s -> %s: %w", role, action, err)
	}
	log.Printf("[access_control] actor=%s grant role=%s action=%s", actor, role, action)
	return nil
}

func (s *Service) RevokeAction(actor, role, action string) error {
	if err := s.Require(actor, ActionRoleManage); err != nil {
		return fmt.Errorf("撤销动作权限校验: %w", err)
	}
	if !s.IsKnownRole(role) {
		return fmt.Errorf("未知角色: %s", role)
	}
	if !s.IsKnownAction(action) {
		return fmt.Errorf("未知动作: %s", action)
	}
	if _, err := s.enforcer.RemovePolicy(role, action); err != nil {
		return fmt.Errorf("撤销动作 %s -> %s: %w", role, action, err)
	}
	log.Printf("[access_control] actor=%s revoke role=%s action=%s", actor, role, action)
	return nil
}

func (s *Service) RolesFor(principal string) ([]string, error) {
	roles, err := s.enforcer.GetRolesForUser(principal)
	if err != nil {
		return nil, fmt.Errorf("查询主体角色: %w", err)
	}
	sort.Strings(roles)
	return roles, nil
}

func (s *Service) ActionsFor(principal string) ([][]string, error) {
	permissions, err := s.enforcer.GetImplicitPermissionsForUser(principal)
	if err != nil {
		return nil, fmt.Errorf("查询主体动作: %w", err)
	}
	sort.Slice(permissions, func(i, j int) bool {
		return strings.Join(permissions[i], "\x00") < strings.Join(permissions[j], "\x00")
	})
	return permissions, nil
}

func (s *Service) ListActions() []string {
	actions := allActions()
	sort.Strings(actions)
	return actions
}

func (s *Service) ListRoles() []string {
	s.mu.RLock()
	roles := make([]string, 0, len(s.knownRoles))
	for role := range s.knownRoles {
		roles = append(roles, role)
	}
	s.mu.RUnlock()
	sort.Strings(roles)
	return roles
}

func (s *Service) IsKnownRole(role string) bool {
	s.mu.RLock()
	_, ok := s.knownRoles[role]
	s.mu.RUnlock()
	return ok
}

func (s *Service) IsKnownAction(action string) bool {
	s.mu.RLock()
	_, ok := s.knownActions[action]
	s.mu.RUnlock()
	return ok
}

// InitBuiltinPolicies 幂等写入内置策略，并只在不存在 owner 时引导 SUPER_USERS。
func (s *Service) InitBuiltinPolicies(superUsers []int64) error {
	for role, actions := range builtinRoleActions() {
		for _, action := range actions {
			hasPolicy, err := s.enforcer.HasPolicy(role, action)
			if err != nil {
				return fmt.Errorf("检查内置策略 %s -> %s: %w", role, action, err)
			}
			if !hasPolicy {
				if _, err := s.enforcer.AddPolicy(role, action); err != nil {
					return fmt.Errorf("添加内置策略 %s -> %s: %w", role, action, err)
				}
			}
		}
	}

	startupPrincipal := SystemPrincipal("startup")
	hasRuntimeRole, err := s.enforcer.HasRoleForUser(startupPrincipal, RolePluginRuntime)
	if err != nil {
		return fmt.Errorf("检查启动主体角色: %w", err)
	}
	if !hasRuntimeRole {
		if _, err := s.enforcer.AddRoleForUser(startupPrincipal, RolePluginRuntime); err != nil {
			return fmt.Errorf("绑定启动主体角色: %w", err)
		}
	}

	owners, err := s.enforcer.GetUsersForRole(RoleBotOwner)
	if err != nil {
		return fmt.Errorf("查询 bot_owner: %w", err)
	}
	if len(owners) != 0 {
		return nil
	}
	for _, userID := range superUsers {
		principal := UserPrincipal(userID)
		if _, err := s.enforcer.AddRoleForUser(principal, RoleBotOwner); err != nil {
			return fmt.Errorf("引导 bot_owner %s: %w", principal, err)
		}
		log.Printf("[access_control] bootstrap principal=%s role=%s", principal, RoleBotOwner)
	}
	return nil
}

func builtinRoleActions() map[string][]string {
	return map[string][]string{
		RolePluginCommandBasic: {
			ActionCommandHandle,
			ActionMessageReply,
			ActionStateRead,
			ActionStateWrite,
			ActionStateDelete,
		},
		RoleBotOwner: {
			ActionPluginList,
			ActionPluginInspect,
			ActionPluginInstall,
			ActionPluginLoad,
			ActionPluginStart,
			ActionPluginStop,
			ActionPluginUnload,
			ActionPluginDelete,
			ActionPluginConfigure,
			ActionPluginUpgrade,
			ActionPluginRoleBind,
			ActionPluginRoleUnbind,
			ActionRoleRead,
			ActionRoleManage,
			ActionAuditRead,
		},
		RolePluginRuntime: {
			ActionPluginLoad,
			ActionPluginStart,
		},
	}
}

func allActions() []string {
	return []string{
		ActionCommandHandle,
		ActionMessageReply,
		ActionStateRead,
		ActionStateWrite,
		ActionStateDelete,
		ActionPluginList,
		ActionPluginInspect,
		ActionPluginInstall,
		ActionPluginLoad,
		ActionPluginStart,
		ActionPluginStop,
		ActionPluginUnload,
		ActionPluginDelete,
		ActionPluginConfigure,
		ActionPluginUpgrade,
		ActionPluginRoleBind,
		ActionPluginRoleUnbind,
		ActionRoleRead,
		ActionRoleManage,
		ActionAuditRead,
	}
}

func ValidateRoleName(role string) bool {
	return strings.HasPrefix(role, "role::") && len(role) > len("role::")
}

func ValidatePrincipal(principal string) bool {
	return strings.HasPrefix(principal, "user::") ||
		strings.HasPrefix(principal, "plugin::") ||
		strings.HasPrefix(principal, "system::")
}
