package memory

import "context"

// Memory 表示一条长期记忆，用于 RAG 上下文增强。
// GroupID 标识来源群：空 = 个人记忆（私聊/用户画像），非空 = 群级记忆（话题归档，user_id=0）。
type Memory struct {
	ID       string
	UserID   int64
	GroupID  string
	Content  string
	Vector   []float32
	Metadata map[string]any
}

// MemoryStore 抽象记忆的存储与检索。
// PGVectorStore 是其基于 pgvector 的实现。
//
// 群级过滤约定：groupID 为空时仅检索用户个人记忆（group_id=”）；
// groupID 非空时检索该群的群级记忆（group_id=gid）或该用户的个人记忆。
type MemoryStore interface {
	// Store 存储一条记忆（含向量），mem.GroupID 非空时写入群级记忆
	Store(ctx context.Context, mem *Memory) error
	// Retrieve 根据查询向量检索最相关的 N 条记忆（向量召回）
	Retrieve(ctx context.Context, queryVec []float32, userID int64, groupID string, limit int) ([]*Memory, error)
	// RetrieveByKeyword 根据关键词全文搜索检索记忆（关键词召回）
	RetrieveByKeyword(ctx context.Context, query string, userID int64, groupID string, limit int) ([]*Memory, error)
	// RetrieveByTime 根据时间倒序检索最近的 N 条记忆（时间召回）
	RetrieveByTime(ctx context.Context, userID int64, groupID string, limit int) ([]*Memory, error)
	// Delete 删除指定记忆
	Delete(ctx context.Context, id string) error
}
