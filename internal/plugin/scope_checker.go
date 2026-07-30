// Package plugin 实现了 WASM 插件的运行时安全沙箱，包括能力授权（Capability）、
// 作用域检查（Scope）、审计日志（Audit）、数据库/HTTP/状态存储访问控制等。
//
// 安全模型的核心设计借鉴了 Tauri v2 的 Capability 模型：
//   - Permission：细粒度的原子权限标识（如 "state:read"、"http:get"）
//   - Scope：对权限的运行时约束（如 state 操作的 key 前缀、HTTP 请求的 host 白名单）
//   - Capability：将 Permission + Scope 绑定到特定插件安装实例的授权声明
package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// ScopeChecker 在运行时检查操作是否满足 Scope 约束。
//
// 设计原理：
// Scope 是对 Permission 的进一步限定。一个插件可能拥有 "state:read" 权限，
// 但通过 Scope 可以约束其只能访问 "user_" 前缀的 key。
// ScopeChecker 的职责是在每次操作前验证操作参数是否符合 Scope 中声明的约束。
//
// 核心判定模式——"有约束则严格、无约束则放行"：
//   - 如果某个权限存在 Scope 约束，则操作参数必须匹配至少一个 Scope 才被允许
//   - 如果某个权限不存在任何 Scope 约束，则该权限下的操作默认全部允许
//   - 这个模式通过 hasScopeForPermission 辅助函数实现：它判断某权限是否存在 Scope，
//     当遍历完所有 Scope 都未匹配时，返回 !hasScopeForPermission(perm)
type ScopeChecker struct {
	scopes []Scope
}

// NewScopeChecker 创建 Scope 检查器。
// scopes 参数来自 Capability 中声明的 Scope 列表。
func NewScopeChecker(scopes []Scope) *ScopeChecker {
	return &ScopeChecker{scopes: scopes}
}

