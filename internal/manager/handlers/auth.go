package handlers

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/manager/auth"
	"github.com/DaWesen/lanmei-dream/internal/manager/crypto"
	"github.com/DaWesen/lanmei-dream/internal/manager/middleware"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// ─────────────────────────────────────────────
// 登录 / 登出 / 刷新（公开）
// ─────────────────────────────────────────────

// passwordLoginReq 密码登录请求。
type passwordLoginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// PasswordLogin 密码登录（可能返回 PendingTOTP 需二次验证）。
func (h *Handler) PasswordLogin(c fiber.Ctx) error {
	var req passwordLoginReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if req.Username == "" || req.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "用户名和密码不能为空"})
	}

	session, pending, err := h.authSvc.PasswordLogin(c.Context(), req.Username, req.Password, c.IP(), clientUA(c))
	switch {
	case err == nil && pending != nil:
		return c.JSON(fiber.Map{"pending_totp": pending})
	case err == nil && session != nil:
		return h.loginSuccess(c, session)
	case errors.Is(err, auth.ErrAccountLocked):
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": err.Error()})
	case errors.Is(err, auth.ErrAccountDisabled):
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "账号已禁用"})
	case err != nil:
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "用户名或密码错误"})
	default:
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "内部错误"})
	}
}

// totpVerifyReq TOTP 二次验证请求。
type totpVerifyReq struct {
	PendingToken string `json:"pending_token"`
	Code         string `json:"code"`
}

// VerifyTOTP 密码登录后的 TOTP 二次验证。
func (h *Handler) VerifyTOTP(c fiber.Ctx) error {
	var req totpVerifyReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	session, err := h.authSvc.VerifyTOTP(c.Context(), req.PendingToken, req.Code, c.IP(), clientUA(c))
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "验证码错误或已过期"})
	}
	return h.loginSuccess(c, session)
}

// webauthnLoginBeginReq passkey 登录第一步请求。
type webauthnLoginBeginReq struct {
	Username string `json:"username"`
}

// WebAuthnLoginBegin passkey 登录第一步：返回断言挑战。
// 非安全上下文（IP 访问 / 无 HTTPS）时返回 400 + code=WEBAUTHN_UNAVAILABLE，
// 前端据此自动回退纯密码登录。
func (h *Handler) WebAuthnLoginBegin(c fiber.Ctx) error {
	var req webauthnLoginBeginReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if req.Username == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "用户名不能为空"})
	}
	assertion, sessionID, err := h.authSvc.BeginPasskeyLogin(c.Context(), req.Username, c.Hostname())
	if err != nil {
		return webauthnErr(c, err)
	}
	return c.JSON(fiber.Map{"session_token": sessionID, "assertion": assertion})
}

// WebAuthnLoginFinish passkey 登录第二步：校验断言（body 为浏览器原始响应，
// session_token / username 经 query 传递，避免依赖表单编码）。
func (h *Handler) WebAuthnLoginFinish(c fiber.Ctx) error {
	sessionID := c.Query("session_token")
	if sessionID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "缺少 session_token"})
	}
	username := c.Query("username")
	if username == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "缺少 username"})
	}
	session, err := h.authSvc.FinishPasskeyLogin(c.Context(), username, sessionID, c.Body(), c.IP(), clientUA(c))
	if err != nil {
		return webauthnErr(c, err)
	}
	return h.loginSuccess(c, session)
}

// refreshReq 刷新会话请求。
type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
}

// Refresh 轮换 refresh token 并签发新 access token。
func (h *Handler) Refresh(c fiber.Ctx) error {
	var req refreshReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	session, err := h.authSvc.Refresh(c.Context(), req.RefreshToken, c.IP(), clientUA(c))
	if err != nil {
		if errors.Is(err, auth.ErrSessionReused) {
			// 会话复用：安全事件，前端应清除本地凭据
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "检测到会话异常，请重新登录",
				"code":  "SESSION_REUSED",
			})
		}
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "会话无效或已过期"})
	}
	return c.JSON(session)
}

// logoutReq 登出请求。
type logoutReq struct {
	RefreshToken string `json:"refresh_token"`
}

// Logout 登出（吊销会话）。
func (h *Handler) Logout(c fiber.Ctx) error {
	var req logoutReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if err := h.authSvc.Logout(c.Context(), req.RefreshToken); err != nil {
		h.logger.Warn("auth: logout failed", zap.Error(err))
	}
	return c.JSON(fiber.Map{"ok": true})
}

