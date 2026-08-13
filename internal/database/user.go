package database

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// userCacheTTL 用户映射缓存有效期。昵称允许过期刷新，ID 为主键长期稳定。
const userCacheTTL = time.Hour

// UserCache 用户映射缓存（防数据库击穿）。
// 高频消息场景下每条消息都会触发 GetOrCreateUser（角色扮演/话题/命令等），
// 加一层缓存避免重复 upsert users 表。nil 时禁用缓存直查数据库。
// 实现方需保证并发安全；Get 返回 ok=false 表示未命中（非错误）。
type UserCache interface {
	Get(ctx context.Context, key string) (value string, ok bool, err error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
}

// userCacheKey 生成用户映射缓存键。
func userCacheKey(platform, platformUserID string) string {
	return "user:" + platform + ":" + platformUserID
}

// userCacheValue 编码缓存值：<id>|<banned(0/1)>（旧格式为纯 ID，兼容读取）。
func userCacheValue(id int64, banned bool) string {
	flag := "0"
	if banned {
		flag = "1"
	}
	return strconv.FormatInt(id, 10) + "|" + flag
}

// parseUserCacheValue 解析缓存值，返回 id 与封禁标记（旧格式纯 ID 视为未封禁）。
func parseUserCacheValue(raw string) (int64, bool, bool) {
	idStr := raw
	banned := false
	if i := strings.IndexByte(raw, '|'); i >= 0 {
		idStr = raw[:i]
		banned = raw[i+1:] == "1"
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, false, false
	}
	return id, banned, true
}

// GetOrCreateUser 按 (platform, platform_user_id) 查找或创建用户（使用 GORM clause.OnConflict 实现幂等 upsert）。
//
// 缓存策略：命中 userCache 直接返回（免查 users 表）；未命中走 DB upsert 后回填缓存。
// 缓存 miss 且 DB 故障时返回错误；缓存命中时返回的 User 仅保证 ID 与封禁标记准确，
// Nickname 取入参（缓存不存昵称，避免与 DB 主数据不一致）。
func (db *DB) GetOrCreateUser(ctx context.Context, platform, platformUserID, nickname string) (*model.User, error) {
	// ── 缓存读：命中直接返回，避免高频消息场景反复 upsert users 表 ──
	if db.userCache != nil {
		if raw, ok, err := db.userCache.Get(ctx, userCacheKey(platform, platformUserID)); err == nil && ok {
			if id, banned, ok := parseUserCacheValue(raw); ok {
				return &model.User{
					ID:             id,
					Platform:       platform,
					PlatformUserID: platformUserID,
					Nickname:       nickname,
					BannedAt:       bannedAtIf(banned),
				}, nil
			}
		}
	}

	var u model.User
	result := db.Orm.WithContext(ctx).
		Where(model.User{Platform: platform, PlatformUserID: platformUserID}).
		Attrs(model.User{Nickname: nickname}).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "platform"}, {Name: "platform_user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"nickname", "updated_at"}),
		}).
		FirstOrCreate(&u)
	if result.Error != nil {
		return nil, fmt.Errorf("get_or_create_user: %w", result.Error)
	}

	// ── 缓存写：回填映射（只存 ID + 封禁标记，TTL 过期后自然刷新）──
	if db.userCache != nil {
		_ = db.userCache.Set(ctx, userCacheKey(platform, platformUserID), userCacheValue(u.ID, u.BannedAt != nil), userCacheTTL)
	}
	return &u, nil
}

// bannedAtIf 根据布尔标记返回封禁时间（标记为 true 时返回非零时间，便于上层判断）。
func bannedAtIf(banned bool) *time.Time {
	if !banned {
		return nil
	}
	t := time.Now()
	return &t
}

// IsUserBanned 判断用户是否被封禁：缓存优先，未命中查库后回填。
func (db *DB) IsUserBanned(ctx context.Context, platform, platformUserID string) (bool, error) {
	key := userCacheKey(platform, platformUserID)
	if db.userCache != nil {
		if raw, ok, err := db.userCache.Get(ctx, key); err == nil && ok {
			if _, banned, ok := parseUserCacheValue(raw); ok {
				return banned, nil
			}
		}
	}
	var u model.User
	if err := db.Orm.WithContext(ctx).
		Where(model.User{Platform: platform, PlatformUserID: platformUserID}).
		First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("is_user_banned: %w", err)
	}
	if db.userCache != nil {
		_ = db.userCache.Set(ctx, key, userCacheValue(u.ID, u.BannedAt != nil), userCacheTTL)
	}
	return u.BannedAt != nil, nil
}

// SetUserBanned 设置/解除用户封禁并同步刷新用户映射缓存（管理面板调用）。
// banned=true 时写入当前时间与原因；banned=false 时清除封禁。
func (db *DB) SetUserBanned(ctx context.Context, platform, platformUserID string, banned bool, reason string) error {
	updates := map[string]any{"ban_reason": reason}
	if banned {
		updates["banned_at"] = time.Now()
	} else {
		updates["banned_at"] = nil
	}
	if err := db.Orm.WithContext(ctx).
		Model(&model.User{}).
		Where(model.User{Platform: platform, PlatformUserID: platformUserID}).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("set_user_banned: %w", err)
	}
	// 同步缓存，使封禁立即生效（不依赖 TTL 过期）
	if db.userCache != nil {
		var u model.User
		if err := db.Orm.WithContext(ctx).
			Where(model.User{Platform: platform, PlatformUserID: platformUserID}).
			First(&u).Error; err == nil {
			_ = db.userCache.Set(ctx, userCacheKey(platform, platformUserID), userCacheValue(u.ID, banned), userCacheTTL)
		}
	}
	return nil
}
