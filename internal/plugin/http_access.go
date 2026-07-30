package plugin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// HTTPAccess 为插件提供受限的 HTTP 客户端能力。
//
// 设计思路借鉴了浏览器 Fetch API 的安全模型：
//   - 插件可以发起 HTTP 请求，但必须事先声明允许访问的目标域名（allow_hosts）
//   - GET 和 POST 分别对应独立的权限（PermHTTPGet / PermHTTPPost），
//     实现最小权限原则——只读插件不需要 POST 权限
//   - 每次请求前进行 Scope 检查和审计日志记录
//   - 响应体大小限制为 1 MiB，防止恶意服务器返回超大响应耗尽内存
//   - HTTP 客户端设置 10 秒超时，防止插件发起长时间阻塞的请求
type HTTPAccess struct {
	scopeChecker   *ScopeChecker
	audit          *AuditLogger
	pluginID       string
	installationID string
	client         *http.Client
	logger         *zap.Logger
}

// NewHTTPAccess 创建 HTTP 访问设施。
// 内部创建一个带 10 秒超时的 http.Client，防止插件发起的请求无限等待。
func NewHTTPAccess(scopeChecker *ScopeChecker, audit *AuditLogger, pluginID, installationID string, logger *zap.Logger) *HTTPAccess {
	return &HTTPAccess{
		scopeChecker:   scopeChecker,
		audit:          audit,
		pluginID:       pluginID,
		installationID: installationID,
		client: &http.Client{
			Timeout: 10 * time.Second, // 全局超时：连接 + 读取合计 10 秒
		},
		logger: logger,
	}
}

// Get 发起 GET 请求（受 allow_hosts Scope 约束）。
//
// 安全流程：
//  1. 从 URL 中提取 host
//  2. 通过 ScopeChecker 检查 host 是否在 allow_hosts 白名单中
//  3. 不在白名单 → 记录 deny 审计日志并拒绝
//  4. 在白名单 → 记录 allow 审计日志，发起请求
//  5. 响应体限制为 1 MiB（LimitReader），防止内存溢出
//
// 参数：
//   - ctx: 上下文，支持请求取消和超时
//   - url: 请求的完整 URL
//   - headers: 自定义请求头
//
// 返回：
//   - int: HTTP 状态码
//   - []byte: 响应体（最大 1 MiB）
//   - error: 权限拒绝、请求失败、读取失败等错误
func (h *HTTPAccess) Get(ctx context.Context, url string, headers map[string]string) (int, []byte, error) {
	host := extractHost(url)
	principal := fmt.Sprintf("plugin:%s:%s", h.pluginID, h.installationID)

	// Scope 检查：验证目标 host 是否在白名单中
	if !h.scopeChecker.CheckHTTPHost(PermHTTPGet, host) {
		h.audit.Log(&AuditEntry{
			Principal:      principal,
			Permission:     string(PermHTTPGet),
			Scope:          "host=" + host,
			Decision:       "deny",
			Reason:         "host not in allow_hosts",
			PluginID:       h.pluginID,
			InstallationID: h.installationID,
		})
		return 0, nil, fmt.Errorf("http: host %q not allowed", host)
	}

	// 审计日志：记录允许访问的决策
	h.audit.Log(&AuditEntry{
		Principal:      principal,
		Permission:     string(PermHTTPGet),
		Scope:          "host=" + host,
		Decision:       "allow",
		PluginID:       h.pluginID,
		InstallationID: h.installationID,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("http: new request: %w", err)
	}
	// 设置插件自定义请求头
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("http: request failed: %w", err)
	}
	defer resp.Body.Close()

	// 限制响应体大小为 1 MiB，防止恶意服务器返回超大响应体导致内存溢出
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("http: read body: %w", err)
	}
	return resp.StatusCode, body, nil
}

// Post 发起 POST 请求（受 allow_hosts Scope 约束）。
//
// 安全流程与 Get 一致，区别在于：
//   - 使用 PermHTTPPost 权限（与 GET 分离，实现最小权限原则）
//   - 允许携带请求体
//
// 参数：
//   - ctx: 上下文
//   - url: 请求的完整 URL
//   - headers: 自定义请求头
//   - body: 请求体内容
//
// 返回：
//   - int: HTTP 状态码
//   - []byte: 响应体（最大 1 MiB）
//   - error: 权限拒绝、请求失败、读取失败等错误
func (h *HTTPAccess) Post(ctx context.Context, url string, headers map[string]string, body []byte) (int, []byte, error) {
	host := extractHost(url)
	principal := fmt.Sprintf("plugin:%s:%s", h.pluginID, h.installationID)

	// Scope 检查：验证目标 host 是否在白名单中
	if !h.scopeChecker.CheckHTTPHost(PermHTTPPost, host) {
		h.audit.Log(&AuditEntry{
			Principal:      principal,
			Permission:     string(PermHTTPPost),
			Scope:          "host=" + host,
			Decision:       "deny",
			Reason:         "host not in allow_hosts",
			PluginID:       h.pluginID,
			InstallationID: h.installationID,
		})
		return 0, nil, fmt.Errorf("http: host %q not allowed", host)
	}

	// 审计日志：记录允许访问的决策
	h.audit.Log(&AuditEntry{
		Principal:      principal,
		Permission:     string(PermHTTPPost),
		Scope:          "host=" + host,
		Decision:       "allow",
		PluginID:       h.pluginID,
		InstallationID: h.installationID,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, fmt.Errorf("http: new request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("http: request failed: %w", err)
	}
	defer resp.Body.Close()

	// 限制响应体大小为 1 MiB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("http: read body: %w", err)
	}
	return resp.StatusCode, respBody, nil
}

// extractHost 从 URL 中提取主机名（去除 scheme、path 和端口）。
//
// 这是一个轻量级的 URL 解析函数，避免引入 net/url 的开销。
// 处理逻辑：
//  1. 去掉 scheme（如 "https://"）
//  2. 去掉 path 部分（第一个 "/" 之后的内容）
//  3. 去掉端口号（最后一个 ":" 之后的内容）
//
// 示例：
//   - "https://api.example.com:8080/v1/data" → "api.example.com"
//   - "http://localhost:3000/test" → "localhost"
func extractHost(rawURL string) string {
	s := rawURL
	if idx := strings.Index(s, "://"); idx != -1 {
		s = s[idx+3:]
	}
	if idx := strings.Index(s, "/"); idx != -1 {
		s = s[:idx]
	}
	// 去掉端口号
	if idx := strings.LastIndex(s, ":"); idx != -1 {
		s = s[:idx]
	}
	return s
}