// loginSuccess 登录成功统一出口：写 CSRF Cookie 并返回会话。
func (h *Handler) loginSuccess(c fiber.Ctx, session *auth.SessionResult) error {
	csrf, err := crypto.RandomToken(24)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "内部错误"})
	}
	middleware.SetCSRFCookie(c, csrf)
	return c.JSON(session)
}

// webauthnErr 统一处理 WebAuthn 错误（区分不可用与校验失败）。
func webauthnErr(c fiber.Ctx, err error) error {
	if errors.Is(err, auth.ErrWebAuthnUnavailable) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "passkey 不可用（需要 HTTPS 域名环境），请使用密码登录",
			"code":  "WEBAUTHN_UNAVAILABLE",
		})
	}
	return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "passkey 验证失败"})
}

// ─────────────────────────────────────────────
// 当前用户 / 会话管理（需登录）
// ─────────────────────────────────────────────

// Me 返回当前登录管理员信息与凭据状态。
func (h *Handler) Me(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	creds, _ := h.store.ListCredentials(c.Context(), admin.ID)
	hasTOTP := false
	hasPasskey := false
	for _, cred := range creds {
		if !cred.Enabled {
			continue
		}
		switch cred.Kind {
		case model.CredentialTOTP:
			hasTOTP = true
		case model.CredentialWebAuthn:
			hasPasskey = true
		}
	}
	passkeys, _ := h.authSvc.ListPasskeys(c.Context(), admin.ID)
	return c.JSON(fiber.Map{
		"id":            admin.ID,
		"username":      admin.Username,
		"role":          admin.Role,
		"display_name":  admin.DisplayName,
		"avatar":        admin.Avatar,
		"last_login_at": admin.LastLoginAt,
		"has_totp":      hasTOTP,
		"has_passkey":   hasPasskey,
		"passkeys":      passkeys,
	})
}

// StepUpVerifyReq 二次身份验证请求（TOTP 优先，未绑定则密码复核）。
type StepUpVerifyReq struct {
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
}

// StepUp 签发 step-up token（高危操作前置验证）。
func (h *Handler) StepUp(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	var req StepUpVerifyReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	token, err := h.authSvc.StepUpVerify(c.Context(), admin, req.Password, req.TOTPCode)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrStepUpRequired):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "请输入 TOTP 验证码或密码"})
		case errors.Is(err, auth.ErrTOTPInvalid):
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "验证码错误"})
		default:
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "身份验证失败"})
		}
	}
	h.auditOK(c, admin, "auth.step_up", "admin", strconv.FormatUint(uint64(admin.ID), 10), "")
	return c.JSON(fiber.Map{"step_up_token": token, "expires_in": 300})
}

// ListSessions 列出当前登录管理员（或超管指定管理员）的活跃会话。
func (h *Handler) ListSessions(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	targetID := admin.ID
	if q := c.Query("admin_id"); q != "" && admin.Role == model.AdminRoleSuper {
		if id, err := strconv.ParseUint(q, 10, 64); err == nil {
			targetID = uint(id)
		}
	}
	sessions, err := h.authSvc.ListSessions(c.Context(), targetID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	return c.JSON(fiber.Map{"sessions": sessions})
}

// RevokeSession 吊销指定会话（本人或超管）。
func (h *Handler) RevokeSession(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "无效的会话 ID"})
	}
	if err := h.authSvc.RevokeSession(c.Context(), uint(id)); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "吊销失败"})
	}
	h.auditOK(c, admin, "auth.session.revoke", "session", c.Params("id"), "")
	return c.JSON(fiber.Map{"ok": true})
}

// RevokeAllSessions 吊销全部会话（超管可指定 admin_id；本人默认吊销他人保留自己）。
func (h *Handler) RevokeAllSessions(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	targetID := admin.ID
	if q := c.Query("admin_id"); q != "" && admin.Role == model.AdminRoleSuper {
		if id, err := strconv.ParseUint(q, 10, 64); err == nil {
			targetID = uint(id)
		}
	}
	if err := h.authSvc.RevokeAllSessions(c.Context(), targetID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "吊销失败"})
	}
	h.auditOK(c, admin, "auth.session.revoke_all", "admin", strconv.FormatUint(uint64(targetID), 10), "")
	return c.JSON(fiber.Map{"ok": true})
}

// ─────────────────────────────────────────────
// 密码 / TOTP / Passkey 管理（需登录 + step-up）
// ─────────────────────────────────────────────

