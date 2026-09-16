package memory

import (
	"context"
	"sort"
)

// RecallWeight 定义各召回通路的基础权重
type RecallWeight struct {
	Vector  float64
	Keyword float64
	Time    float64
}

// DefaultRecallWeight 默认权重：向量最高，关键词次之，时间最低
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

// Retrieve 执行多路召回并合并结果：queryVec 走向量召回、query 走关键词召回，
// 时间召回始终执行；groupID 为空表示私聊（按群级过滤），limit 为最终返回数量。
func (r *MultiRetriever) Retrieve(ctx context.Context, queryVec []float32, query string, userID int64, groupID string, limit int) ([]*Memory, error) {
	scored := make(map[string]*ScoredMemory)

	if queryVec != nil {
		memories, err := r.store.Retrieve(ctx, queryVec, userID, groupID, limit)
		if err == nil {
			for i, m := range memories {
				addScore(scored, m, r.weights.Vector*rankScore(i, len(memories)))
			}
		}
	}

	if query != "" {
		memories, err := r.store.RetrieveByKeyword(ctx, query, userID, groupID, limit)
		if err == nil {
			for i, m := range memories {
				addScore(scored, m, r.weights.Keyword*rankScore(i, len(memories)))
			}
		}
	}

	memories, err := r.store.RetrieveByTime(ctx, userID, groupID, limit)
	if err == nil {
		for i, m := range memories {
			addScore(scored, m, r.weights.Time*rankScore(i, len(memories)))
		}
	}

	sorted := make([]*ScoredMemory, 0, len(scored))
	for _, sm := range scored {
		sorted = append(sorted, sm)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Score > sorted[j].Score
	})

	if len(sorted) > limit {
		sorted = sorted[:limit]
	}

	result := make([]*Memory, len(sorted))
	for i, sm := range sorted {
		result[i] = sm.Memory
	}
	return result, nil
}

// rankScore 按召回列表中的排名计算衰减分数：第 1 名 1.0，第 2 名 ≈0.5，依此类推。
func rankScore(rank, total int) float64 {
	return 1.0 / float64(rank+1)
}

// addScore 将记忆加入评分表；多路命中同一记忆时分数累加。
func addScore(scored map[string]*ScoredMemory, m *Memory, score float64) {
	if existing, ok := scored[m.ID]; ok {
		existing.Score += score
	} else {
		scored[m.ID] = &ScoredMemory{Memory: m, Score: score}
	}
}
