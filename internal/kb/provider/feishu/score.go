package feishu

import (
	"math"
	"strings"
)

// cosineSimilarity 计算两个向量的余弦相似度（归一化到 [0,1]，负数视为 0）。
// 维度不匹配或零向量返回 0。
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x := float64(a[i])
		y := float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	sim := dot / (math.Sqrt(na) * math.Sqrt(nb))
	if sim < 0 {
		return 0
	}
	return sim
}

// fuzzyScore 计算查询与文档的模糊匹配分数（0~1）。
//
// 评分规则：
//   - 标题与内容各自计算命中率（整体包含给满分，否则按 token 命中比例）；
//   - 最终分数 = 0.35 × 标题命中率 + 0.65 × 内容命中率（内容证据更强）。
//
// token 切分：拉丁单词按字母数字连续段，中文按单字，兼顾中英文查询。
func fuzzyScore(query, title, content string) float64 {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0
	}
	qtoks := tokenize(q)
	if len(qtoks) == 0 {
		return 0
	}
	titleScore := matchScore(q, qtoks, strings.ToLower(title))
	contentScore := matchScore(q, qtoks, strings.ToLower(content))
	return 0.35*titleScore + 0.65*contentScore
}

// matchScore 计算查询 token 集在目标文本中的命中率（0~1）。
// 目标文本整体包含查询原文时直接返回 1.0（最强信号）。
func matchScore(query string, qtoks []string, target string) float64 {
	if target == "" {
		return 0
	}
	if strings.Contains(target, query) {
		return 1.0
	}
	set := make(map[string]struct{}, 16)
	for _, tok := range tokenize(target) {
		set[tok] = struct{}{}
	}
	hits := 0
	for _, tok := range qtoks {
		if _, ok := set[tok]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(qtoks))
}

// tokenize 将文本切分为查询/匹配 token。
//   - 拉丁字母与数字：按连续段切分（如 "hello"、"gpt-4o" 拆为 "gpt","4o"）；
//   - 中文（CJK）：每个字符独立成 token；
//   - 其它符号：作为分隔符丢弃。
func tokenize(s string) []string {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			cur.WriteRune(r)
		case isCJK(r):
			flush()
			toks = append(toks, string(r))
		default:
			flush()
		}
	}
	flush()
	return toks
}

// isCJK 判断是否为中日韩统一表意文字（含扩展 A 区）。
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

// truncateRunes 按 rune 截断字符串，避免切断多字节 UTF-8 字符。
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
