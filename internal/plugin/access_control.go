package plugin

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 权限动作标识，格式为 <resource>.<verb>，是 Casbin 策略的 act 维度：
// 主体绑定角色、角色持有动作，Require 按动作精确匹配判定；新增动作必须同步登记到 allActions()。
const (
	// ActionCommandHandle 处理命令：允许插件处理路由到自身的 command 事件。
	ActionCommandHandle = "command.handle"

	// ActionMessageReply 消息回复：允许插件把输出回复到触发事件的目标会话。
	ActionMessageReply = "message.reply"

	// ActionStateRead 状态读取：允许通过 state_get 读取本安装实例的私有状态。
	ActionStateRead = "state.read"

	// ActionStateWrite 状态写入：允许通过 state_set 等 Host Function 写入私有状态。
	ActionStateWrite = "state.write"

	// ActionStateDelete 状态删除：允许通过 state_delete 删除本安装实例的状态键。
	ActionStateDelete = "state.delete"

	// ActionPluginList 插件管理：列出已安装或已注册的插件。
	ActionPluginList = "plugin.list"

	// ActionPluginInspect 插件管理：查看插件元数据与安装详情。
	ActionPluginInspect = "plugin.inspect"

	// ActionPluginInstall 插件管理：从公网 HTTPS 直链安装 Wasm 插件。
	ActionPluginInstall = "plugin.install"

	// ActionPluginLoad 插件管理：加载安装记录，创建实例并完成初始化握手。
	ActionPluginLoad = "plugin.load"

	// ActionPluginStart 插件管理：启动插件并提供服务。
	ActionPluginStart = "plugin.start"

	// ActionPluginStop 插件管理：停止已启动的插件，保留注册与安装信息。
	ActionPluginStop = "plugin.stop"

	// ActionPluginUnload 插件管理：停止并注销插件，保留安装文件、策略与状态。
	ActionPluginUnload = "plugin.unload"

	// ActionPluginDelete 插件管理：删除安装记录与托管文件。
	ActionPluginDelete = "plugin.delete"

	// ActionPluginConfigure 插件管理：修改插件安装级配置。
	ActionPluginConfigure = "plugin.configure"

	// ActionPluginUpgrade 插件管理：以新版本替换现有安装。
	ActionPluginUpgrade = "plugin.upgrade"

	// ActionPluginRoleBind 角色管理：为插件安装实例绑定角色。
	ActionPluginRoleBind = "plugin.role.bind"

	// ActionPluginRoleUnbind 角色管理：解绑插件安装实例的角色。
	ActionPluginRoleUnbind = "plugin.role.unbind"

	// ActionRoleRead 角色管理：查看角色、动作及其绑定关系。
	ActionRoleRead = "role.read"

	// ActionRoleManage 角色管理：授予或撤销角色的动作权限。
	ActionRoleManage = "role.manage"

	// ActionAuditRead 审计：读取插件权限检查的审计日志。
	ActionAuditRead = "audit.read"
)

