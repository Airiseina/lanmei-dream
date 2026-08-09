package topic

import (
	"context"
	"math"
	"testing"

	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"github.com/DaWesen/lanmei-dream/internal/config"
)

// mockEmbedder 基于汉字分布构造确定性向量的 Embedder 测试替身。
// 含相同汉字的文本向量余弦相似度更高，用于语义相关性测试。
type mockEmbedder struct{ dim int }

func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, m.dim)
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			vec[int(r)%m.dim] += 1
		}
	}
	norm := 0.0
	for _, v := range vec {
		norm += float64(v * v)
	}
	if norm == 0 {
		return vec, nil
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range vec {
		vec[i] *= inv
	}
	return vec, nil
}

func (m *mockEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := m.Embed(context.Background(), t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (m *mockEmbedder) Dimension() int { return m.dim }

var _ embedding.Embedder = (*mockEmbedder)(nil)

// TestCosineSimilarity 验证余弦相似度计算正确性。
func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	c := []float32{0, 1, 0}
	if got := cosineSimilarity(a, b); math.Abs(got-1) > 1e-6 {
		t.Fatalf("cos(a,a) = %v, want 1", got)
	}
	if got := cosineSimilarity(a, c); math.Abs(got) > 1e-6 {
		t.Fatalf("cos(a,orthogonal) = %v, want 0", got)
	}
	if got := cosineSimilarity(nil, a); got != 0 {
		t.Fatalf("cos(nil,a) = %v, want 0", got)
	}
	if got := cosineSimilarity(a, []float32{1, 2}); got != 0 {
		t.Fatalf("cos(dim mismatch) = %v, want 0", got)
	}
}

// TestUpdateVectorEMA 验证话题语义中心 EMA 平滑。
func TestUpdateVectorEMA(t *testing.T) {
	topic := &Topic{}
	topic.updateVector([]float32{1, 0})
	topic.updateVector([]float32{0, 1})
	// EMA: v = α*new + (1-α)*old，α=0.3 → 0.3*[0,1] + 0.7*[1,0] = [0.7, 0.3]
	alpha := float32(semanticAlpha)
	if math.Abs(float64(topic.Vector[0])-float64(1-alpha)) > 1e-6 || math.Abs(float64(topic.Vector[1])-float64(alpha)) > 1e-6 {
		t.Fatalf("EMA update = %v, want [0.7 0.3]", topic.Vector)
	}
	// 维度变化时直接重置
	topic.updateVector([]float32{5, 6, 7})
	if len(topic.Vector) != 3 || topic.Vector[0] != 5 {
		t.Fatalf("dim change reset = %v, want [5 6 7]", topic.Vector)
	}
}

// TestSemanticRelevant 覆盖阈值行为与降级路径（无向量/无 Embedder）。
func TestSemanticRelevant(t *testing.T) {
	m := NewManager(&config.TopicConfig{SemanticThreshold: 0.5}, nil, &mockEmbedder{dim: 64}, nil, nil,
		[]string{"蓝妹"}, zap.NewNop())
	topic := &Topic{Members: map[string]*Member{"u1": {UserID: "u1"}}}
	topic.updateVector(mustEmbed(m, "爬山装备"))

	// 语义相关：高度共享关键词"爬山装备"（单字分布余弦 ≈ 0.76）
	msg := &IncomingMsg{GroupID: "g1", UserID: "u1", Content: "爬山装备怎么选"}
	vec, ok := m.embedMessage(context.Background(), msg)
	if !ok {
		t.Fatal("embedMessage failed")
	}
	if !semanticRelevant(m, topic, msg, vec, ok) {
		t.Fatal("semantic relevant message should match topic")
	}

	// 语义不相关：不同话题（成员制关闭时视为不相关）
	msg2 := &IncomingMsg{GroupID: "g1", UserID: "u1", Content: "今晚足球比赛谁赢了"}
	vec2, ok2 := m.embedMessage(context.Background(), msg2)
	if !ok2 {
		t.Fatal("embedMessage failed")
	}
	if semanticRelevant(m, topic, msg2, vec2, ok2) {
		t.Fatal("semantic irrelevant message should not match topic")
	}

	// 降级1：无 Embedder → 成员制宽松（成员即相关）
	mNoEmb := NewManager(&config.TopicConfig{SemanticThreshold: 0.5}, nil, nil, nil, nil,
		[]string{"蓝妹"}, zap.NewNop())
	if !semanticRelevant(mNoEmb, topic, msg2, nil, false) {
		t.Fatal("no-embedder fallback: member should be relevant")
	}
	// 降级2：非成员在无 Embedder 时也不相关
	if semanticRelevant(mNoEmb, topic, &IncomingMsg{UserID: "u2", Content: "你好"}, nil, false) {
		t.Fatal("no-embedder fallback: non-member should be irrelevant")
	}
	// 降级3：阈值 <= 0（语义关闭）→ 成员制
	mOff := NewManager(&config.TopicConfig{SemanticThreshold: 0}, nil, &mockEmbedder{dim: 64}, nil, nil,
		[]string{"蓝妹"}, zap.NewNop())
	if !semanticRelevant(mOff, topic, msg2, vec2, ok2) {
		t.Fatal("threshold-off: member should be relevant")
	}
}

func mustEmbed(m *Manager, text string) []float32 {
	v, err := m.emb.Embed(context.Background(), text)
	if err != nil {
		panic(err)
	}
	return v
}
