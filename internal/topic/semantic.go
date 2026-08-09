package topic

import (
	"context"
	"math"

	"go.uber.org/zap"
)

// semanticAlpha 话题向量 EMA 平滑系数：
// t.Vector = α * newVec + (1-α) * t.Vector。α 越小越平滑，抗单条消息抖动。
const semanticAlpha = 0.3

// semanticMaxEmbeddingLen 参与向量化的文本长度上限（rune），超长截断防成本失控。
const semanticMaxEmbeddingLen = 200

// embedMessage 计算消息文本的向量（每条消息最多一次 embedding，各判定路径复用）。
// 返回 vecOK=false 表示语义能力不可用（无 Embedder / 内容为空 / 调用失败），调用方应降级。
func (m *Manager) embedMessage(ctx context.Context, msg *IncomingMsg) (vec []float32, vecOK bool) {
	if m.emb == nil || msg == nil || msg.Content == "" {
		return nil, false
	}
	vec, err := m.emb.Embed(ctx, truncateRunes(msg.Content, semanticMaxEmbeddingLen))
	if err != nil || len(vec) == 0 {
		m.logger.Debug("topic: 向量化失败，降级为成员制", zap.String("group", msg.GroupID), zap.Error(err))
		return nil, false
	}
	return vec, true
}

// semanticRelevant 判断消息是否属于话题 t 的延续。
//
// 判定逻辑（成本从低到高）：
//  1. 阈值 <= 0 → 关闭语义判定，成员制宽松模式（发送者是成员即相关）；
//  2. 有向量（vecOK）且话题有语义中心 → cos(vec, t.Vector) >= threshold；
//  3. 语义不可用 → 降级为成员制宽松模式（失败不阻塞）。
func semanticRelevant(m *Manager, t *Topic, msg *IncomingMsg, vec []float32, vecOK bool) bool {
	if m.cfg == nil || m.cfg.SemanticThreshold <= 0 || !vecOK {
		return t.isMember(msg.UserID)
	}
	if len(t.Vector) == 0 {
		return t.isMember(msg.UserID)
	}
	sim := cosineSimilarity(vec, t.Vector)
	return sim >= m.cfg.SemanticThreshold
}

// semanticMatch 在候选话题中找出与消息语义最匹配的一个。
//
// 匹配策略：
//   - 语义命中（vecOK 且话题有中心）：取相似度最高者，需 >= threshold；
//   - 语义不可用：取最近活跃的话题（LastActiveAt 最新）；
//   - 冷却话题也可被匹配（重入恢复，由调用方决定是否传入）。
func semanticMatch(m *Manager, candidates []*Topic, msg *IncomingMsg, vec []float32, vecOK bool) *Topic {
	if len(candidates) == 0 {
		return nil
	}
	if vecOK && m.cfg != nil && m.cfg.SemanticThreshold > 0 {
		best := (*Topic)(nil)
		bestSim := m.cfg.SemanticThreshold // 相似度须达到阈值
		for _, t := range candidates {
			if len(t.Vector) == 0 {
				continue
			}
			if sim := cosineSimilarity(vec, t.Vector); sim > bestSim {
				bestSim = sim
				best = t
			}
		}
		if best != nil {
			return best
		}
	}
	// 语义不可用/无命中：取最近活跃的话题
	best := candidates[0]
	for _, t := range candidates[1:] {
		if t.LastActiveAt.After(best.LastActiveAt) {
			best = t
		}
	}
	return best
}

// cosineSimilarity 计算两个向量的余弦相似度。
// 向量为空或维度不一致时返回 0。
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// updateVector 以 EMA 方式将新消息向量并入话题语义中心。
// 首次设置时直接采用新向量。
func (t *Topic) updateVector(vec []float32) {
	if len(vec) == 0 {
		return
	}
	if len(t.Vector) == 0 {
		t.Vector = vec
		return
	}
	if len(t.Vector) != len(vec) {
		t.Vector = vec // 维度变化（模型切换）时直接重置
		return
	}
	alpha := float32(semanticAlpha)
	for i := range t.Vector {
		t.Vector[i] = alpha*vec[i] + (1-alpha)*t.Vector[i]
	}
}

// isMember 判断用户是否为话题成员。
func (t *Topic) isMember(userID string) bool {
	if userID == "" {
		return false
	}
	_, ok := t.Members[userID]
	return ok
}

// truncateRunes 按 rune 截断字符串，避免切割 UTF-8 字符。
// n <= 0 时返回空串（防御非法调用）。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// activeOnly 过滤出活跃话题（冷却话题不参与被动重入）。
func activeOnly(topics []*Topic) []*Topic {
	out := make([]*Topic, 0, len(topics))
	for _, t := range topics {
		if t.IsActive() {
			out = append(out, t)
		}
	}
	return out
}

// memberTopicOf 返回发送者所属的第一个活跃话题（一个用户同一时刻只在一个活跃话题内）。
func memberTopicOf(topics []*Topic, userID string) *Topic {
	for _, t := range topics {
		if t.IsActive() && t.isMember(userID) {
			return t
		}
	}
	return nil
}
