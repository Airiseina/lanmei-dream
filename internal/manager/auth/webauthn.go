package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/DaWesen/lanmei-dream/internal/model"
)

// waSessionTTL WebAuthn 挑战有效期（开始与完成之间的窗口）。
const waSessionTTL = 5 * time.Minute

// webauthnSession WebAuthn 挑战会话（内存态，单进程）。
type webauthnSession struct {
	session   *webauthn.SessionData
	username  string
	adminID   uint
	host      string // 发起挑战时的请求 Host（用于完成时取回实例）
	expiresAt time.Time
}

// waSessions 挑战会话表。
var (
	waMu       sync.Mutex
	waSessions = map[string]*webauthnSession{}
	waInstance = map[string]*webauthn.WebAuthn{} // 按 rpid 缓存实例
)

// waUser 实现 webauthn.User 接口（管理员适配器）。
type waUser struct {
	id          uint
	name        string
	displayName string
	credentials []webauthn.Credential
}

// WebAuthnID 用户句柄（稳定：管理员 ID 十进制字符串）。
func (u *waUser) WebAuthnID() []byte { return []byte(fmt.Sprintf("%d", u.id)) }

// WebAuthnName 用户名。
func (u *waUser) WebAuthnName() string { return u.name }

// WebAuthnDisplayName 展示名。
func (u *waUser) WebAuthnDisplayName() string { return u.displayName }

// WebAuthnIcon 无图标。
func (u *waUser) WebAuthnIcon() string { return "" }

