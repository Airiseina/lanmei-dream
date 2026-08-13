package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/manager/crypto"
	"github.com/DaWesen/lanmei-dream/internal/model"
	"go.uber.org/zap"
)

// ─────────────────────────────────────────────
// 登录流程
// ─────────────────────────────────────────────

// PasswordLogin 密码登录（第一步）。
// 若账号已绑定 TOTP：返回 PendingTOTP（挂起状态），需调用方继续走 TOTP 校验；
// 否则直接返回完整会话。
func (s *Service) PasswordLogin(ctx context.Context, username, password, ip, userAgent string) (*SessionResult, *PendingTOTP, error) {
	// 登录锁定检查（连续失败）
	locked, err := s.isLocked(ctx, username)
	if err != nil {
		return nil, nil, err
	}
	if locked {
		s.recordAttempt(ctx, username, nil, ip, userAgent, model.LoginMethodPassword, false, "locked")
		return nil, nil, ErrAccountLocked
	}

	admin, err := s.store.GetAdminByUsername(ctx, username)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: lookup user: %w", err)
	}
	// 账号不存在与密码错误返回同一泛化错误（防账号枚举）
	if admin == nil {
		s.recordAttempt(ctx, username, nil, ip, userAgent, model.LoginMethodPassword, false, "no_such_user")
		return nil, nil, ErrInvalidCredentials
	}
	if admin.Status != model.AdminStatusActive {
		s.recordAttempt(ctx, username, &admin.ID, ip, userAgent, model.LoginMethodPassword, false, "disabled")
		return nil, nil, ErrAccountDisabled
	}

	ok, err := crypto.VerifyPassword(admin.PasswordHash, password)
	if err != nil || !ok {
		s.recordAttempt(ctx, username, &admin.ID, ip, userAgent, model.LoginMethodPassword, false, "bad_password")
		return nil, nil, ErrInvalidCredentials
	}

	// 已绑定 TOTP → 进入挂起状态，等待 TOTP 验证码
	if s.hasCredential(ctx, admin.ID, model.CredentialTOTP) {
		pending, err := s.newPendingTOTP(admin.ID)
		if err != nil {
			return nil, nil, err
		}
		s.recordAttempt(ctx, username, &admin.ID, ip, userAgent, model.LoginMethodPassword, true, "password_ok_totp_pending")
		return nil, pending, nil
	}

	session, err := s.createSession(ctx, admin, ip, userAgent)
	if err != nil {
		return nil, nil, err
	}
	s.recordAttempt(ctx, username, &admin.ID, ip, userAgent, model.LoginMethodPassword, true, "ok")
	return session, nil, nil
}

// VerifyTOTP 密码登录后的 TOTP 二次验证（第二步）。
func (s *Service) VerifyTOTP(ctx context.Context, pendingToken, code, ip, userAgent string) (*SessionResult, error) {
	s.muPending()
	entry, ok := s.totpPending[pendingToken]
	delete(s.totpPending, pendingToken)
	s.unPending()

	if !ok || time.Now().After(entry.expiresAt) {
		return nil, ErrSessionInvalid
	}

	admin, err := s.store.GetAdminByID(ctx, entry.adminID)
	if err != nil || admin == nil {
		return nil, ErrSessionInvalid
	}
	if admin.Status != model.AdminStatusActive {
		return nil, ErrAccountDisabled
	}

	if err := s.verifyTOTPCredential(ctx, admin.ID, code); err != nil {
		s.recordAttempt(ctx, admin.Username, &admin.ID, ip, userAgent, model.LoginMethodPassword, false, "bad_totp")
		return nil, ErrTOTPInvalid
	}

	session, err := s.createSession(ctx, admin, ip, userAgent)
	if err != nil {
		return nil, err
	}
	s.recordAttempt(ctx, admin.Username, &admin.ID, ip, userAgent, model.LoginMethodPassword, true, "totp_ok")
	return session, nil
}

