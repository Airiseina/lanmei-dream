package infra

import (
	"context"
	"fmt"
	"log"

	"github.com/DaWesen/lanmei-dream/internal/ai/memory"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/redis/go-redis/v9"
)

// Config 基础设施连接配置
type Config struct {
	DatabaseURL  string
	RedisAddr    string
	EmbeddingDim int
}

// Infra 持有所有基础设施连接，统一生命周期管理
type Infra struct {
	DB         *database.DB
	Redis      *redis.Client
	MemStore   memory.MemoryStore // pgvector 向量记忆存储
	StateStore *RedisStore        // conduit.StateStore 的 Redis 实现
}

// Setup 初始化所有基础设施连接，返回可统一关闭的 Infra 实例
func Setup(ctx context.Context, cfg *Config) (*Infra, error) {
	inf := &Infra{}

	// ── PostgreSQL（含 pgvector 扩展）──
	db, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("连接 PostgreSQL 失败: %w", err)
	}
	inf.DB = db
	log.Println("PostgreSQL 已连接")

	if err := db.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	log.Println("数据库迁移完成")

	// ── pgvector 记忆存储 ──
	inf.MemStore = NewPGVectorStore(db.Orm)
	log.Println("pgvector 记忆存储就绪")

	// ── Redis ──
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("连接 Redis 失败: %w", err)
	}
	inf.Redis = rdb
	inf.StateStore = NewRedisStore(rdb, "conduit")
	log.Println("Redis 已连接")

	return inf, nil
}

// Close 关闭所有基础设施连接
func (inf *Infra) Close() {
	if inf.Redis != nil {
		inf.Redis.Close()
	}
	if inf.DB != nil {
		inf.DB.Close()
	}
}
