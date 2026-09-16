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

// HTTPAccess 为插件提供受限的 HTTP 客户端能力（借鉴浏览器 Fetch API 安全模型）：
// 目标域名必须在 allow_hosts 白名单内，GET/POST 分属独立权限以贯彻最小权限；
// 每次请求前做 Scope 检查并记审计，响应体限制 1 MiB、客户端超时 10 秒。
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
//
// 参数：
//   - scopeChecker：Scope 检查器，按 http:get/http:post 校验目标 host
//   - audit：审计日志器，每次检查（放行与拒绝）都记录
//   - pluginID：插件 ID，用于审计主体标识
//   - installationID：安装实例 ID，用于审计主体标识
//   - logger：日志器
//
// 返回：HTTP 访问设施实例。
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

// Get 发起 GET 请求（受 allow_hosts Scope 约束）：先提取 URL 的 host 并做
// Scope 检查与审计，不在白名单则记 deny 并拒绝；响应体以 LimitReader
// 限制为 1 MiB，返回状态码与响应内容。
//
// 参数：
//   - ctx：请求上下文
//   - url：完整 URL，其 host 须在 http:get 的 allow_hosts 白名单内
//   - headers：附加请求头，可为 nil
//
// 返回：HTTP 状态码、响应体（最多 1 MiB）与错误；host 被拒绝或请求发起失败时状态码为 0。
func (h *HTTPAccess) Get(ctx context.Context, url string, headers map[string]string) (int, []byte, error) {
	host := extractHost(url)
	principal := fmt.Sprintf("plugin:%s:%s", h.pluginID, h.installationID)

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

// Post 发起 POST 请求：流程与 Get 一致，但使用独立的 PermHTTPPost 权限
// （与 GET 分离以贯彻最小权限原则）并允许携带请求体。
//
// 参数：
//   - ctx：请求上下文
//   - url：完整 URL，其 host 须在 http:post 的 allow_hosts 白名单内
//   - headers：附加请求头，可为 nil
//   - body：请求体，可为 nil
//
// 返回：HTTP 状态码、响应体（最多 1 MiB）与错误；host 被拒绝或请求发起失败时状态码为 0。
func (h *HTTPAccess) Post(ctx context.Context, url string, headers map[string]string, body []byte) (int, []byte, error) {
	host := extractHost(url)
	principal := fmt.Sprintf("plugin:%s:%s", h.pluginID, h.installationID)

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

// extractHost 从 URL 提取主机名（去掉 scheme、path 与端口），
// 轻量实现以避免引入 net/url 的开销，例如
// "https://api.example.com:8080/v1/data" → "api.example.com"。
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