// 内置角色标识，仅以下三者可通过 IsKnownRole 校验，策略由 InitBuiltinPolicies 写入。
const (
	// RolePluginCommandBasic 基础命令插件角色：command.handle、message.reply 与 state.read/state.write/state.delete，
	// 通常绑定到命令类插件的安装实例。
	RolePluginCommandBasic = "role::plugin_command_basic"

	// RoleBotOwner 机器人所有者角色：全部插件管理、角色管理与审计读取动作，仅在系统尚无 owner 时由超级用户引导。
	RoleBotOwner = "role::bot_owner"

	// RolePluginRuntime 宿主运行时角色：plugin.load 与 plugin.start，供 system::startup 恢复已启用插件。
	RolePluginRuntime = "role::plugin_runtime"
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

// Authorizer 集中管理权限判定和策略变更：Require 是资源访问前的统一鉴权入口，
// BindRole、GrantAction 等策略变更方法自身也要求调用者（actor）持有对应管理动作。
type Authorizer interface {
	// Require 校验主体是否持有动作，未授权返回包装 ErrPermissionDenied 的错误。
	Require(principal, action string) error
	// BindRole 为主体绑定角色，需要 actor 持有 plugin.role.bind。
	BindRole(actor, principal, role string) error
	// UnbindRole 解绑主体角色，需要 actor 持有 plugin.role.unbind。
	UnbindRole(actor, principal, role string) error
	// GrantAction 为角色授予动作，需要 actor 持有 role.manage。
	GrantAction(actor, role, action string) error
	// RevokeAction 撤销角色的动作，需要 actor 持有 role.manage。
	RevokeAction(actor, role, action string) error
	// RolesFor 返回主体直接绑定的角色列表。
	RolesFor(principal string) ([]string, error)
	// ActionsFor 返回主体经角色继承的有效权限（[主体, 动作] 二元组）。
	ActionsFor(principal string) ([][]string, error)
	// ListActions 返回系统预定义的全部动作。
	ListActions() []string
	// ListRoles 返回系统内置的全部角色。
	ListRoles() []string
	// InitBuiltinPolicies 幂等写入内置策略，并引导超级用户成为机器人所有者。
	InitBuiltinPolicies(superUserPrincipals []string) error
	// IsKnownRole 判断角色是否为内置角色，是策略写入的白名单。
	IsKnownRole(role string) bool
	// IsKnownAction 判断动作是否为预定义动作，是策略写入的白名单。
	IsKnownAction(action string) bool
}

var _ Authorizer = (*Service)(nil)

// Service 是并发安全的 Casbin 授权服务。
type Service struct {
	enforcer     *casbin.SyncedEnforcer
	mu           sync.RWMutex
	knownRoles   map[string]struct{}
	knownActions map[string]struct{}
	logger       *zap.Logger
}

// NewService 复用现有 GORM 连接创建授权服务，策略持久化在 plugin_casbin_rule 表并开启 AutoSave。
//
// 参数：
//   - db：GORM 连接（PostgreSQL），用于加载与自动保存 Casbin 策略
//
// 返回：创建成功返回 Service；adapter 或 enforcer 初始化失败时返回错误。
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
		logger:       zap.L().Named("access_control"),
	}
	for _, action := range allActions() {
		service.knownActions[action] = struct{}{}
	}
	for _, role := range []string{RolePluginCommandBasic, RoleBotOwner, RolePluginRuntime} {
		service.knownRoles[role] = struct{}{}
	}
	return service, nil
}

// Require 执行精确动作匹配；无匹配策略时默认拒绝（deny-by-default）。
//
// 参数：
//   - principal：主体标识，插件场景必须是由宿主用 PluginPrincipal 构造的安装实例主体
//   - action：动作标识，须为 allActions() 中的预定义动作
//
// 返回：允许返回 nil；被拒绝返回包装 ErrPermissionDenied 的错误；enforcer 执行失败返回该错误。
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

// BindRole 为主体绑定角色，是插件授权的入口（声明角色不会自动生效，必须显式绑定）。
//
// 参数：
//   - actor：发起操作的主体，须持有 plugin.role.bind
//   - principal：目标主体，须符合 ValidatePrincipal 格式（插件须为安装实例主体）
//   - role：目标角色，须为 IsKnownRole 认可的已知角色
//
// 返回：权限不足、角色未知、主体格式无效或持久化失败时返回错误。
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
	s.logger.Info("绑定角色", zap.String("actor", actor), zap.String("principal", principal), zap.String("role", role))
	return nil
}

// UnbindRole 解除主体与角色的绑定，解绑后该角色的动作立即失效。
//
// 参数：
//   - actor：发起操作的主体，须持有 plugin.role.unbind
//   - principal：目标主体，须符合 ValidatePrincipal 格式
//   - role：目标角色，须为 IsKnownRole 认可的已知角色
//
// 返回：权限不足、角色未知、主体格式无效或持久化失败时返回错误。
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
	s.logger.Info("解绑角色", zap.String("actor", actor), zap.String("principal", principal), zap.String("role", role))
	return nil
}

// GrantAction 为角色授予动作权限，未知角色或动作一律拒绝，防止写入越权策略。
//
// 参数：
//   - actor：发起操作的主体，须持有 role.manage
//   - role：目标角色，须为 IsKnownRole 认可的已知角色
//   - action：待授予动作，须为 IsKnownAction 认可的预定义动作
//
// 返回：权限不足、角色或动作未知、持久化失败时返回错误。
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
	s.logger.Info("授予动作", zap.String("actor", actor), zap.String("role", role), zap.String("action", action))
	return nil
}

// RevokeAction 撤销角色持有的动作权限。
//
// 参数：
//   - actor：发起操作的主体，须持有 role.manage
//   - role：目标角色，须为 IsKnownRole 认可的已知角色
//   - action：待撤销动作，须为 IsKnownAction 认可的预定义动作
//
// 返回：权限不足、角色或动作未知、持久化失败时返回错误。
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
	s.logger.Info("撤销动作", zap.String("actor", actor), zap.String("role", role), zap.String("action", action))
	return nil
}

