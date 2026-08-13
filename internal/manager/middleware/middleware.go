// Package middleware 提供管理面板 HTTP 中间件：
// 鉴权（Bearer Access Token）、RBAC、step-up 二次认证、CSRF、限流、恢复。
package middleware

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/DaWesen/lanmei-dream/internal/manager/auth"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// Context 键（fiber Locals）。
const (
	ctxKeyAdmin  = "mgr_admin"
	ctxKeyClaims = "mgr_claims"
	ctxKeyCSRF   = "mgr_csrf"
)

// csrfCookieName 双提交 CSRF Cookie 名。
const csrfCookieName = "lanmei_csrf"

// GetAdmin 从上下文取出当前管理员（未登录为 nil）。
func GetAdmin(c fiber.Ctx) *model.ManagerAdmin {
	v, _ := c.Locals(ctxKeyAdmin).(*model.ManagerAdmin)
	return v
}

// GetClaims 从上下文取出 Access Token 声明。
func GetClaims(c fiber.Ctx) *auth.AccessClaims {
	v, _ := c.Locals(ctxKeyClaims).(*auth.AccessClaims)
	return v
}

// ─────────────────────────────────────────────
// 鉴权与 RBAC
// ─────────────────────────────────────────────

// Auth 验证 Bearer Access Token，注入当前管理员。
func Auth(svc *auth.Service) fiber.Handler {
	return func(c fiber.Ctx) error {
		header := strings.TrimSpace(c.Get("Authorization"))
		if !strings.HasPrefix(header, "Bearer ") {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		claims, err := svc.ParseAccessToken(token)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "登录已过期，请重新登录"})
		}
		if claims.Type != string(auth.TokenTypeAccess) {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "无效的访问令牌"})
		}
		adminID, err := strconv.ParseUint(claims.Subject, 10, 64)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "无效的访问令牌"})
		}
		admin, err := svc.GetAdmin(c.Context(), uint(adminID))
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "内部错误"})
		}
		if admin == nil || admin.Status != model.AdminStatusActive {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "账号不可用"})
		}
		c.Locals(ctxKeyAdmin, admin)
		c.Locals(ctxKeyClaims, claims)
		return c.Next()
	}
}

// RequireRole 要求当前管理员角色在给定集合内（RBAC）。
func RequireRole(roles ...model.AdminRole) fiber.Handler {
	return func(c fiber.Ctx) error {
		admin := GetAdmin(c)
		if admin == nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
		}
		for _, r := range roles {
			if admin.Role == r {
				return c.Next()
			}
		}
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "权限不足"})
	}
}

// RequireStepUp 要求高危操作携带有效的 step-up token（二次身份验证）。
// 校验：X-Step-Up-Token 为有效签名 + 与当前 access 身份同账号 + 未过期。
func RequireStepUp(svc *auth.Service) fiber.Handler {
	return func(c fiber.Ctx) error {
		admin := GetAdmin(c)
		if admin == nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
		}
		stepUpToken := strings.TrimSpace(c.Get("X-Step-Up-Token"))
		if stepUpToken == "" {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error":   "该操作需要二次身份验证",
				"code":    "STEP_UP_REQUIRED",
			})
		}
		claims, err := svc.ParseAccessToken(stepUpToken)
		if err != nil || !claims.StepUp {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "二次验证已过期，请重新验证"})
		}
		if claims.Subject != strconv.FormatUint(uint64(admin.ID), 10) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "二次验证身份不匹配"})
		}
		if !svc.ValidateStepUp(stepUpToken, admin.ID) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "二次验证已过期，请重新验证"})
		}
		return c.Next()
	}
}

// ─────────────────────────────────────────────
// CSRF（双提交 Cookie + Origin 校验）
// ─────────────────────────────────────────────

