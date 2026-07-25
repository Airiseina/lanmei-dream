package model

import (
	"time"

	"github.com/pgvector/pgvector-go"
)

// MemoryVector 对应 memory_vectors 表，使用 pgvector 存储记忆向量
type MemoryVector struct {
	ID        int64           `json:"id"         gorm:"primaryKey;autoIncrement;comment:记忆ID"`
	UserID    int64           `json:"user_id"    gorm:"index;not null;comment:用户ID"`
	Content   string          `json:"content"    gorm:"type:text;not null;comment:记忆文本"`
	Embedding pgvector.Vector `json:"embedding"  gorm:"type:vector(1024);not null;comment:嵌入向量"`
	CreatedAt time.Time       `json:"created_at" gorm:"autoCreateTime;comment:创建时间"`
}
