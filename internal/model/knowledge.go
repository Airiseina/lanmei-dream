package model

import (
	"time"

	"github.com/pgvector/pgvector-go"
	"gorm.io/datatypes"
)

// KnowledgeChunk 对应 knowledge_chunks 表，存储本地知识库的分块数据。
//
// 设计说明：
//   - 知识库元信息以配置文件为唯一事实来源（不做 IM 管理命令），因此不建 knowledge_bases 表；
//   - 本表仅存储 local provider 的分块（向量 + 倒排索引），飞书等远程 provider 通过各自 API 召回，
//     不落库；
//   - (knowledge_base_id, source_id) 构成唯一键，保证 docs_dir 文件同步与 kb_add 工具写入的幂等性。
type KnowledgeChunk struct {
	ID              int64           `json:"id"                gorm:"primaryKey;autoIncrement;comment:分块ID"`
	KnowledgeBaseID string          `json:"knowledge_base_id" gorm:"size:64;not null;uniqueIndex:uq_kb_source;comment:知识库ID(对应配置bases[].id)"`
	Provider        string          `json:"provider"          gorm:"size:32;not null;comment:provider类型"`
	SourceID        string          `json:"source_id"         gorm:"size:256;not null;uniqueIndex:uq_kb_source;comment:外部源唯一标识(文件相对路径/llm:时间戳:hash)"`
	Title           string          `json:"title"             gorm:"size:256;not null;default:'';comment:标题"`
	Content         string          `json:"content"           gorm:"type:text;not null;comment:内容"`
	Embedding       pgvector.Vector `json:"embedding"         gorm:"type:vector(1024);comment:嵌入向量"`
	Meta            datatypes.JSON  `json:"meta"              gorm:"type:jsonb;not null;default:'{}';comment:元数据(source/tags等)"`
	CreatedAt       time.Time       `json:"created_at"        gorm:"autoCreateTime;index;comment:创建时间"`
	UpdatedAt       time.Time       `json:"updated_at"        gorm:"autoUpdateTime;comment:更新时间"`
}
