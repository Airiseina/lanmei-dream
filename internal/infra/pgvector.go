package infra

import (
	"context"
	"fmt"
	"strconv"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/DaWesen/lanmei-dream/internal/ai/memory"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// 确保 PGVectorStore 实现 memory.MemoryStore 接口
var _ memory.MemoryStore = (*PGVectorStore)(nil)

// PGVectorStore 基于 PostgreSQL + pgvector 的 MemoryStore 实现
type PGVectorStore struct {
	orm *gorm.DB
}

// NewPGVectorStore 创建基于 pgvector 的记忆存储
func NewPGVectorStore(db *gorm.DB) *PGVectorStore {
	return &PGVectorStore{orm: db}
}

// Store 存储一条记忆（含向量）
func (s *PGVectorStore) Store(ctx context.Context, mem *memory.Memory) error {
	row := &model.MemoryVector{
		UserID:    mem.UserID,
		Content:   mem.Content,
		Embedding: pgvector.NewVector(mem.Vector),
	}
	return s.orm.WithContext(ctx).Create(row).Error
}

// Retrieve 根据查询向量检索最相关的 N 条记忆
func (s *PGVectorStore) Retrieve(ctx context.Context, queryVec []float32, userID int64, limit int) ([]*memory.Memory, error) {
	vecStr := formatVector(queryVec)

	var rows []model.MemoryVector
	err := s.orm.WithContext(ctx).
		Where("user_id = ?", userID).
		Order(fmt.Sprintf("embedding <=> %s", vecStr)).
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("pgvector retrieve: %w", err)
	}

	memories := make([]*memory.Memory, len(rows))
	for i, row := range rows {
		memories[i] = &memory.Memory{
			ID:      strconv.FormatInt(row.ID, 10),
			UserID:  row.UserID,
			Content: row.Content,
			Vector:  row.Embedding.Slice(),
		}
	}
	return memories, nil
}

// Delete 删除指定 ID 的记忆
func (s *PGVectorStore) Delete(ctx context.Context, id string) error {
	pk, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid id %q: %w", id, err)
	}
	return s.orm.WithContext(ctx).Delete(&model.MemoryVector{}, pk).Error
}

// formatVector 将 float32 切片格式化为 SQL 向量字面量 '[0.1,0.2,...]'
func formatVector(vec []float32) string {
	s := "["
	for i, v := range vec {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%f", v)
	}
	s += "]"
	return s
}
