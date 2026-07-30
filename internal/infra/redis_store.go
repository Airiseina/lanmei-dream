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

// CompareAndSwap 原子性地比较并交换值。仅当当前值等于 oldValue 时才设置为 newValue。
// 返回 true 表示交换成功，false 表示当前值不匹配。
// ttl > 0 设置过期时间，ttl == 0 永不过期。
func (s *RedisStore) CompareAndSwap(ctx context.Context, key, oldValue, newValue string, ttl time.Duration) (bool, error) {
	k := s.key(key)
	var ttlMs int64
	if ttl > 0 {
		ttlMs = ttl.Milliseconds()
	}
	// Lua 脚本保证原子性
	script := redis.NewScript(`
		local current = redis.call('GET', KEYS[1])
		if current == ARGV[1] then
			if ARGV[3] == '0' then
				redis.call('SET', KEYS[1], ARGV[2])
			else
				redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
			end
			return 1
		end
		return 0
	`)
	result, err := script.Run(ctx, s.client, []string{k}, oldValue, newValue, fmt.Sprintf("%d", ttlMs)).Int64()
	if err != nil {
		return false, fmt.Errorf("redis cas: %w", err)
	}
	return result == 1, nil
}

// IncrBy 原子性地增加指定键的整数值。键不存在时视为 0。
func (s *RedisStore) IncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	result, err := s.client.IncrBy(ctx, s.key(key), delta).Result()
	if err != nil {
		return 0, fmt.Errorf("redis incrby: %w", err)
	}
	return result, nil
}

// SetIfNotExists 仅当键不存在时设置值（SETNX）。
// 返回 true 表示设置成功，false 表示键已存在。
func (s *RedisStore) SetIfNotExists(ctx context.Context, key string, value string, ttl time.Duration) (bool, error) {
	k := s.key(key)
	var result bool
	var err error
	if ttl > 0 {
		result, err = s.client.SetNX(ctx, k, value, ttl).Result()
	} else {
		result, err = s.client.SetNX(ctx, k, value, 0).Result()
	}
	if err != nil {
		return false, fmt.Errorf("redis setnx: %w", err)
	}
	return result, nil
}

// Close 实现 conduit.StateStore 接口。
// 注意：不关闭底层 Redis 连接，由 Infra 统一管理生命周期。
func (s *RedisStore) Close() error {
	return nil
}
