package infra

import (
	"context"
	"fmt"

	"github.com/DaWesen/lanmei-dream/internal/ai/memory"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/media"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Infra 持有所有基础设施连接，统一生命周期管理
type Infra struct {
	DB          *database.DB
	Redis       *redis.Client
	MemStore    memory.MemoryStore // pgvector 向量记忆存储
	StateStore  *RedisStore        // conduit.StateStore 的 Redis 实现
	ObjectStore *media.ObjectStore // RustFS 多媒体对象存储（未配置时为 nil）
	Logger      *zap.Logger
}

// Setup 初始化所有基础设施连接，返回可统一关闭的 Infra 实例。
// embeddingDim 为知识库向量列的目标维度（来自 ai.embedding_dim 配置），透传给数据库迁移。
// mediaCfg 为空或 endpoint 为空时跳过 RustFS 初始化（媒体缓存存储不可用，调用方需降级）。
// 任一步失败立即返回错误、不返回部分初始化的实例；失败前已建立的连接不做回收，由进程退出兜底。
func Setup(ctx context.Context, dbCfg *config.DatabaseConfig, redisCfg *config.RedisConfig, mediaCfg *config.MediaConfig, embeddingDim int, logger *zap.Logger) (*Infra, error) {
	inf := &Infra{Logger: logger}

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

	inf.MemStore = NewPGVectorStore(db.Orm)
	logger.Info("pgvector 记忆存储就绪")

	rdb := redis.NewClient(&redis.Options{Addr: redisCfg.Addr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("连接 Redis 失败: %w", err)
	}
	inf.Redis = rdb
	inf.StateStore = NewRedisStore(rdb, "conduit")
	// 用户映射缓存：高频消息下每条都会 GetOrCreateUser（角色扮演/话题/命令），
	// 命中缓存可免查 users 表，缓解数据库压力
	db.SetUserCache(&userCacheRedis{client: rdb, ttl: userCacheTTL})
	logger.Info("Redis 已连接", zap.Bool("user_cache", true))

	if mediaCfg != nil && mediaCfg.Endpoint != "" {
		store, err := media.NewObjectStore(
			mediaCfg.Endpoint, mediaCfg.AccessKey, mediaCfg.SecretKey,
			mediaCfg.Bucket, mediaCfg.Region,
		)
		if err != nil {
			return nil, fmt.Errorf("初始化 RustFS 对象存储失败: %w", err)
		}
		inf.ObjectStore = store
		logger.Info("RustFS 对象存储就绪",
			zap.String("endpoint", mediaCfg.Endpoint),
			zap.String("bucket", mediaCfg.Bucket))
	} else {
		logger.Warn("未配置 RustFS 端点（bot.media.endpoint），多媒体缓存存储不可用")
	}

	return inf, nil
}

// Close 关闭 Setup 建立的 Redis 与数据库连接（Redis 先关）。
// 对应组件为 nil 时跳过；MemoryStore、StateStore 复用这两条连接，无需单独关闭。
func (inf *Infra) Close() {
	if inf.Redis != nil {
		inf.Redis.Close()
	}
	if inf.DB != nil {
		inf.DB.Close()
	}
}
