package infra

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// userCacheTTL 用户映射缓存有效期（与 database.UserCache 的语义配合，ID 长期稳定）。
const userCacheTTL = time.Hour

// userCacheRedis 基于 Redis 的 database.UserCache 实现。
// 缓存键为 user:<platform>:<platform_user_id>，值为 users 表主键 ID 的十进制字符串。
type userCacheRedis struct {
	client *redis.Client
	ttl    time.Duration
}

// Get 读取缓存；redis.Nil（键不存在）视为未命中而非错误。
func (c *userCacheRedis) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := c.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Set 写入缓存。
func (c *userCacheRedis) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}
