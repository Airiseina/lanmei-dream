package plugin

import (
	"fmt"
)

// Permission 权限标识符，格式 <facility>:<action>。
//
// 设计借鉴了 Tauri v2 的权限模型：
//   - 采用 "设施:动作" 的二维命名，便于按设施分组管理和审计
//   - 每个权限是原子的、不可再分的，插件只能拥有或不拥有某个权限
//   - 权限本身只表达"能做什么"，具体"在什么范围内做"由 Scope 约束
//
// 例如：
//   - "state:read" 表示可以读取状态存储
//   - "http:get" 表示可以发起 HTTP GET 请求
//   - 但具体能读取哪些 key、能请求哪些 host，由对应的 Scope 决定
type Permission string

const (
	// state 设施：状态存储操作
	// 插件可使用键值存储持久化自己的状态数据
	PermStateRead   Permission = "state:read"   // 读取状态值
	PermStateWrite  Permission = "state:write"  // 写入状态值
	PermStateDelete Permission = "state:delete" // 删除状态键
	PermStateAtomic Permission = "state:atomic" // 原子操作（CAS、incr_by、set_if_not_exists）

	// db 设施：数据库操作
	// 使用 IndexedDB 隔离模型：每个插件只能访问自己的命名空间（plugin_<pluginID>_ 前缀）
	PermDBRead  Permission = "db:read"  // 数据库读取
	PermDBWrite Permission = "db:write" // 数据库写入（insert/update/delete）

	// http 设施：HTTP 网络请求
	// GET 和 POST 分离为独立权限，实现最小权限原则
	PermHTTPGet  Permission = "http:get"  // HTTP GET 请求
	PermHTTPPost Permission = "http:post" // HTTP POST 请求

	// command 设施：命令处理
	// 允许插件注册并响应用户命令（如 /签到、/查询）
	PermCommandHandle Permission = "command:handle" // 处理用户命令

	// tool 设施：AI 工具
	// 允许插件注册为 LLM 可调用的工具，或调用其他工具
	PermToolRegister Permission = "tool:register" // 注册 AI 工具
	PermToolCall     Permission = "tool:call"     // 调用 AI 工具

	// message 设施：消息发送
	// 允许插件主动向用户发送消息
	PermMessageReply Permission = "message:reply" // 回复用户消息
)

// Scope 对权限的运行时范围约束。
//
// 设计原理（借鉴 Tauri v2）：
// Permission 表达"能做什么"，Scope 表达"在什么范围内做"。
// 例如：
//   - Permission=http:get + Scope{allow_hosts: ["api.example.com"]}
//     → 只允许请求 api.example.com，而非任意域名
//   - Permission=state:read + Scope{key_prefix: "user_"}
//     → 只允许读取 "user_" 前缀的 key
//
// Params 中的键值对由各设施的 ScopeChecker 解释，
// 不同的设施定义各自的参数格式（如 key_prefix、allow_hosts、tables）。
type Scope struct {
	Permission Permission        `json:"permission"`       // 约束所属的权限
	Params     map[string]string `json:"params,omitempty"` // 约束参数（由各设施的 ScopeChecker 解释）
}

// PermissionSet 预定义权限集，将常用权限组合打包。
//
// 设计目的：
//   - 降低插件开发者的授权配置复杂度——选择一个权限集即可获得一组相关权限
//   - 提供合理的默认值，避免开发者遗漏必要权限或过度授权
//   - 权限集可以附带默认 Scope，为常见场景提供开箱即用的约束
//
// 使用方式：
//   - 插件在 manifest 中声明所需的权限集标识符（如 "state:default"）
//   - 也可以额外声明个别权限和自定义 Scope
//   - ResolvePermissions 函数将权限集和显式权限合并为最终权限列表
type PermissionSet struct {
	Identifier  string       `json:"identifier"`       // 权限集唯一标识符（如 "state:default"）
	Description string       `json:"description"`      // 人类可读的描述
	Permissions []Permission `json:"permissions"`      // 包含的权限列表
	Scopes      []Scope      `json:"scopes,omitempty"` // 附加的默认 Scope 约束
}

// 预定义权限集
var (
	SetStateDefault = PermissionSet{
		Identifier:  "state:default",
		Description: "状态存储基础读写",
		Permissions: []Permission{PermStateRead, PermStateWrite},
	}
	SetStateFull = PermissionSet{
		Identifier:  "state:full",
		Description: "状态存储完全访问（含删除和原子操作）",
		Permissions: []Permission{PermStateRead, PermStateWrite, PermStateDelete, PermStateAtomic},
	}
	SetDBDefault = PermissionSet{
		Identifier:  "db:default",
		Description: "数据库基础读写（隔离命名空间）",
		Permissions: []Permission{PermDBRead, PermDBWrite},
	}
	SetHTTPReadOnly = PermissionSet{
		Identifier:  "http:read-only",
		Description: "HTTP 只读访问（GET）",
		Permissions: []Permission{PermHTTPGet},
	}
	SetHTTPFull = PermissionSet{
		Identifier:  "http:full",
		Description: "HTTP 完全访问（GET + POST）",
		Permissions: []Permission{PermHTTPGet, PermHTTPPost},
	}
	SetCommandBasic = PermissionSet{
		Identifier:  "command:basic",
		Description: "命令处理 + 消息回复",
		Permissions: []Permission{PermCommandHandle, PermMessageReply},
	}
	SetToolProvider = PermissionSet{
		Identifier:  "tool:provider",
		Description: "AI 工具注册与调用",
		Permissions: []Permission{PermToolRegister, PermToolCall},
	}
)

