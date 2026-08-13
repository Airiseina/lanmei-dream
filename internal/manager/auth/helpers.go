package auth

import (
	"strings"
	"sync"

	"github.com/DaWesen/lanmei-dream/internal/manager/crypto"
)

// 认证服务内共享锁（保护内存态 TOTP 挂起会话与 step-up 记录）。
var (
	pendingMu sync.Mutex
	stepUpMu  sync.Mutex
)

// muPending 加锁 TOTP 挂起会话表。
func (s *Service) muPending() { pendingMu.Lock() }

// unPending 解锁 TOTP 挂起会话表。
func (s *Service) unPending() { pendingMu.Unlock() }

// muStepUp 加锁 step-up 记录表。
func (s *Service) muStepUp() { stepUpMu.Lock() }

// unStepUp 解锁 step-up 记录表。
func (s *Service) unStepUp() { stepUpMu.Unlock() }

// randomToken 生成 URL-safe 随机 token。
func randomToken(n int) (string, error) { return crypto.RandomToken(n) }

// summarizeUA 从 User-Agent 提取简短设备名（前端展示用）。
func summarizeUA(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return "未知设备"
	}
	// 取第一个以空格分隔的 token（通常是主产品标识）
	if i := strings.IndexAny(ua, " ("); i > 0 {
		return ua[:i]
	}
	if len(ua) > 32 {
		return ua[:32]
	}
	return ua
}