// StepUpVerify 高危操作二次认证：校验当前登录管理员的 TOTP 或密码，签发 step-up token。
// accessToken 用于绑定 step-up 与当前登录身份。
func (s *Service) StepUpVerify(ctx context.Context, admin *model.ManagerAdmin, password, totpCode string) (string, error) {
	if admin == nil {
		return "", ErrSessionInvalid
	}
	if admin.Status != model.AdminStatusActive {
		return "", ErrAccountDisabled
	}

	// TOTP 优先；未绑定 TOTP 时回退密码复核
	if s.hasCredential(ctx, admin.ID, model.CredentialTOTP) {
		if totpCode == "" {
			return "", ErrStepUpRequired
		}
		if err := s.verifyTOTPCredential(ctx, admin.ID, totpCode); err != nil {
			return "", ErrTOTPInvalid
		}
	} else {
		if password == "" {
			return "", ErrStepUpRequired
		}
		ok, err := crypto.VerifyPassword(admin.PasswordHash, password)
		if err != nil || !ok {
			return "", ErrInvalidCredentials
		}
	}

	token, err := s.jwt.Issue(admin, true)
	if err != nil {
		return "", err
	}
	// 记录 step-up token（用于绑定 access 身份）
	s.muStepUp()
	s.stepUp[token] = stepUpEntry{adminID: admin.ID, expiresAt: time.Now().Add(stepUpTTL)}
	s.unStepUp()
	return token, nil
}

// ─────────────────────────────────────────────
// 会话管理
// ─────────────────────────────────────────────

// Refresh 刷新会话（轮换 refresh token）。
// 复用检测：旧 refresh token 再次出现 → 视为泄露，吊销该账号全部会话。
func (s *Service) Refresh(ctx context.Context, refreshToken, ip, userAgent string) (*SessionResult, error) {
	if refreshToken == "" {
		return nil, ErrSessionInvalid
	}
	hash := crypto.SHA256Hex(refreshToken)

	session, err := s.store.GetSessionByRefreshHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("auth: lookup session: %w", err)
	}

	if session == nil {
		return nil, ErrSessionInvalid
	}
	// 复用检测：该 hash 是某个会话轮换前的旧摘要 → 旧 token 被再次使用
	if session.PrevRefreshHash == hash {
		s.revokeAll(ctx, session.AdminID)
		s.logger.Warn("auth: 检测到 refresh token 复用，已吊销全部会话",
			zap.Uint64("admin_id", uint64(session.AdminID)), zap.String("ip", ip))
		return nil, ErrSessionReused
	}
	if session.RevokedAt != nil || time.Now().After(session.ExpiresAt) {
		return nil, ErrSessionInvalid
	}

	admin, err := s.store.GetAdminByID(ctx, session.AdminID)
	if err != nil || admin == nil {
		return nil, ErrSessionInvalid
	}
	if admin.Status != model.AdminStatusActive {
		return nil, ErrAccountDisabled
	}

	// 轮换：旧摘要移到 PrevRefreshHash，签发新 refresh token
	newRefresh, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	session.PrevRefreshHash = session.RefreshHash
	session.RefreshHash = crypto.SHA256Hex(newRefresh)
	session.ExpiresAt = now.Add(s.cfg.RefreshTokenTTL)
	session.LastSeenAt = &now
	if err := s.store.UpdateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("auth: rotate session: %w", err)
	}

	access, err := s.jwt.Issue(admin, false)
	if err != nil {
		return nil, err
	}

	return &SessionResult{
		AdminID:         admin.ID,
		Username:        admin.Username,
		Role:            string(admin.Role),
		DisplayName:     admin.DisplayName,
		AccessToken:     access,
		RefreshToken:    newRefresh,
		AccessExpiresIn: int64(s.cfg.AccessTokenTTL.Seconds()),
	}, nil
}

// Logout 登出（吊销会话）。
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	hash := crypto.SHA256Hex(refreshToken)
	session, err := s.store.GetSessionByRefreshHash(ctx, hash)
	if err != nil || session == nil {
		return nil
	}
	if session.PrevRefreshHash == hash {
		// 登出时旧 token 也一律吊销
		s.revokeAll(ctx, session.AdminID)
		return nil
	}
	return s.store.RevokeSession(ctx, session.ID)
}

// RevokeSession 吊销指定会话。
func (s *Service) RevokeSession(ctx context.Context, sessionID uint) error {
	return s.store.RevokeSession(ctx, sessionID)
}

// RevokeAllSessions 吊销某管理员全部会话。
func (s *Service) RevokeAllSessions(ctx context.Context, adminID uint) error {
	return s.store.RevokeSessionsByAdmin(ctx, adminID)
}