// AllPermissionSets 所有预定义权限集的映射
var AllPermissionSets = map[string]*PermissionSet{
	SetStateDefault.Identifier: &SetStateDefault,
	SetStateFull.Identifier:    &SetStateFull,
	SetDBDefault.Identifier:    &SetDBDefault,
	SetHTTPReadOnly.Identifier: &SetHTTPReadOnly,
	SetHTTPFull.Identifier:     &SetHTTPFull,
	SetCommandBasic.Identifier: &SetCommandBasic,
	SetToolProvider.Identifier: &SetToolProvider,
}

// ResolvePermissions 解析权限集标识符和显式权限为最终的权限列表。
//
// 处理逻辑：
//  1. 逐个解析权限集标识符，从 AllPermissionSets 映射中查找对应的 PermissionSet
//  2. 将权限集中的权限去重后加入结果列表
//  3. 将显式声明的权限去重后追加到结果列表
//  4. 未知权限集标识符返回错误（fail-closed 策略）
//
// 参数：
//   - sets: 权限集标识符列表（如 ["state:default", "http:read-only"]）
//   - explicit: 显式声明的权限列表（如 [PermStateDelete]）
//
// 返回：
//   - []Permission: 去重后的完整权限列表
//   - error: 遇到未知权限集标识符时返回错误
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

// PermissionRequest 插件在 manifest 中声明的权限请求。
//
// 设计借鉴了 Android 的权限声明模型：
//   - Sets：引用预定义权限集（批量声明）
//   - Permissions：声明个别权限（精细控制）
//   - Scopes：为特定权限添加运行时约束
//   - Required：区分必需权限和可选权限——可选权限在用户拒绝后仍可运行
//   - Reason：人类可读的权限用途说明（用于授权确认界面）
type PermissionRequest struct {
	Sets        []string     `json:"sets,omitempty"`        // 权限集标识符列表
	Permissions []Permission `json:"permissions,omitempty"` // 显式权限列表
	Scopes      []Scope      `json:"scopes,omitempty"`      // Scope 约束列表
	Required    bool         `json:"required"`              // 是否为必需权限（false 表示可选）
	Reason      string       `json:"reason"`                // 权限用途说明
}

// Capability 插件能力授权，将权限和 Scope 绑定到特定插件安装实例。
//
// 设计借鉴了 Tauri v2 的 Capability 模型：
//   - Capability 是授权的载体，连接了"谁（PluginID + InstallationID）"
//     和"能做什么（PermissionSets + Permissions + Scopes）"
//   - 同一个插件的不同安装实例可以有不同的 Capability，实现同插件不同权限
//   - PermissionSets 和 Permissions 都会被 ResolvePermissions 解析为最终的权限列表
//   - Scopes 为解析后的权限提供运行时范围约束
//
// 生命周期：
//  1. 插件在 manifest 中声明 PermissionRequest
//  2. 用户安装时确认授权
//  3. 系统将授权结果存储为 Capability
//  4. 运行时 ScopeChecker 和各 Access 组件基于 Capability 执行权限检查
type Capability struct {
	PluginID       string       `json:"plugin_id"`                 // 插件标识符
	InstallationID string       `json:"installation_id,omitempty"` // 安装实例标识符（同插件可多次安装）
	PermissionSets []string     `json:"permission_sets"`           // 授权的权限集列表
	Permissions    []Permission `json:"permissions,omitempty"`     // 额外授权的个别权限
	Scopes         []Scope      `json:"scopes,omitempty"`          // 运行时 Scope 约束
}

// ResourceQuota 运行时资源配额，限制单个插件的资源消耗。
//
// 设计原理：
// WASM 插件在宿主进程中运行，如果不加以限制，恶意或有缺陷的插件可能
// 耗尽系统资源（内存、CPU、I/O）。ResourceQuota 从多个维度对资源使用进行限制：
//   - 内存限制：防止单个插件占用过多内存
//   - CPU 限制：防止单次调用执行时间过长
//   - 状态限制：防止状态存储无限增长
//   - 频率限制：防止插件过于频繁地调用宿主功能（防 DDoS）
//   - 并发限制：防止插件同时执行过多任务
//
// 这些配额值是保守的默认值，可根据插件的实际需求在 Capability 中调整。
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
