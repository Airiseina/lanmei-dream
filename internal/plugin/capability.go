package plugin

import (
	"fmt"
)

// Permission 是 <facility>:<action> 格式的原子权限标识（借鉴 Tauri v2 权限模型），
// 不可再分；它只表达"能做什么"，"在什么范围内做"由 Scope 约束
// （如 "state:read" 表示能读状态存储，但可读哪些 key 取决于 Scope）。
type Permission string

const (
	// state 设施：状态存储操作，插件可用键值存储持久化自身状态。
	PermStateRead Permission = "state:read"

	// PermStateWrite state 设施：写入或更新状态键（state_set）。
	PermStateWrite Permission = "state:write"

	// PermStateDelete state 设施：删除状态键（state_delete）。
	PermStateDelete Permission = "state:delete"

	// PermStateAtomic state 设施：原子操作（CAS、incr_by、set_if_not_exists），供读改写在并发下安全执行。
	PermStateAtomic Permission = "state:atomic"

	// db 设施：数据库操作，采用 IndexedDB 隔离模型，
	// 每个插件只能访问自己的命名空间（plugin_<pluginID>_ 前缀）。
	PermDBRead Permission = "db:read"

	// PermDBWrite db 设施：在隔离命名空间内写入（insert/update/delete）。
	PermDBWrite Permission = "db:write"

	// http 设施：GET 与 POST 拆分为独立权限，实现最小权限原则。
	PermHTTPGet Permission = "http:get"

	// PermHTTPPost http 设施：发起 POST 请求（写操作，与只读的 GET 分列）。
	PermHTTPPost Permission = "http:post"

	// command 设施：允许插件注册并响应用户命令（如 /签到、/查询）。
	PermCommandHandle Permission = "command:handle"

	// tool 设施：允许插件注册为 LLM 可调用的工具，或调用其他工具。
	PermToolRegister Permission = "tool:register"

	// PermToolCall tool 设施：调用其他工具，与 tool:register 分列为独立权限。
	PermToolCall Permission = "tool:call"

	// message 设施：允许插件主动向用户发送消息。
	PermMessageReply Permission = "message:reply"
)

// Scope 对权限施加运行时范围约束（借鉴 Tauri v2）：Permission 表达"能做什么"，
// Scope 表达"在什么范围内做"——如 http:get + allow_hosts 只允许请求
// api.example.com，state:read + key_prefix 只允许读 "user_" 前缀的 key。
// Params 由各设施的 ScopeChecker 解释，参数格式因设施而异
// （如 key_prefix、allow_hosts、tables）。
type Scope struct {
	Permission Permission        `json:"permission"`
	Params     map[string]string `json:"params,omitempty"` // 由各设施的 ScopeChecker 解释
}

// PermissionSet 将常用权限组合打包，降低授权配置复杂度并提供合理默认值，
// 避免遗漏必要权限或过度授权；插件在 manifest 中声明权限集标识符即可，
// 也可额外声明个别权限和自定义 Scope，最终由 ResolvePermissions 合并。
type PermissionSet struct {
	Identifier  string       `json:"identifier"` // 如 "state:default"
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"`
	Scopes      []Scope      `json:"scopes,omitempty"` // 附加的默认 Scope 约束
}

var (
	// SetStateDefault 权限集 state:default：状态存储基础读写。
	SetStateDefault = PermissionSet{
		Identifier:  "state:default",
		Description: "状态存储基础读写",
		Permissions: []Permission{PermStateRead, PermStateWrite},
	}
	// SetStateFull 权限集 state:full：状态存储完全访问，含删除与原子操作。
	SetStateFull = PermissionSet{
		Identifier:  "state:full",
		Description: "状态存储完全访问（含删除和原子操作）",
		Permissions: []Permission{PermStateRead, PermStateWrite, PermStateDelete, PermStateAtomic},
	}
	// SetDBDefault 权限集 db:default：插件隔离命名空间内的数据库基础读写。
	SetDBDefault = PermissionSet{
		Identifier:  "db:default",
		Description: "数据库基础读写（隔离命名空间）",
		Permissions: []Permission{PermDBRead, PermDBWrite},
	}
	// SetHTTPReadOnly 权限集 http:read-only：仅允许 GET。
	SetHTTPReadOnly = PermissionSet{
		Identifier:  "http:read-only",
		Description: "HTTP 只读访问（GET）",
		Permissions: []Permission{PermHTTPGet},
	}
	// SetHTTPFull 权限集 http:full：允许 GET 与 POST。
	SetHTTPFull = PermissionSet{
		Identifier:  "http:full",
		Description: "HTTP 完全访问（GET + POST）",
		Permissions: []Permission{PermHTTPGet, PermHTTPPost},
	}
	// SetCommandBasic 权限集 command:basic：命令处理与消息回复。
	SetCommandBasic = PermissionSet{
		Identifier:  "command:basic",
		Description: "命令处理 + 消息回复",
		Permissions: []Permission{PermCommandHandle, PermMessageReply},
	}
	// SetToolProvider 权限集 tool:provider：AI 工具注册与调用。
	SetToolProvider = PermissionSet{
		Identifier:  "tool:provider",
		Description: "AI 工具注册与调用",
		Permissions: []Permission{PermToolRegister, PermToolCall},
	}
)

