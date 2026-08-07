package memory

import (
	"context"
	"sort"
)

// RecallWeight 定义各召回通路的基础权重
type RecallWeight struct {
	Vector  float64 // 向量召回权重
	Keyword float64 // 关键词召回权重
	Time    float64 // 时间召回权重
}

// DefaultRecallWeight 默认权重配置：向量最高，关键词次之，时间最低
var DefaultRecallWeight = RecallWeight{
	Vector:  1.0,
	Keyword: 0.8,
	Time:    0.5,
}

// ScoredMemory 带分数的记忆，用于多路召回合并排序
type ScoredMemory struct {
	*Memory
	Score float64
}

// MultiRetriever 多路召回合并器
// 从向量、关键词、时间三条通路检索记忆，去重后按加权分数排序
type MultiRetriever struct {
	store   MemoryStore
	weights RecallWeight
}

// NewMultiRetriever 创建多路召回合并器
func NewMultiRetriever(store MemoryStore, weights RecallWeight) *MultiRetriever {
	return &MultiRetriever{store: store, weights: weights}
}

// Retrieve 执行多路召回并合并结果
// queryVec: 查询向量（向量召回），query: 查询文本（关键词召回），userID: 用户ID，limit: 最终返回数量
func (r *MultiRetriever) Retrieve(ctx context.Context, queryVec []float32, query string, userID int64, limit int) ([]*Memory, error) {
	scored := make(map[string]*ScoredMemory) // 按 ID 去重

	// 通路1：向量召回
	if queryVec != nil {
		memories, err := r.store.Retrieve(ctx, queryVec, userID, limit)
		if err == nil {
			for i, m := range memories {
				addScore(scored, m, r.weights.Vector*rankScore(i, len(memories)))
			}
		}
	}

	// 通路2：关键词召回
	if query != "" {
		memories, err := r.store.RetrieveByKeyword(ctx, query, userID, limit)
		if err == nil {
			for i, m := range memories {
				addScore(scored, m, r.weights.Keyword*rankScore(i, len(memories)))
			}
		}
	}

	// 通路3：时间召回
	memories, err := r.store.RetrieveByTime(ctx, userID, limit)
	if err == nil {
		for i, m := range memories {
			addScore(scored, m, r.weights.Time*rankScore(i, len(memories)))
		}
	}

	// 按分数降序排序
	sorted := make([]*ScoredMemory, 0, len(scored))
	for _, sm := range scored {
		sorted = append(sorted, sm)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Score > sorted[j].Score
	})

	// 取 top-N
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}

	result := make([]*Memory, len(sorted))
	for i, sm := range sorted {
		result[i] = sm.Memory
	}
	return result, nil
}

// rankScore 根据在召回列表中的排名计算衰减分数
// 排名越靠前分数越高：第1名=1.0，第2名≈0.5，第3名≈0.33，依此类推
func rankScore(rank, total int) float64 {
	return 1.0 / float64(rank+1)
}

// addScore 将记忆加入评分表，如果已存在则累加分数（多路命中加权）
func addScore(scored map[string]*ScoredMemory, m *Memory, score float64) {
	if existing, ok := scored[m.ID]; ok {
		existing.Score += score // 多路命中，分数累加
	} else {
		scored[m.ID] = &ScoredMemory{Memory: m, Score: score}
	}
}
