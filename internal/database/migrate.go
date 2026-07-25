package database

import (
	"context"
	"fmt"
	"log"

	"github.com/DaWesen/lanmei-dream/internal/model"
)

// Migrate 使用 GORM AutoMigrate 自动建表（幂等），并确保 pgvector 扩展和索引就绪
func (db *DB) Migrate(ctx context.Context) error {
	// 启用 pgvector 扩展
	if err := db.Orm.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return fmt.Errorf("enable pgvector: %w", err)
	}
	log.Println("pgvector 扩展已启用")

	if err := db.Orm.WithContext(ctx).AutoMigrate(
		&model.User{},
		&model.Conversation{},
		&model.Memory{},
		&model.EpisodeSummary{},
		&model.TopicCluster{},
		&model.MemoryVector{},
	); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// 创建 HNSW 索引（幂等，已存在则跳过）
	db.Orm.WithContext(ctx).Exec(
		"CREATE INDEX IF NOT EXISTS idx_memory_vectors_embedding ON memory_vectors USING hnsw (embedding vector_cosine_ops)")
	log.Println("HNSW 向量索引已就绪")

	return nil
}
