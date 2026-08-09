package model

import (
	"time"

	"github.com/pgvector/pgvector-go"
)

// MemoryVector 对应 memory_vectors 表，使用 pgvector 存储记忆向量
type MemoryVector struct {
	ID        int64           `json:"id"          gorm:"primaryKey;autoIncrement;comment:记忆ID"`
	UserID    int64           `json:"user_id"     gorm:"index;not null;comment:用户ID(群级记忆为0)"`
	GroupID   string          `json:"group_id"    gorm:"size:64;index:idx_mv_group_created;default:'';comment:来源群(空=个人记忆)"`
	Content   string          `json:"content"     gorm:"type:text;not null;comment:记忆文本"`
	Embedding pgvector.Vector `json:"embedding"   gorm:"type:vector(1024);not null;comment:嵌入向量"`
	SearchVec string          `json:"search_vec"  gorm:"type:tsvector;comment:全文搜索向量"`
	CreatedAt time.Time       `json:"created_at"  gorm:"autoCreateTime;comment:创建时间"`
}
