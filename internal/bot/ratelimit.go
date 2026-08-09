package bot

import (
	"context"
	"time"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/config"
)

// ratelimitWindow 限流窗口长度（1 分钟），与配置项"每分钟上限"对应。
const ratelimitWindow = time.Minute

// ReplyLimiter Bot 回复限流器：按维度分别计数限流，防止 Bot 刷屏与 LLM 成本失控。
//
// 维度（配置见 [bot.ratelimit]）：
//   - 每群回复：reply_per_group_per_min 条/分钟
//   - 每用户 LLM 对话：llm_per_user_per_min 条/分钟
//   - 全局回复：reply_total_per_min 条/分钟
//
// 实现：Redis 计数器（SetIfNotExists 初始化 TTL + IncrBy 原子递增）。
// 首次计数时创建带 TTL 的键，窗口过期后自动归零。
type ReplyLimiter struct {
	store  conduit.StateStore
	cfg    *config.RateLimitConfig
	logger *zap.Logger
}

// NewReplyLimiter 创建回复限流器。
func NewReplyLimiter(store conduit.StateStore, cfg *config.RateLimitConfig, logger *zap.Logger) *ReplyLimiter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ReplyLimiter{store: store, cfg: cfg, logger: logger}
}

// countAndCheck 对指定键做一次计数并判断是否超限。
// limit <= 0 表示不限流。
func (l *ReplyLimiter) countAndCheck(ctx context.Context, key string, limit int) bool {
	if l.store == nil || limit <= 0 {
		return true
	}
	// 首次计数前初始化带 TTL 的计数器（窗口过期自动归零）
	if _, err := l.store.SetIfNotExists(ctx, key, "0", ratelimitWindow); err != nil {
		l.logger.Warn("ratelimit: 初始化计数器失败，放行", zap.String("key", key), zap.Error(err))
		return true
	}
	n, err := l.store.IncrBy(ctx, key, 1)
	if err != nil {
		l.logger.Warn("ratelimit: 计数失败，放行", zap.String("key", key), zap.Error(err))
		return true
	}
	return n <= int64(limit)
}

// AllowGroupReply 判断群是否还有回复额度（返回 true 表示允许回复）。
func (l *ReplyLimiter) AllowGroupReply(ctx context.Context, groupID string) bool {
	if groupID == "" {
		return true // 私聊不按群限流
	}
	return l.countAndCheck(ctx, conduit.MakeStoreKey("ratelimit", "reply", "group", groupID), l.cfg.ReplyPerGroupPerMin)
}

// AllowUserLLM 判断用户是否还有 LLM 对话额度。
func (l *ReplyLimiter) AllowUserLLM(ctx context.Context, userID string) bool {
	if userID == "" {
		return true
	}
	return l.countAndCheck(ctx, conduit.MakeStoreKey("ratelimit", "llm", "user", userID), l.cfg.LLMPerUserPerMin)
}

// AllowTotalReply 判断全局是否还有回复额度。
func (l *ReplyLimiter) AllowTotalReply(ctx context.Context) bool {
	return l.countAndCheck(ctx, conduit.MakeStoreKey("ratelimit", "reply", "total"), l.cfg.ReplyTotalPerMin)
}

// ─── 事件防刷 / 幂等框架工具（供插件子树复用）───

// EventOnce 事件幂等：同一 (kind, groupID, userID) 在 TTL 内只允许首次通过。
// 用于"进群欢迎 24h 内只欢迎一次"等场景。
// 返回 true 表示首次（应执行），false 表示窗口内已执行过。
func EventOnce(store conduit.StateStore, kind, groupID, userID string, ttl time.Duration) (bool, error) {
	if store == nil {
		return true, nil // 无状态存储时视为首次
	}
	return store.SetIfNotExists(context.Background(),
		conduit.MakeStoreKey("event", "once", kind, groupID, userID), "1", ttl)
}

// EventCooldown 事件冷却计数：同一 (kind, userID) 在 TTL 窗口内最多放行 max 次。
// 用于连续 poke / 刷屏事件的限频。
// 返回 true 表示在配额内（应执行），false 表示已超频。
func EventCooldown(store conduit.StateStore, kind, userID string, max int, ttl time.Duration) (bool, error) {
	if store == nil || max <= 0 {
		return true, nil
	}
	key := conduit.MakeStoreKey("event", "cooldown", kind, userID)
	if _, err := store.SetIfNotExists(context.Background(), key, "0", ttl); err != nil {
		return false, err
	}
	n, err := store.IncrBy(context.Background(), key, 1)
	if err != nil {
		return false, err
	}
	return n <= int64(max), nil
}
