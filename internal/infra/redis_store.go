package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/zrurf/conduit"
)

// 确保 RedisStore 实现 conduit.StateStore 接口
var _ conduit.StateStore = (*RedisStore)(nil)

// RedisStore 是 conduit.StateStore 的 Redis 实现。
// 提供短期状态存储（带 TTL 自动过期），适用于速率限制、冷却计时等场景。
type RedisStore struct {
	client *redis.Client
	prefix string // 键前缀，避免与其他业务冲突
}

// NewRedisStore 创建基于 Redis 的状态存储
func NewRedisStore(client *redis.Client, prefix string) *RedisStore {
	if prefix == "" {
		prefix = "conduit"
	}
	return &RedisStore{client: client, prefix: prefix}
}

func (s *RedisStore) key(k string) string {
	return s.prefix + ":" + k
}

// Get 获取指定键的值。键不存在或已过期返回空字符串和 nil。
func (s *RedisStore) Get(ctx context.Context, key string) (string, error) {
	val, err := s.client.Get(ctx, s.key(key)).Result()
	if err != nil {
		if err == redis.Nil {
			return "", nil
		}
		return "", fmt.Errorf("redis get: %w", err)
	}
	return val, nil
}

// Set 设置键值对。ttl > 0 设置过期时间，ttl == 0 永不过期。
func (s *RedisStore) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	return s.client.Set(ctx, s.key(key), value, ttl).Err()
}

// Delete 删除指定键。
func (s *RedisStore) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, s.key(key)).Err()
}

// Exists 检查键是否存在且未过期。
func (s *RedisStore) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.client.Exists(ctx, s.key(key)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Close 关闭 Redis 连接
func (s *RedisStore) Close() error {
	return s.client.Close()
}
