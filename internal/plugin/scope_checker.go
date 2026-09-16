// Package plugin 实现 WASM 插件的运行时安全沙箱：能力授权（Capability）、
// 作用域检查（Scope）、审计日志（Audit）以及数据库/HTTP/状态存储访问控制。
//
// 安全模型借鉴 Tauri v2：Permission 是原子权限标识，Scope 对其施加运行时约束
// （如 state 的 key 前缀、HTTP 的 host 白名单），Capability 将二者绑定到安装实例。
package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// ScopeChecker 在运行时校验操作参数是否满足 Scope 约束，是对 Permission 的进一步限定
// （如拥有 state:read 却只能访问 "user_" 前缀的 key）。
//
// 核心判定模式"有约束则严格、无约束则放行"：某权限存在 Scope 时，操作须匹配至少
// 一个 Scope 才允许；不存在任何 Scope 时该权限下的操作默认放行。该模式由
// hasScopeForPermission 实现——遍历所有 Scope 未匹配时返回 !hasScopeForPermission(perm)。
type ScopeChecker struct {
	scopes []Scope
}

// NewScopeChecker 创建 Scope 检查器。
//
// 参数：
//   - scopes：来自 Capability 的 Scope 列表；为空时所有权限视为无约束（Check 系列直接放行）
//
// 返回：检查器实例。
func NewScopeChecker(scopes []Scope) *ScopeChecker {
	return &ScopeChecker{scopes: scopes}
}

// CheckStateKey 检查 state key 是否落在允许的前缀范围内：Scope 未配置 key_prefix
// 时视为无约束直接允许，配置了则 key 必须以该前缀开头；遍历完所有 Scope 仍未匹配时，
// 该权限存在 Scope 约束则拒绝、不存在则放行。
//
// 参数：
//   - perm：待检查的权限（如 state:read、state:write）
//   - key：Guest 逻辑 key
//
// 返回：允许访问返回 true；存在约束且不匹配返回 false。
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
	// 存在 scope 约束但均不匹配时拒绝，无约束时放行
	return !sc.hasScopeForPermission(perm)
}

// CheckHTTPHost 检查 HTTP 目标 host 是否在 Scope 的 allow_hosts 白名单
// （JSON 数组，支持 "*.example.com" 通配）内；未配置 allow_hosts 视为无限制直接允许。
//
// 安全约束：allow_hosts 解析失败一律视为拒绝（fail-closed），避免配置错误导致越权；
// 存在 Scope 约束但 host 不匹配时立即返回 false，不再检查其他 Scope。
//
// 参数：
//   - perm：待检查的权限（http:get 或 http:post）
//   - host：从目标 URL 提取的主机名（不含端口）
//
// 返回：允许访问返回 true；白名单不匹配或解析失败返回 false。
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

// CheckDBTable 检查数据库表是否在 Scope 的 tables 列表内。
// 插件表使用 IndexedDB 式隔离命名空间（plugin_<pluginID>_<table>），
// 同名逻辑表物理隔离、互不干扰，匹配时逻辑表名与隔离表名都接受；
// 未配置 tables 视为无限制，可访问隔离命名空间内的任何表。
//
// 参数：
//   - perm：待检查的权限（db:read 或 db:write）
//   - pluginID：插件 ID，用于推导隔离表名
//   - table：逻辑表名
//
// 返回：允许访问返回 true；存在 tables 约束且不匹配或解析失败返回 false。
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

// IsolatedTableName 返回插件隔离后的完整表名 plugin_<pluginID>_<tableName>，
// 供 DBAccess 等组件构造 SQL 时使用，确保物理表名始终带有隔离前缀。
//
// 参数：
//   - pluginID：插件 ID
//   - table：逻辑表名
//
// 返回：带 plugin_<pluginID>_ 前缀的物理表名。
func IsolatedTableName(pluginID, table string) string {
	return fmt.Sprintf("plugin_%s_%s", pluginID, table)
}

// hasScopeForPermission 判断是否存在针对指定权限的 Scope 约束。
// 它是"有约束则严格、无约束则放行"模式的实现基础：Check 系列函数遍历完所有
// Scope 仍未匹配时返回其取反值——有约束则拒绝、无约束则放行，
// 避免新增权限漏配 Scope 时误拦合法操作。
func (sc *ScopeChecker) hasScopeForPermission(perm Permission) bool {
	for _, s := range sc.scopes {
		if s.Permission == perm {
			return true
		}
	}
	return false
}

// matchHost 执行域名匹配，支持 filepath.Match 风格通配符："*" 匹配所有 host，
// pattern == host 为精确匹配，其余（如 "*.example.com"）交给 filepath.Match。
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