// CheckStateKey 检查 state 操作的 key 是否在允许的前缀范围内。
//
// 匹配逻辑：
//  1. 遍历所有与目标权限匹配的 Scope
//  2. 如果 Scope 未配置 "key_prefix" 参数，表示无前缀约束，直接允许
//  3. 如果配置了 "key_prefix"，则 key 必须以该前缀开头才被允许
//  4. 遍历完所有 Scope 都未匹配时，采用兜底策略：
//     若该权限存在 Scope 约束则拒绝（说明有约束但不匹配），若不存在则允许（无限制）
//
// 参数：
//   - perm: 需要检查的权限（如 PermStateRead）
//   - key: 要访问的 state key
//
// 返回：true 表示允许访问，false 表示拒绝
func (sc *ScopeChecker) CheckStateKey(perm Permission, key string) bool {
	for _, s := range sc.scopes {
		if s.Permission != perm {
			continue
		}
		prefix, ok := s.Params["key_prefix"]
		if !ok {
			return true // 无前缀约束，允许所有
		}
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	// 如果存在该权限的 scope 约束但 key 不匹配任何前缀，则拒绝访问
	// 如果不存在该权限的 scope 约束，则默认允许（无限制）
	return !sc.hasScopeForPermission(perm)
}

// CheckHTTPHost 检查 HTTP 请求的目标 host 是否在白名单中。
//
// 匹配逻辑：
//  1. 遍历所有与目标权限匹配的 Scope
//  2. 从 Scope 的 "allow_hosts" 参数中解析白名单（JSON 数组格式）
//  3. 逐个匹配白名单中的 pattern（支持 * 通配符，如 "*.example.com"）
//  4. 如果未配置 "allow_hosts" 参数，表示无 host 限制，直接允许
//  5. 遍历完所有 Scope 都未匹配时，采用 hasScopeForPermission 兜底策略
//
// 安全考虑：
//   - allow_hosts 的 JSON 解析失败视为拒绝（fail-closed），防止配置错误导致越权
//   - 当存在 Scope 约束但 host 不在白名单中时，直接返回 false（不再检查其他 Scope）
//
// 参数：
//   - perm: 需要检查的权限（如 PermHTTPGet）
//   - host: 目标主机名（不含 scheme 和端口）
//
// 返回：true 表示允许访问，false 表示拒绝
func (sc *ScopeChecker) CheckHTTPHost(perm Permission, host string) bool {
	for _, s := range sc.scopes {
		if s.Permission != perm {
			continue
		}
		hostsJSON, ok := s.Params["allow_hosts"]
		if !ok {
			return true
		}
		var hosts []string
		if err := json.Unmarshal([]byte(hostsJSON), &hosts); err != nil {
			return false
		}
		for _, pattern := range hosts {
			if matchHost(pattern, host) {
				return true
			}
		}
		return false
	}
	return !sc.hasScopeForPermission(perm)
}

// CheckDBTable 检查数据库表是否在允许的列表中。
//
// IndexedDB 隔离模型说明：
// WASM 插件使用类似浏览器 IndexedDB 的隔离模型——每个插件只能访问自己的命名空间，
// 表名格式为 plugin_<pluginID>_<tableName>。这保证了即使不同插件使用了相同的逻辑表名，
// 在物理存储上也是隔离的，不会互相干扰。
//
// 匹配逻辑：
//  1. 先构造隔离表名 isolatedTable = "plugin_<pluginID>_<table>"
//  2. 遍历所有与目标权限匹配的 Scope
//  3. 从 Scope 的 "tables" 参数中解析允许的表名列表
//  4. 同时匹配逻辑表名和隔离表名（兼容两种配置方式）
//  5. 如果未配置 "tables" 参数，表示无表名限制，允许访问隔离命名空间内的任何表
//  6. 遍历完所有 Scope 都未匹配时，采用 hasScopeForPermission 兜底策略
//
// 参数：
//   - perm: 需要检查的权限（如 PermDBRead）
//   - pluginID: 插件标识符，用于构造隔离表名
//   - table: 逻辑表名（插件请求的原始表名）
//
// 返回：true 表示允许访问，false 表示拒绝
func (sc *ScopeChecker) CheckDBTable(perm Permission, pluginID, table string) bool {
	// IndexedDB 隔离模型：插件只能访问 plugin_<pluginID>_ 前缀的表
	isolatedTable := fmt.Sprintf("plugin_%s_%s", pluginID, table)

	for _, s := range sc.scopes {
		if s.Permission != perm {
			continue
		}
		tablesJSON, ok := s.Params["tables"]
		if !ok {
			// 无表名约束，允许访问隔离命名空间内的任何表
			return true
		}
		var tables []string
		if err := json.Unmarshal([]byte(tablesJSON), &tables); err != nil {
			return false
		}
		for _, t := range tables {
			if t == table || t == isolatedTable {
				return true
			}
		}
		return false
	}
	return !sc.hasScopeForPermission(perm)
}

// IsolatedTableName 返回插件隔离后的完整表名。
// 格式：plugin_<pluginID>_<tableName>
// 此函数供 DBAccess 等组件在构造 SQL 查询时使用，确保物理表名始终带有隔离前缀。
func IsolatedTableName(pluginID, table string) string {
	return fmt.Sprintf("plugin_%s_%s", pluginID, table)
}

// hasScopeForPermission 判断是否存在针对指定权限的 Scope 约束。
//
// 这是 ScopeChecker 的核心辅助函数，实现了"有约束则严格、无约束则放行"的判定模式：
//   - 返回 true：说明该权限存在 Scope 约束，但前面的 Check 函数遍历了所有 Scope
//     都未匹配，因此应拒绝访问（返回 !true = false）
//   - 返回 false：说明该权限不存在任何 Scope 约束，应默认允许（返回 !false = true）
//
// 这个模式确保了：
//   - 新增权限时，如果忘记配置 Scope，不会意外阻止合法操作
//   - 配置了 Scope 后，未匹配的操作会被正确拒绝
func (sc *ScopeChecker) hasScopeForPermission(perm Permission) bool {
	for _, s := range sc.scopes {
		if s.Permission == perm {
			return true
		}
	}
	return false
}

// matchHost 执行域名匹配，支持 filepath.Match 风格的通配符。
//
// 匹配规则：
//   - "*" 匹配所有 host（用于完全开放的场景）
//   - 精确匹配：pattern == host
//   - 通配符匹配：如 "*.example.com" 可匹配 "api.example.com"
//
// 参数：
//   - pattern: 白名单中的模式串（如 "*.example.com"）
//   - host: 实际请求的目标主机名
//
// 返回：true 表示匹配成功
func matchHost(pattern, host string) bool {
	if pattern == "*" {
		return true
	}
	if pattern == host {
		return true
	}
	matched, _ := filepath.Match(pattern, host)
	return matched
}
