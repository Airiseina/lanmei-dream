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

	return rowsToMemories(rows), nil
}

// Delete 删除指定 ID 的记忆
func (s *PGVectorStore) Delete(ctx context.Context, id string) error {
	pk, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid id %q: %w", id, err)
	}
	return s.orm.WithContext(ctx).Delete(&model.MemoryVector{}, pk).Error
}

// RetrieveByKeyword 根据关键词全文搜索检索记忆（关键词召回）
// 使用 PostgreSQL tsvector 全文搜索，simple 配置按空白切割适合中文
func (s *PGVectorStore) RetrieveByKeyword(ctx context.Context, query string, userID int64, limit int) ([]*memory.Memory, error) {
	// 将查询文本转换为 tsquery：按空白分割，用 & (AND) 连接
	tsQuery := toSimpleTSQuery(query)
	if tsQuery == "" {
		return nil, nil
	}

	var rows []model.MemoryVector
	err := s.orm.WithContext(ctx).Raw(
		"SELECT * FROM memory_vectors WHERE user_id = ? AND search_vec @@ to_tsquery('simple', ?) ORDER BY ts_rank(search_vec, to_tsquery('simple', ?)) DESC LIMIT ?",
		userID, tsQuery, tsQuery, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("pgvector keyword retrieve: %w", err)
	}

	return rowsToMemories(rows), nil
}

// RetrieveByTime 根据时间倒序检索最近的 N 条记忆（时间召回）
func (s *PGVectorStore) RetrieveByTime(ctx context.Context, userID int64, limit int) ([]*memory.Memory, error) {
	var rows []model.MemoryVector
	err := s.orm.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("pgvector time retrieve: %w", err)
	}

	return rowsToMemories(rows), nil
}

// toSimpleTSQuery 将自然语言查询转为 simple 配置的 tsquery
// 按 Unicode 空白分割，用 & (AND) 连接各词项
func toSimpleTSQuery(query string) string {
	var parts []string
	for _, w := range splitWhitespace(query) {
		if w != "" {
			parts = append(parts, w+"&")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += " & " + parts[i]
	}
	return result
}

// splitWhitespace 按空白字符分割字符串（模拟 strings.Fields 但返回可遍历切片）
func splitWhitespace(s string) []string {
	var fields []string
	var buf []rune
	for _, r := range s {
		if isWhitespace(r) {
			if len(buf) > 0 {
				fields = append(fields, string(buf))
				buf = buf[:0]
			}
		} else {
			buf = append(buf, r)
		}
	}
	if len(buf) > 0 {
		fields = append(fields, string(buf))
	}
	return fields
}

func isWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// rowsToMemories 将数据库行转换为 Memory 切片
func rowsToMemories(rows []model.MemoryVector) []*memory.Memory {
	memories := make([]*memory.Memory, len(rows))
	for i, row := range rows {
		memories[i] = &memory.Memory{
			ID:      strconv.FormatInt(row.ID, 10),
			UserID:  row.UserID,
			Content: row.Content,
			Vector:  row.Embedding.Slice(),
		}
	}
	return memories
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
