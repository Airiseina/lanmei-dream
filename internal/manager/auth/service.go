// Package auth 实现管理面板认证：env 超管引导、密码登录（argon2id + TOTP）、
// WebAuthn passkey、双 Token 会话（轮换 + 复用检测）、step-up 二次认证。
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/manager/crypto"
	"github.com/DaWesen/lanmei-dream/internal/manager/store"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// stepUpTTL step-up token 有效期（高危操作二次认证后）。
const stepUpTTL = 5 * time.Minute

// totpPendingTTL 密码验证通过后等待 TOTP 确认的时间窗口。
const totpPendingTTL = 5 * time.Minute

// 认证流程错误（对外统一返回泛化错误，避免账号枚举）
var (
	ErrInvalidCredentials  = errors.New("auth: 用户名或密码错误")
	ErrAccountDisabled     = errors.New("auth: 账号已禁用")
	ErrAccountLocked       = errors.New("auth: 登录失败次数过多，账号已临时锁定")
	ErrTOTPRequired        = errors.New("auth: 需要 TOTP 二次验证")
	ErrTOTPInvalid         = errors.New("auth: TOTP 验证码错误")
	ErrSessionInvalid      = errors.New("auth: 会话无效或已过期")
	ErrSessionReused       = errors.New("auth: 检测到会话复用，已吊销全部会话")
	ErrStepUpRequired      = errors.New("auth: 需要二次身份验证")
	ErrWebAuthnUnavailable = errors.New("auth: passkey 不可用（需要 HTTPS 域名环境）")
)

// Config 认证服务配置（来自 manager 配置节）。
type Config struct {
	AccessTokenTTL      time.Duration // 短期 Access Token 有效期
	RefreshTokenTTL     time.Duration // 长期 Refresh Token 有效期
	MaxSessionsPerUser  int           // 单账号最大活跃会话数
	MaxLoginFails       int           // 连续失败锁定阈值
	LoginLockWindow     time.Duration // 失败统计窗口（同时作为锁定时长）
	EnableWebAuthn      bool
	WebAuthnRPID        string // 显式 RPID（域名）；空则按请求 Host 推断
	WebAuthnDisplayName string
	WebAuthnOrigins     []string
	SuperAdminUsername  string // env 超管（bootstrap 用，不落库明文）
	SuperAdminPassword  string // env 超管密码
	SecretKey           string // 加密主密钥
}

// SessionResult 登录/刷新成功后的会话结果。
type SessionResult struct {
	AdminID         uint   `json:"admin_id"`
	Username        string `json:"username"`
	Role            string `json:"role"`
	DisplayName     string `json:"display_name"`
	AccessToken     string `json:"access_token"`
	RefreshToken    string `json:"refresh_token"`
	AccessExpiresIn int64  `json:"access_expires_in"` // 秒
	HasPasskey      bool   `json:"has_passkey"`
	HasTOTP         bool   `json:"has_totp"`
}

// PendingTOTP 密码通过但需要 TOTP 时的挂起状态。
type PendingTOTP struct {
	Token string `json:"token"`
	TTL   int64  `json:"ttl"` // 秒
}

// Service 认证服务。
type Service struct {
	store  *store.Store
	box    *crypto.Box
	jwt    *jwtManager
	cfg    *Config
	logger *zap.Logger

	// TOTP 挂起会话：token → adminID（内存态，单进程）
	totpPending map[string]totpPendingEntry
	// step-up 会话记录：token → 管理员 + 有效期（防跨账号组合）
	stepUp map[string]stepUpEntry
}

type totpPendingEntry struct {
	adminID   uint
	expiresAt time.Time
}

type stepUpEntry struct {
	adminID   uint
	expiresAt time.Time
}

