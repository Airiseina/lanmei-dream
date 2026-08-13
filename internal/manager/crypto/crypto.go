// Package crypto 提供管理面板的加密与安全原语：
//   - AES-256-GCM 对称加解密（TOTP secret / LLM API Key / passkey 数据加密）
//   - argon2id 密码哈希（登录密码安全存储）
//   - 安全随机 token / 摘要（会话 token、CSRF nonce）
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ─────────────────────────────────────────────
// AES-256-GCM 对称加解密
// ─────────────────────────────────────────────

// keySize AES-256 密钥长度（字节）
const keySize = 32

// Box AES-256-GCM 加解密器。
// 密钥派生规则（LANMEI_MANAGER_SECRET_KEY 环境变量）：
//  1. 若值为 32 字节 base64（std 或 url-safe）→ 直接作为密钥；
//  2. 若值长度 >= 32 → 对其做 SHA-256 派生为 32 字节；
//  3. 否则视为过弱，返回错误（启动期应 Fatal，禁止静默降级）。
type Box struct {
	aead cipher.AEAD
}

// NewBox 从密钥串创建加解密器。
func NewBox(secret string) (*Box, error) {
	key, err := deriveKey(secret)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: init aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: init gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

// deriveKey 按规则派生 32 字节 AES 密钥。
func deriveKey(secret string) ([]byte, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("crypto: 加密主密钥（LANMEI_MANAGER_SECRET_KEY）为空")
	}
	// 规则 1：32 字节 base64
	if decoded, err := base64.StdEncoding.DecodeString(secret); err == nil && len(decoded) == keySize {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(secret); err == nil && len(decoded) == keySize {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(secret); err == nil && len(decoded) == keySize {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(secret); err == nil && len(decoded) == keySize {
		return decoded, nil
	}
	// 规则 2：长度 >= 32 的原始字符串 → SHA-256 派生
	if len(secret) >= keySize {
		sum := sha256.Sum256([]byte(secret))
		return sum[:], nil
	}
	// 规则 3：过弱
	return nil, fmt.Errorf("crypto: 加密主密钥过弱（长度 %d < %d）", len(secret), keySize)
}

// Encrypt 加密明文，返回 nonce||ciphertext。
func (b *Box) Encrypt(plain []byte) ([]byte, error) {
	if b == nil || b.aead == nil {
		return nil, errors.New("crypto: box 未初始化")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: gen nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

// Decrypt 解密 Encrypt 的输出。
func (b *Box) Decrypt(ciphertext []byte) ([]byte, error) {
	if b == nil || b.aead == nil {
		return nil, errors.New("crypto: box 未初始化")
	}
	if len(ciphertext) < b.aead.NonceSize()+b.aead.Overhead() {
		return nil, errors.New("crypto: 密文长度非法")
	}
	nonce := ciphertext[:b.aead.NonceSize()]
	body := ciphertext[b.aead.NonceSize():]
	plain, err := b.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: decrypt: %w", err)
	}
	return plain, nil
}

// ─────────────────────────────────────────────
// argon2id 密码哈希
// ─────────────────────────────────────────────

// argon2 参数（OWASP 推荐级别）
const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword 以 argon2id 派生密码哈希，返回标准编码串：
// $argon2id$v=19$m=65536,t=1,p=4$<salt b64>$<hash b64>
func HashPassword(password string) (string, error) {
	if len(password) == 0 {
		return "", errors.New("crypto: 密码为空")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("crypto: gen salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash))
	return encoded, nil
}

// VerifyPassword 校验密码与哈希是否匹配（恒定时间比较）。
func VerifyPassword(encodedHash, password string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	// 格式：["", "argon2id", "v=19", "m=65536,t=1,p=4", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("crypto: 哈希格式非法")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("crypto: 解析版本: %w", err)
	}
	var m, t, p int
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("crypto: 解析参数: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("crypto: 解码 salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("crypto: 解码哈希: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ─────────────────────────────────────────────
// 随机 token / 摘要
// ─────────────────────────────────────────────

// RandomToken 生成 URL-safe 随机 token（n 字节熵，base64url 编码，无填充）。
func RandomToken(n int) (string, error) {
	b, err := RandomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RandomBytes 生成 n 字节安全随机数。
func RandomBytes(n int) ([]byte, error) {
	if n <= 0 {
		return nil, errors.New("crypto: 随机长度非法")
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("crypto: rand: %w", err)
	}
	return b, nil
}

// SHA256Hex 返回字符串的 SHA-256 十六进制摘要（用于存储 token 的不可逆指纹）。
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