// RolesFor 返回主体直接绑定的角色（不含经其他主体继承的角色），结果按字典序排序。
//
// 参数：
//   - principal：主体标识
//
// 返回：角色列表；查询失败时返回错误。
func (s *Service) RolesFor(principal string) ([]string, error) {
	roles, err := s.enforcer.GetRolesForUser(principal)
	if err != nil {
		return nil, fmt.Errorf("查询主体角色: %w", err)
	}
	sort.Strings(roles)
	return roles, nil
}

// ActionsFor 返回主体经角色继承得到的隐式权限，每项为 [主体, 动作] 二元组，按字典序排序。
//
// 参数：
//   - principal：主体标识
//
// 返回：权限二元组列表；查询失败时返回错误。
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

// ListActions 返回系统预定义的全部动作，按字典序排序。
//
// 返回：动作标识列表副本，调用方可安全修改。
func (s *Service) ListActions() []string {
	actions := allActions()
	sort.Strings(actions)
	return actions
}

// ListRoles 返回系统内置的全部角色，按字典序排序。
//
// 返回：角色标识列表副本，调用方可安全修改。
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

// IsKnownRole 判断角色是否为内置角色，策略变更前用它做白名单校验。
//
// 参数：
//   - role：角色标识
//
// 返回：内置角色返回 true。
func (s *Service) IsKnownRole(role string) bool {
	s.mu.RLock()
	_, ok := s.knownRoles[role]
	s.mu.RUnlock()
	return ok
}

// IsKnownAction 判断动作是否为系统预定义动作，策略变更前用它做白名单校验。
//
// 参数：
//   - action：动作标识
//
// 返回：预定义动作返回 true。
func (s *Service) IsKnownAction(action string) bool {
	s.mu.RLock()
	_, ok := s.knownActions[action]
	s.mu.RUnlock()
	return ok
}

// InitBuiltinPolicies 幂等写入内置角色-动作策略，并把超级用户引导为机器人所有者。
// 仅当数据库中尚无任何 bot_owner 绑定时才执行引导，因此后续配置变化不会覆盖运行期授权结果。
//
// 参数：
//   - superUserPrincipals：超级用户主体列表，格式 user::<platform>::<platformUserID>，由 ParseSuperUsers 生成
//
// 返回：策略检查、写入或查询失败时返回错误；已存在 owner 时引导步骤直接跳过。
func (s *Service) InitBuiltinPolicies(superUserPrincipals []string) error {
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
	for _, principal := range superUserPrincipals {
		if _, err := s.enforcer.AddRoleForUser(principal, RoleBotOwner); err != nil {
			return fmt.Errorf("引导 bot_owner %s: %w", principal, err)
		}
		s.logger.Info("引导 bot_owner", zap.String("principal", principal), zap.String("role", RoleBotOwner))
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

// ValidateRoleName 校验角色名是否符合 role:: 前缀约定且后缀非空。
// 只检查格式，不判断是否为内置角色；已知角色校验请用 IsKnownRole。
//
// 参数：
//   - role：待校验的角色名
//
// 返回：格式合法返回 true。
func ValidateRoleName(role string) bool {
	return strings.HasPrefix(role, "role::") && len(role) > len("role::")
}

// ValidatePrincipal 校验主体是否使用受支持的命名空间前缀：user::（用户）、plugin::（插件安装实例）、
// system::（宿主系统主体）。
//
// 参数：
//   - principal：待校验的主体标识
//
// 返回：以任一合法前缀开头即返回 true；不校验前缀之后内容的格式。
func ValidatePrincipal(principal string) bool {
	return strings.HasPrefix(principal, "user::") ||
		strings.HasPrefix(principal, "plugin::") ||
		strings.HasPrefix(principal, "system::")
}

// ParseSuperUsers 解析超级用户配置字符串为 principal 列表，
// 格式 qq:123456,wechat:wxid_xxx → [user::qq::123456, user::wechat::wxid_xxx]。
//
// 参数：
//   - superUsersStr：逗号分隔的 platform:userID 列表，允许包含空白；空字符串返回 nil
//
// 返回：user:: 前缀的主体列表；忽略无冒号的无效条目，无有效条目时返回 nil。
func ParseSuperUsers(superUsersStr string) []string {
	if superUsersStr == "" {
		return nil
	}
	var principals []string
	for _, entry := range strings.Split(superUsersStr, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) == 2 {
			principals = append(principals, UserPrincipal(parts[0], parts[1]))
		}
	}
	return principals
}