// changePasswordReq 修改密码请求。
type changePasswordReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// ChangePassword 修改当前登录账号密码（需 step-up）。
func (h *Handler) ChangePassword(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	var req changePasswordReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if len(req.NewPassword) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "新密码至少 8 位"})
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "参数不完整"})
	}
	ok, err := crypto.VerifyPassword(admin.PasswordHash, req.OldPassword)
	if err != nil || !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "原密码错误"})
	}
	hash, err := crypto.HashPassword(req.NewPassword)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "密码处理失败"})
	}
	admin.PasswordHash = hash
	admin.AuthSource = model.AuthSourceDB // 面板改过密码后 env 不再覆盖
	if err := h.store.UpdateAdmin(c.Context(), admin); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "保存失败"})
	}
	h.auditOK(c, admin, "auth.password.change", "admin", strconv.FormatUint(uint64(admin.ID), 10), "")
	return c.JSON(fiber.Map{"ok": true})
}

// TOTPSetupBegin 开始 TOTP 绑定（返回 secret 与 otpauth URL 供二维码）。
func (h *Handler) TOTPSetupBegin(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	secret, otpauthURL, err := h.authSvc.BeginTOTPSetup(c.Context(), admin)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "TOTP 初始化失败"})
	}
	return c.JSON(fiber.Map{"secret": secret, "otpauth_url": otpauthURL})
}

// totpConfirmReq TOTP 绑定确认请求。
type totpConfirmReq struct {
	Code string `json:"code"`
}

// TOTPSetupConfirm 校验验证码并绑定 TOTP。
func (h *Handler) TOTPSetupConfirm(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	var req totpConfirmReq
	if err := h.bind(c, &req); err != nil {
		return err
	}
	if err := h.authSvc.ConfirmTOTPSetup(c.Context(), admin, req.Code); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "验证码错误，请重试"})
	}
	h.auditOK(c, admin, "auth.totp.bind", "admin", strconv.FormatUint(uint64(admin.ID), 10), "")
	return c.JSON(fiber.Map{"ok": true})
}

// TOTPRemove 解绑 TOTP（step-up 已由路由层校验）。
func (h *Handler) TOTPRemove(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	if err := h.authSvc.RemoveTOTP(c.Context(), admin.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "解绑失败"})
	}
	h.auditOK(c, admin, "auth.totp.remove", "admin", strconv.FormatUint(uint64(admin.ID), 10), "")
	return c.JSON(fiber.Map{"ok": true})
}

// WebAuthnRegisterBegin passkey 注册第一步（登录态）。
func (h *Handler) WebAuthnRegisterBegin(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	creation, sessionID, err := h.authSvc.BeginPasskeyRegistration(c.Context(), admin, c.Hostname())
	if err != nil {
		return webauthnErr(c, err)
	}
	return c.JSON(fiber.Map{"session_token": sessionID, "creation": creation})
}

// WebAuthnRegisterFinish passkey 注册第二步（body 为浏览器原始响应，
// session_token 经 query 传递）。
func (h *Handler) WebAuthnRegisterFinish(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	sessionID := c.Query("session_token")
	if sessionID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "缺少 session_token"})
	}
	if err := h.authSvc.FinishPasskeyRegistration(c.Context(), admin, sessionID, c.Body()); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "passkey 注册失败"})
	}
	h.auditOK(c, admin, "auth.passkey.register", "admin", strconv.FormatUint(uint64(admin.ID), 10), "")
	return c.JSON(fiber.Map{"ok": true})
}

// RemovePasskey 删除指定 passkey（step-up 已由路由层校验）。
func (h *Handler) RemovePasskey(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	credentialID := c.Params("credential_id")
	if credentialID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "缺少凭据 ID"})
	}
	if err := h.authSvc.RemovePasskey(c.Context(), credentialID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "删除失败"})
	}
	h.auditOK(c, admin, "auth.passkey.remove", "passkey", credentialID, "")
	return c.JSON(fiber.Map{"ok": true})
}

// ListPasskeys 列出当前登录管理员 passkey。
func (h *Handler) ListPasskeys(c fiber.Ctx) error {
	admin := currentAdmin(c)
	if admin == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "未登录"})
	}
	passkeys, err := h.authSvc.ListPasskeys(c.Context(), admin.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "查询失败"})
	}
	return c.JSON(fiber.Map{"passkeys": passkeys})
}
