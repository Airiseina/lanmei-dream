package kbscore

import (
	"math"
	"testing"
)

// TestCosineSimilarity 验证余弦相似度：相同向量、正交向量、维度不匹配、零向量。
func TestCosineSimilarity(t *testing.T) {
	if got := CosineSimilarity([]float32{1, 0, 0}, []float32{1, 0, 0}); math.Abs(got-1) > 1e-9 {
		t.Errorf("相同向量期望 1，实际 %v", got)
	}
	if got := CosineSimilarity([]float32{1, 0}, []float32{0, 1}); math.Abs(got) > 1e-9 {
		t.Errorf("正交向量期望 0，实际 %v", got)
	}
	if got := CosineSimilarity([]float32{1, 0}, []float32{1}); got != 0 {
		t.Errorf("维度不匹配期望 0，实际 %v", got)
	}
	if got := CosineSimilarity([]float32{0, 0}, []float32{0, 1}); got != 0 {
		t.Errorf("零向量期望 0，实际 %v", got)
	}
	if got := CosineSimilarity(nil, []float32{1}); got != 0 {
		t.Errorf("空向量期望 0，实际 %v", got)
	}
}

// TestFuzzyScore 验证模糊评分：整体包含、中文按字命中、英文 token 命中、空查询。
func TestFuzzyScore(t *testing.T) {
	// 内容整体包含查询 → 满分（内容权重 0.65 全拿 + 标题可能命中）
	if got := FuzzyScore("退货", "退货政策", "签收后 7 天内可无理由退货"); got <= 0.65 {
		t.Errorf("整体包含期望内容满分，实际 %v", got)
	}
	// 中文按单字命中：查询「退货」命中标题"退货政策"中的每个字
	got := FuzzyScore("退货", "退货政策", "政策说明")
	if got <= 0.3 { // 标题 0.35 全命中
		t.Errorf("标题整体包含期望 ≥0.35，实际 %v", got)
	}
	// 英文单词命中
	if got := FuzzyScore("gpt", "gpt-4o 介绍", "GPT 的英文含义"); got <= 0.3 {
		t.Errorf("英文 token 命中期望 >0.3，实际 %v", got)
	}
	// 完全不相关
	if got := FuzzyScore("苹果", "香蕉种植", "番茄的栽培技术"); got != 0 {
		t.Errorf("完全不相关期望 0，实际 %v", got)
	}
	// 空查询
	if got := FuzzyScore("  ", "任意", "内容"); got != 0 {
		t.Errorf("空查询期望 0，实际 %v", got)
	}
}

// TestTruncateRunes 验证按 rune 截断不切断多字节字符。
func TestTruncateRunes(t *testing.T) {
	s := "你好世界abc"
	if got := TruncateRunes(s, 3); got != "你好世" {
		t.Errorf("截断 3 个 rune 期望 %q，实际 %q", "你好世", got)
	}
	if got := TruncateRunes(s, 100); got != s {
		t.Errorf("超长上限期望原样返回")
	}
	if got := TruncateRunes("", 3); got != "" {
		t.Errorf("空串期望空")
	}
}