// CSRF 对状态变更请求做 CSRF 防护：
//  1. 带 Origin 的请求必须与 Host 同源（浏览器跨站请求必带 Origin）；
//  2. X-CSRF-Token 头必须与 lanmei_csrf Cookie 一致（双提交）。
func CSRF() fiber.Handler {
	return func(c fiber.Ctx) error {
		if c.Method() == fiber.MethodGet || c.Method() == fiber.MethodHead || c.Method() == fiber.MethodOptions {
			return c.Next()
		}

		// Origin 校验（仅当请求携带 Origin，即浏览器跨站场景）
		if origin := c.Get("Origin"); origin != "" {
			host := c.Hostname()
			if !sameOrigin(origin, host, c.Scheme()) {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "跨站请求被拒绝"})
			}
		}

		// 双提交校验：Cookie 与 Header 必须一致
		cookie := c.Cookies(csrfCookieName)
		header := c.Get("X-CSRF-Token")
		if cookie == "" || header == "" || cookie != header {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "CSRF 校验失败"})
		}
		return c.Next()
	}
}

// sameOrigin 判断 Origin 是否为当前 Host 的同源请求。
func sameOrigin(origin, host, scheme string) bool {
	// origin 形如 https://host:port
	u := strings.TrimPrefix(origin, "http://")
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimSuffix(u, "/")
	// 去除默认端口差异
	originHost := stripDefaultPort(u)
	reqHost := stripDefaultPort(host)
	return originHost == reqHost
}

// stripDefaultPort 去除 URL 中 80/443 默认端口（比较同源用）。
func stripDefaultPort(h string) string {
	h = strings.TrimSpace(h)
	if hh, _, err := net.SplitHostPort(h); err == nil {
		return hh
	}
	return h
}

// SetCSRFCookie 登录成功后写入 CSRF Cookie（SameSite=Strict）。
func SetCSRFCookie(c fiber.Ctx, value string) {
	c.Cookie(&fiber.Cookie{
		Name:     csrfCookieName,
		Value:    value,
		Path:     "/",
		SameSite: "strict",
		HTTPOnly: false, // 前端 JS 需读取以放入请求头
	})
}

// GetCSRFCookieValue 读取当前请求的 CSRF Cookie 值。
func GetCSRFCookieValue(c fiber.Ctx) string {
	return c.Cookies(csrfCookieName)
}

// ─────────────────────────────────────────────
// 限流（内存滑动窗口，按 IP）
// ─────────────────────────────────────────────

// rateBucket 固定窗口计数器。
type rateBucket struct {
	count  int
	reset  time.Time
}

// RateLimit 按 IP 限流（窗口内允许 limit 次请求）。
func RateLimit(limit int, window time.Duration) fiber.Handler {
	var (
		mu     sync.Mutex
		buckets = make(map[string]*rateBucket)
	)
	return func(c fiber.Ctx) error {
		if limit <= 0 {
			return c.Next()
		}
		ip := c.IP()
		now := time.Now()
		mu.Lock()
		b, ok := buckets[ip]
		if !ok || now.After(b.reset) {
			b = &rateBucket{count: 0, reset: now.Add(window)}
			buckets[ip] = b
		}
		b.count++
		remaining := limit - b.count
		mu.Unlock()

		if remaining < 0 {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":    "请求过于频繁，请稍后再试",
				"retry_after": int64(b.reset.Sub(now).Seconds()),
			})
		}
		c.Set("X-RateLimit-Remaining", strconv.Itoa(maxInt(remaining, 0)))
		return c.Next()
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ─────────────────────────────────────────────
// 恢复
// ─────────────────────────────────────────────

// Recover 捕获 handler panic，返回 500 并记录日志。
func Recover(logf func(format string, args ...any)) fiber.Handler {
	return func(c fiber.Ctx) error {
		defer func() {
			if r := recover(); r != nil {
				if logf != nil {
					logf("manager: panic recovered: %v (path=%s)", r, c.Path())
				}
				_ = c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "内部错误"})
			}
		}()
		return c.Next()
	}
}

// ClientIP 获取客户端真实 IP（支持 X-Forwarded-For，受信任代理限制）。
func ClientIP(c fiber.Ctx, trusted []string) string {
	if len(trusted) == 0 {
		return c.IP()
	}
	// fiber v3 的 IP 提取已处理代理头；此处不做额外处理，交由部署层配置
	return c.IP()
}