// ListSessions 列出某管理员活跃会话。
func (s *Service) ListSessions(ctx context.Context, adminID uint) ([]model.AuthSession, error) {
	return s.store.ListSessionsByAdmin(ctx, adminID)
}

// createSession 创建新会话并执行配额裁剪（超出上限吊销最旧会话）。
func (s *Service) createSession(ctx context.Context, admin *model.ManagerAdmin, ip, userAgent string) (*SessionResult, error) {
	refreshToken, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	session := &model.AuthSession{
		AdminID:     admin.ID,
		RefreshHash: crypto.SHA256Hex(refreshToken),
		Device:      summarizeUA(userAgent),
		IP:          ip,
		UserAgent:   userAgent,
		IssuedAt:    now,
		ExpiresAt:   now.Add(s.cfg.RefreshTokenTTL),
		LastSeenAt:  &now,
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("auth: create session: %w", err)
	}

	// 会话配额裁剪
	if s.cfg.MaxSessionsPerUser > 0 {
		active, err := s.store.ListSessionsByAdmin(ctx, admin.ID)
		if err == nil && len(active) > s.cfg.MaxSessionsPerUser {
			oldest, err2 := s.store.OldestActiveSessionID(ctx, admin.ID)
			if err2 == nil && oldest != nil && oldest.ID != session.ID {
				_ = s.store.RevokeSession(ctx, oldest.ID)
			}
		}
	}

	access, err := s.jwt.Issue(admin, false)
	if err != nil {
		return nil, err
	}

	now2 := time.Now()
	admin.LastLoginAt = &now2
	_ = s.store.UpdateAdmin(ctx, admin)

	return &SessionResult{
		AdminID:         admin.ID,
		Username:        admin.Username,
		Role:            string(admin.Role),
		DisplayName:     admin.DisplayName,
		AccessToken:     access,
		RefreshToken:    refreshToken,
		AccessExpiresIn: int64(s.cfg.AccessTokenTTL.Seconds()),
		HasPasskey:      s.hasCredential(ctx, admin.ID, model.CredentialWebAuthn),
		HasTOTP:         s.hasCredential(ctx, admin.ID, model.CredentialTOTP),
	}, nil
}

// revokeAll 吊销某管理员全部会话。
func (s *Service) revokeAll(ctx context.Context, adminID uint) {
	_ = s.store.RevokeSessionsByAdmin(ctx, adminID)
}

// isLocked 检查账号是否处于登录锁定状态。
func (s *Service) isLocked(ctx context.Context, username string) (bool, error) {
	since := time.Now().Add(-s.cfg.LoginLockWindow)
	n, err := s.store.CountRecentLoginFails(ctx, username, since)
	if err != nil {
		return false, err
	}
	return int(n) >= s.cfg.MaxLoginFails, nil
}

// recordAttempt 记录登录尝试（成功/失败）。
func (s *Service) recordAttempt(ctx context.Context, username string, adminID *uint, ip, ua string, method model.LoginMethod, success bool, reason string) {
	att := &model.LoginAttempt{
		AdminID:   adminID,
		Username:  username,
		IP:        ip,
		UserAgent: ua,
		Method:    method,
		Success:   success,
		Reason:    reason,
		CreatedAt: time.Now(),
	}
	_ = s.store.CreateLoginAttempt(ctx, att)
}

// hasCredential 判断某管理员是否绑定指定类型凭据。
func (s *Service) hasCredential(ctx context.Context, adminID uint, kind model.CredentialKind) bool {
	cred, err := s.store.GetCredentialByKind(ctx, adminID, kind)
	return err == nil && cred != nil && cred.Enabled
}

// newPendingTOTP 创建 TOTP 挂起会话。
func (s *Service) newPendingTOTP(adminID uint) (*PendingTOTP, error) {
	token, err := randomToken(24)
	if err != nil {
		return nil, err
	}
	s.muPending()
	s.totpPending[token] = totpPendingEntry{adminID: adminID, expiresAt: time.Now().Add(totpPendingTTL)}
	s.unPending()
	return &PendingTOTP{Token: token, TTL: int64(totpPendingTTL.Seconds())}, nil
}
