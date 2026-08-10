package database

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/model"
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

// GetOrCreateUser 按 (platform, platform_user_id) 查找或创建用户（使用 GORM clause.OnConflict 实现幂等 upsert）。
//
// 缓存策略：命中 userCache 直接返回（免查 users 表）；未命中走 DB upsert 后回填缓存。
// 缓存 miss 且 DB 故障时返回错误；缓存命中时返回的 User 仅保证 ID 准确，
// Nickname 取入参（缓存不存昵称，避免与 DB 主数据不一致）。
func (db *DB) GetOrCreateUser(ctx context.Context, platform, platformUserID, nickname string) (*model.User, error) {
	// ── 缓存读：命中直接返回，避免高频消息场景反复 upsert users 表 ──
	if db.userCache != nil {
		if raw, ok, err := db.userCache.Get(ctx, userCacheKey(platform, platformUserID)); err == nil && ok {
			if id, perr := strconv.ParseInt(raw, 10, 64); perr == nil {
				return &model.User{
					ID:             id,
					Platform:       platform,
					PlatformUserID: platformUserID,
					Nickname:       nickname,
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

	// ── 缓存写：回填映射（只存 ID，TTL 过期后自然刷新）──
	if db.userCache != nil {
		_ = db.userCache.Set(ctx, userCacheKey(platform, platformUserID), strconv.FormatInt(u.ID, 10), userCacheTTL)
	}
	return &u, nil
}
