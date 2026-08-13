package auth

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/DaWesen/lanmei-dream/internal/model"
)

// tokenIssuer JWT 签发者标识
const tokenIssuer = "lanmei-manager"

// TokenType Access Token 类型
type TokenType string

const (
	TokenTypeAccess TokenType = "access"  // 常规访问
	TokenTypeStepUp TokenType = "step_up" // 高危操作二次认证（短效）
)

// AccessClaims Access Token 声明。
// StepUp 标记该 token 是否经过 step-up 认证（二次身份验证）。
type AccessClaims struct {
	Type     string `json:"typ"`
	Username string `json:"username"`
	Role     string `json:"role"`
	StepUp   bool   `json:"sua,omitempty"`
	jwt.RegisteredClaims
}

// jwtManager 负责 Access Token 的签发与校验（HS256）。
type jwtManager struct {
	secret []byte
	ttl    time.Duration
}

// newJWTManager 创建 JWT 管理器，密钥为 LANMEI_MANAGER_SECRET_KEY 的 SHA-256 派生。
func newJWTManager(secret string, ttl time.Duration) *jwtManager {
	sum := sha256.Sum256([]byte(secret))
	return &jwtManager{secret: sum[:], ttl: ttl}
}

// Issue 为管理员签发 Access Token。
// stepUp 为 true 时签发 step-up 短效 token（用于高危操作二次认证）。
func (m *jwtManager) Issue(admin *model.ManagerAdmin, stepUp bool) (string, error) {
	typ := string(TokenTypeAccess)
	ttl := m.ttl
	if stepUp {
		typ = string(TokenTypeStepUp)
		ttl = stepUpTTL
	}
	now := time.Now()
	claims := AccessClaims{
		Type:     typ,
		Username: admin.Username,
		Role:     string(admin.Role),
		StepUp:   stepUp,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			Subject:   strconv.FormatUint(uint64(admin.ID), 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        randomJTI(),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", fmt.Errorf("auth: sign access token: %w", err)
	}
	return signed, nil
}

// Parse 校验并解析 Access Token。
func (m *jwtManager) Parse(tokenStr string) (*AccessClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &AccessClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("auth: 签名算法非法")
		}
		return m.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithExpirationRequired())
	if err != nil {
		return nil, fmt.Errorf("auth: 解析 access token: %w", err)
	}
	claims, ok := token.Claims.(*AccessClaims)
	if !ok || !token.Valid {
		return nil, errors.New("auth: access token 无效")
	}
	return claims, nil
}

// randomJTI 生成随机 JWT ID（防重放）。
func randomJTI() string {
	s, err := randomToken(12)
	if err != nil {
		// crypto/rand 失败属极端情况，用时间戳兜底（保证唯一性）
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return s
}