// New 创建认证服务。
// 密钥校验失败（主密钥过弱）直接返回错误，由调用方 Fatal。
func New(s *store.Store, cfg *Config, logger *zap.Logger) (*Service, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	box, err := crypto.NewBox(cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	if cfg.MaxSessionsPerUser <= 0 {
		cfg.MaxSessionsPerUser = 10
	}
	if cfg.MaxLoginFails <= 0 {
		cfg.MaxLoginFails = 5
	}
	if cfg.LoginLockWindow <= 0 {
		cfg.LoginLockWindow = 15 * time.Minute
	}
	if cfg.AccessTokenTTL <= 0 {
		cfg.AccessTokenTTL = 15 * time.Minute
	}
	if cfg.RefreshTokenTTL <= 0 {
		cfg.RefreshTokenTTL = 7 * 24 * time.Hour
	}
	return &Service{
		store:       s,
		box:         box,
		jwt:         newJWTManager(cfg.SecretKey, cfg.AccessTokenTTL),
		cfg:         cfg,
		logger:      logger,
		totpPending: make(map[string]totpPendingEntry),
		stepUp:      make(map[string]stepUpEntry),
	}, nil
}

// Box 暴露加密盒（供 LLM API Key 等加密）。
func (s *Service) Box() *crypto.Box { return s.box }

// GetAdmin 按 ID 查询管理员（middleware/handler 便捷方法）。
func (s *Service) GetAdmin(ctx context.Context, id uint) (*model.ManagerAdmin, error) {
	return s.store.GetAdminByID(ctx, id)
}

// GetAdminByUsername 按用户名查询管理员。
func (s *Service) GetAdminByUsername(ctx context.Context, username string) (*model.ManagerAdmin, error) {
	return s.store.GetAdminByUsername(ctx, username)
}

// ParseAccessToken 解析并校验 Access Token（middleware 用）。
func (s *Service) ParseAccessToken(token string) (*AccessClaims, error) {
	return s.jwt.Parse(token)
}

// ValidateStepUp 校验 step-up token 与当前登录身份绑定（防跨账号组合）。
func (s *Service) ValidateStepUp(token string, adminID uint) bool {
	s.muStepUp()
	entry, ok := s.stepUp[token]
	s.unStepUp()
	if !ok || entry.adminID != adminID {
		return false
	}
	if time.Now().After(entry.expiresAt) {
		s.muStepUp()
		delete(s.stepUp, token)
		s.unStepUp()
		return false
	}
	return true
}

// Bootstrap 环境变量超级管理员引导（启动时调用一次）。
// 规则见实施文档 §4.3：
//   - 不存在 → 创建 super_admin（auth_source=env，argon2id 哈希）
//   - 存在且 auth_source=env → 用 env 重新派生哈希（改 env 重启即生效）
//   - 存在且 auth_source=db → 不覆盖（面板内改过密码）
//
// 明文密码全程不落库、不落日志。
func (s *Service) Bootstrap(ctx context.Context) error {
	username := s.cfg.SuperAdminUsername
	password := s.cfg.SuperAdminPassword
	if username == "" {
		return errors.New("auth: 环境变量 LANMEI_MANAGER_ADMIN_USERNAME 未配置")
	}
	if len(password) < 8 {
		return errors.New("auth: 环境变量 LANMEI_MANAGER_ADMIN_PASSWORD 缺失或过弱（要求至少 8 位）")
	}
	if s.cfg.SecretKey == "" {
		return errors.New("auth: 环境变量 LANMEI_MANAGER_SECRET_KEY 未配置（凭据加密主密钥）")
	}

	hash, err := crypto.HashPassword(password)
	if err != nil {
		return fmt.Errorf("auth: bootstrap hash: %w", err)
	}

	existing, err := s.store.GetAdminByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("auth: bootstrap lookup: %w", err)
	}

	switch {
	case existing == nil:
		admin := &model.ManagerAdmin{
			Username:     username,
			PasswordHash: hash,
			Role:         model.AdminRoleSuper,
			Status:       model.AdminStatusActive,
			AuthSource:   model.AuthSourceEnv,
			DisplayName:  "超级管理员",
		}
		if err := s.store.CreateAdmin(ctx, admin); err != nil {
			return fmt.Errorf("auth: bootstrap create: %w", err)
		}
		s.logger.Info("auth: 超级管理员引导完成（新建）", zap.String("username", username))
	case existing.AuthSource == model.AuthSourceEnv && existing.Role == model.AdminRoleSuper:
		existing.PasswordHash = hash
		if err := s.store.UpdateAdmin(ctx, existing); err != nil {
			return fmt.Errorf("auth: bootstrap update: %w", err)
		}
		s.logger.Info("auth: 超级管理员引导完成（env 哈希已刷新）", zap.String("username", username))
	default:
		s.logger.Info("auth: 跳过 env 覆盖（超级管理员凭据已由面板管理）", zap.String("username", username))
	}
	return nil
}