// AllPermissionSets 汇总全部预定义权限集，键为 PermissionSet.Identifier，供 ResolvePermissions 按标识符查找。
var AllPermissionSets = map[string]*PermissionSet{
	SetStateDefault.Identifier: &SetStateDefault,
	SetStateFull.Identifier:    &SetStateFull,
	SetDBDefault.Identifier:    &SetDBDefault,
	SetHTTPReadOnly.Identifier: &SetHTTPReadOnly,
	SetHTTPFull.Identifier:     &SetHTTPFull,
	SetCommandBasic.Identifier: &SetCommandBasic,
	SetToolProvider.Identifier: &SetToolProvider,
}

// ResolvePermissions 将权限集标识符（sets，如 ["state:default"]）与显式权限
// （explicit）解析合并为去重后的权限列表；未知权限集标识符返回错误（fail-closed，
// 调用方应中止授权流程，不得按部分结果继续）。
//
// 参数：
//   - sets：权限集标识符列表，每项必须存在于 AllPermissionSets
//   - explicit：权限集之外额外声明的单项权限
//
// 返回：按 sets 展开顺序、再追加 explicit 的去重权限列表；存在未知权限集时返回错误且不返回部分结果。
func ResolvePermissions(sets []string, explicit []Permission) ([]Permission, error) {
	seen := make(map[Permission]bool)
	var result []Permission

	for _, setID := range sets {
		set, ok := AllPermissionSets[setID]
		if !ok {
			return nil, fmt.Errorf("plugin: unknown permission set %q", setID)
		}
		for _, p := range set.Permissions {
			if !seen[p] {
				seen[p] = true
				result = append(result, p)
			}
		}
	}

	for _, p := range explicit {
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}

	return result, nil
}

// PermissionRequest 是插件在 manifest 中声明的权限请求（借鉴 Android 权限声明模型）：
// Sets/Permissions 分别声明权限集与个别权限，Scopes 附加运行时约束，
// Required 区分必需与可选权限（可选权限被拒绝后插件仍可运行），Reason 用于授权确认界面。
type PermissionRequest struct {
	Sets        []string     `json:"sets,omitempty"`
	Permissions []Permission `json:"permissions,omitempty"`
	Scopes      []Scope      `json:"scopes,omitempty"`
	Required    bool         `json:"required"` // false 表示可选权限
	Reason      string       `json:"reason"`   // 权限用途说明，展示于授权确认界面
}

// Capability 是插件能力授权（借鉴 Tauri v2），将权限与 Scope 绑定到具体安装实例：
// 它连接"谁（PluginID + InstallationID）"与"能做什么（PermissionSets + Permissions
// + Scopes）"，同一插件的不同安装实例可有不同授权。PermissionSets 与 Permissions
// 由 ResolvePermissions 解析，Scopes 提供运行时约束，供 ScopeChecker 与各 Access 组件判定。
type Capability struct {
	PluginID       string       `json:"plugin_id"`
	InstallationID string       `json:"installation_id,omitempty"` // 同插件可多次安装，授权互不相同
	PermissionSets []string     `json:"permission_sets"`
	Permissions    []Permission `json:"permissions,omitempty"`
	Scopes         []Scope      `json:"scopes,omitempty"`
}

// ResourceQuota 限制单个插件的运行时资源消耗。WASM 插件与宿主同进程运行，
// 不加限制时恶意或有缺陷的插件可能耗尽内存、CPU 与 I/O，故从内存、CPU、状态、
// 频率、并发等维度设限；字段默认值偏保守，可按插件实际需求在 Capability 中调整。
type ResourceQuota struct {
	MaxMemoryMB       int   `json:"max_memory_mb"`        // WASM 实例最大内存（MB），默认 16
	MaxCPUMs          int   `json:"max_cpu_ms"`           // 单次调用最大 CPU 时间（ms），默认 3000
	MaxStateKeys      int   `json:"max_state_keys"`       // 最大状态键数量，默认 100
	MaxStateTotalSize int64 `json:"max_state_total_size"` // 状态总大小（字节），默认 1 MiB
	MaxCallRate       int   `json:"max_call_rate"`        // 每分钟最大调用次数，默认 60
	MaxConcurrent     int   `json:"max_concurrent"`       // 最大并发调用数，默认 1
	MaxHTTPRequests   int   `json:"max_http_requests"`    // 每分钟最大 HTTP 请求数，默认 30
	MaxDBQueries      int   `json:"max_db_queries"`       // 每分钟最大 DB 查询数，默认 60
}

// DefaultResourceQuota 返回默认资源配额。
// 默认值偏保守，适合大多数轻量插件；资源需求较高的插件可在 Capability 中申请更大配额。
func DefaultResourceQuota() ResourceQuota {
	return ResourceQuota{
		MaxMemoryMB:       16,
		MaxCPUMs:          3000,
		MaxStateKeys:      100,
		MaxStateTotalSize: 1024 * 1024, // 1 MiB
		MaxCallRate:       60,
		MaxConcurrent:     1,
		MaxHTTPRequests:   30,
		MaxDBQueries:      60,
	}
}
