package infra

import (
	"context"
	"fmt"

	"github.com/DaWesen/lanmei-dream/internal/ai/memory"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Infra 持有所有基础设施连接，统一生命周期管理
type Infra struct {
	DB         *database.DB
	Redis      *redis.Client
	MemStore   memory.MemoryStore // pgvector 向量记忆存储
	StateStore *RedisStore        // conduit.StateStore 的 Redis 实现
	Logger     *zap.Logger
}

// Setup 初始化所有基础设施连接，返回可统一关闭的 Infra 实例。
// embeddingDim 为知识库向量列的目标维度（来自 ai.embedding_dim 配置），透传给数据库迁移。
func Setup(ctx context.Context, dbCfg *config.DatabaseConfig, redisCfg *config.RedisConfig, embeddingDim int, logger *zap.Logger) (*Infra, error) {
	inf := &Infra{Logger: logger}

	// ── PostgreSQL（含 pgvector 扩展）──
	db, err := database.Connect(ctx, dbCfg.URL, logger)
	if err != nil {
		return nil, fmt.Errorf("连接 PostgreSQL 失败: %w", err)
	}
	inf.DB = db
	logger.Info("PostgreSQL 已连接")

	if err := db.Migrate(ctx, embeddingDim); err != nil {
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	logger.Info("数据库迁移完成")

	// ── pgvector 记忆存储 ──
	inf.MemStore = NewPGVectorStore(db.Orm)
	logger.Info("pgvector 记忆存储就绪")

	// ── Redis ──
	rdb := redis.NewClient(&redis.Options{Addr: redisCfg.Addr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("连接 Redis 失败: %w", err)
	}
	inf.Redis = rdb
	inf.StateStore = NewRedisStore(rdb, "conduit")
	logger.Info("Redis 已连接")

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