// WebAuthnCredentials 返回已注册凭据。
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// PasskeyInfo passkey 摘要（前端展示）。
type PasskeyInfo struct {
	ID          string     `json:"id"`
	Attestation string     `json:"attestation"`
	Transports  []string   `json:"transports"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

// WebAuthnInstance 获取（并按需构建）指定 Host 对应的 WebAuthn 实例。
// 仅当 RPID 为合法域名（非 IP）时可用；IP 访问返回 ErrWebAuthnUnavailable。
func (s *Service) WebAuthnInstance(host string) (*webauthn.WebAuthn, error) {
	if !s.cfg.EnableWebAuthn {
		return nil, ErrWebAuthnUnavailable
	}
	rpid := s.resolveRPID(host)
	if rpid == "" || net.ParseIP(rpid) != nil || strings.Contains(rpid, ":") {
		return nil, ErrWebAuthnUnavailable
	}
	waMu.Lock()
	defer waMu.Unlock()
	if inst, ok := waInstance[rpid]; ok {
		return inst, nil
	}
	displayName := s.cfg.WebAuthnDisplayName
	if displayName == "" {
		displayName = "Lanmei Manager"
	}
	inst, err := webauthn.New(&webauthn.Config{
		RPDisplayName: displayName,
		RPID:          rpid,
		RPOrigins:     s.resolveOrigins(rpid),
	})
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn init: %w", err)
	}
	waInstance[rpid] = inst
	return inst, nil
}

// resolveRPID 解析 Relying Party ID：显式配置优先，否则取请求 Host 域名部分。
func (s *Service) resolveRPID(host string) string {
	if s.cfg.WebAuthnRPID != "" {
		return strings.TrimSpace(s.cfg.WebAuthnRPID)
	}
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	// 去首尾方括号（IPv6 字面量）
	host = strings.Trim(host, "[]")
	return host
}

// resolveOrigins 解析允许的 Origin 列表。
func (s *Service) resolveOrigins(rpid string) []string {
	if len(s.cfg.WebAuthnOrigins) > 0 {
		return s.cfg.WebAuthnOrigins
	}
	// 默认 https；localhost 开发环境允许 http
	if rpid == "localhost" || strings.HasPrefix(rpid, "127.") {
		return []string{"http://" + rpid, "https://" + rpid}
	}
	return []string{"https://" + rpid}
}

// loadCredentials 加载某管理员的 passkey 凭据列表（DB JSON → 内存对象）。
func (s *Service) loadCredentials(ctx context.Context, adminID uint) ([]webauthn.Credential, error) {
	creds, err := s.store.ListCredentials(ctx, adminID)
	if err != nil {
		return nil, err
	}
	out := make([]webauthn.Credential, 0, len(creds))
	for _, c := range creds {
		if c.Kind != model.CredentialWebAuthn || len(c.Data) == 0 {
			continue
		}
		var wc webauthn.Credential
		if err := json.Unmarshal(c.Data, &wc); err != nil {
			continue // 损坏数据跳过，不阻塞注册/登录
		}
		out = append(out, wc)
	}
	return out, nil
}

// buildWAUser 构造 webauthn.User 适配器。
func (s *Service) buildWAUser(ctx context.Context, admin *model.ManagerAdmin) (*waUser, error) {
	creds, err := s.loadCredentials(ctx, admin.ID)
	if err != nil {
		return nil, err
	}
	return &waUser{
		id:          admin.ID,
		name:        admin.Username,
		displayName: firstNonEmpty(admin.DisplayName, admin.Username),
		credentials: creds,
	}, nil
}

// BeginPasskeyRegistration 开始 passkey 注册（登录态）。
// 返回注册挑战与挑战会话 ID。
func (s *Service) BeginPasskeyRegistration(ctx context.Context, admin *model.ManagerAdmin, host string) (*protocol.CredentialCreation, string, error) {
	inst, err := s.WebAuthnInstance(host)
	if err != nil {
		return nil, "", err
	}
	user, err := s.buildWAUser(ctx, admin)
	if err != nil {
		return nil, "", err
	}
	creation, sessionData, err := inst.BeginRegistration(user,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
	)
	if err != nil {
		return nil, "", fmt.Errorf("auth: webauthn begin registration: %w", err)
	}
	sessionID, err := s.storeWASession(ctx, admin, host, sessionData)
	if err != nil {
		return nil, "", err
	}
	return creation, sessionID, nil
}

// FinishPasskeyRegistration 完成 passkey 注册（登录态）。
// body 为浏览器返回的注册响应 JSON（原始字节）。
func (s *Service) FinishPasskeyRegistration(ctx context.Context, admin *model.ManagerAdmin, sessionID string, body []byte) error {
	entry := s.takeWASession(sessionID)
	if entry == nil || entry.adminID != admin.ID || entry.session == nil {
		return errors.New("auth: 注册挑战无效或已过期")
	}
	inst, err := s.WebAuthnInstance(entry.host)
	if err != nil {
		return err
	}
	user, err := s.buildWAUser(ctx, admin)
	if err != nil {
		return err
	}
	req := webauthnRequest(body)
	cred, err := inst.FinishRegistration(user, *entry.session, req)
	if err != nil {
		return fmt.Errorf("auth: webauthn finish registration: %w", err)
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	if err := s.store.CreateCredential(ctx, &model.AuthCredential{
		AdminID:      admin.ID,
		Kind:         model.CredentialWebAuthn,
		CredentialID: encodeCredentialID(cred.ID),
		Data:         data,
		Enabled:      true,
	}); err != nil {
		return fmt.Errorf("auth: save webauthn credential: %w", err)
	}
	return nil
}

// BeginPasskeyLogin 开始 passkey 登录（未登录态）。
func (s *Service) BeginPasskeyLogin(ctx context.Context, username, host string) (*protocol.CredentialAssertion, string, error) {
	inst, err := s.WebAuthnInstance(host)
	if err != nil {
		return nil, "", err
	}
	admin, err := s.store.GetAdminByUsername(ctx, username)
	if err != nil {
		return nil, "", err
	}
	if admin == nil || admin.Status != model.AdminStatusActive {
		return nil, "", ErrInvalidCredentials
	}
	user, err := s.buildWAUser(ctx, admin)
	if err != nil {
		return nil, "", err
	}
	if len(user.credentials) == 0 {
		return nil, "", ErrInvalidCredentials
	}
	assertion, sessionData, err := inst.BeginLogin(user,
		webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		return nil, "", fmt.Errorf("auth: webauthn begin login: %w", err)
	}
	sessionID, err := s.storeWASession(ctx, admin, host, sessionData)
	if err != nil {
		return nil, "", err
	}
	return assertion, sessionID, nil
}

// FinishPasskeyLogin 完成 passkey 登录并建立会话。
// body 为浏览器返回的断言响应 JSON（原始字节）。
func (s *Service) FinishPasskeyLogin(ctx context.Context, username, sessionID string, body []byte, ip, ua string) (*SessionResult, error) {
	entry := s.takeWASession(sessionID)
	if entry == nil || entry.session == nil || entry.username != username {
		return nil, ErrSessionInvalid
	}
	inst, err := s.WebAuthnInstance(entry.host)
	if err != nil {
		return nil, err
	}
	admin, err := s.store.GetAdminByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if admin == nil || admin.Status != model.AdminStatusActive {
		return nil, ErrAccountDisabled
	}
	user, err := s.buildWAUser(ctx, admin)
	if err != nil {
		return nil, err
	}
	req := webauthnRequest(body)
	cred, err := inst.FinishLogin(user, *entry.session, req)
	if err != nil {
		s.recordAttempt(ctx, username, &admin.ID, ip, ua, model.LoginMethodWebAuthn, false, "assertion_failed")
		return nil, ErrInvalidCredentials
	}
	// 更新签名计数（防克隆）
	s.updateCredentialSignCount(ctx, admin.ID, cred)

	session, err := s.createSession(ctx, admin, ip, ua)
	if err != nil {
		return nil, err
	}
	s.recordAttempt(ctx, username, &admin.ID, ip, ua, model.LoginMethodWebAuthn, true, "ok")
	return session, nil
}

// webauthnRequest 将原始 JSON 响应包装为 http.Request（go-webauthn 通过解析请求体完成校验）。
func webauthnRequest(body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// updateCredentialSignCount 更新 DB 中对应凭据的签名计数（检测克隆）。
func (s *Service) updateCredentialSignCount(ctx context.Context, adminID uint, cred *webauthn.Credential) {
	if cred == nil {
		return
	}
	id := encodeCredentialID(cred.ID)
	stored, err := s.store.GetCredentialByID(ctx, id)
	if err != nil || stored == nil {
		return
	}
	var old webauthn.Credential
	if err := json.Unmarshal(stored.Data, &old); err != nil {
		return
	}
	old.Authenticator.SignCount = cred.Authenticator.SignCount
	data, err := json.Marshal(&old)
	if err != nil {
		return
	}
	stored.Data = data
	now := time.Now()
	stored.LastUsedAt = &now
	_ = s.store.UpdateCredential(ctx, stored)
}

// ListPasskeys 列出某管理员 passkey（脱敏）。
func (s *Service) ListPasskeys(ctx context.Context, adminID uint) ([]PasskeyInfo, error) {
	creds, err := s.store.ListCredentials(ctx, adminID)
	if err != nil {
		return nil, err
	}
	out := make([]PasskeyInfo, 0, len(creds))
	for _, c := range creds {
		if c.Kind != model.CredentialWebAuthn {
			continue
		}
		info := PasskeyInfo{ID: c.CredentialID, CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt}
		var wc webauthn.Credential
		if err := json.Unmarshal(c.Data, &wc); err == nil {
			info.Attestation = wc.AttestationType
			for _, t := range wc.Transport {
				info.Transports = append(info.Transports, string(t))
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// RemovePasskey 删除指定 passkey（高危操作，调用方需已做 step-up）。
func (s *Service) RemovePasskey(ctx context.Context, credentialID string) error {
	cred, err := s.store.GetCredentialByID(ctx, credentialID)
	if err != nil {
		return err
	}
	if cred == nil {
		return nil
	}
	return s.store.DeleteCredential(ctx, cred.ID)
}

// storeWASession 保存挑战会话并返回会话 ID。
func (s *Service) storeWASession(ctx context.Context, admin *model.ManagerAdmin, host string, data *webauthn.SessionData) (string, error) {
	sessionID, err := randomToken(24)
	if err != nil {
		return "", err
	}
	waMu.Lock()
	waSessions[sessionID] = &webauthnSession{
		session:   data,
		username:  admin.Username,
		adminID:   admin.ID,
		host:      host,
		expiresAt: time.Now().Add(waSessionTTL),
	}
	waMu.Unlock()
	return sessionID, nil
}

// takeWASession 取出并删除挑战会话（一次性使用，防重放）。
func (s *Service) takeWASession(sessionID string) *webauthnSession {
	waMu.Lock()
	defer waMu.Unlock()
	entry, ok := waSessions[sessionID]
	if !ok {
		return nil
	}
	delete(waSessions, sessionID)
	if time.Now().After(entry.expiresAt) {
		return nil
	}
	return entry
}

// encodeCredentialID 将凭据 ID 编码为 URL-safe 字符串（DB 唯一标识）。
func encodeCredentialID(id []byte) string {
	return base64.RawURLEncoding.EncodeToString(id)
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
