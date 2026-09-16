package kb

import (
	"strings"
)

// maxChunkChars 单条召回结果在提示词中展示的最大字符数（rune）。
const maxChunkChars = 200

// FormatRecall 将召回结果格式化为可注入提示词 / 工具返回的文本，空结果返回空字符串。
// 每条为「- [知识库: 名称] 标题」，后跟缩进的内容片段（截断）与来源链接。
//
// 参数：
//   - results：按分数降序的召回结果；Chunk 为 nil 的条目会被跳过
//
// 返回：格式化文本；results 为空（或全部条目的 Chunk 为 nil）时返回空字符串
//
// 注意：内容片段按 rune 截断到 200 字（超出补 "..."）；标题为空时取内容前 24 字，
// 知识库名缺失时回退为知识库 ID，来源链接仅在 URL 非空时输出。
func FormatRecall(results []ScoredChunk) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	for _, sc := range results {
		c := sc.Chunk
		if c == nil {
			continue
		}
		kbName := c.KnowledgeBaseName
		if kbName == "" {
			kbName = c.KnowledgeBaseID
		}
		title := c.Title
		if title == "" {
			title = truncateRunes(c.Content, 24)
		}
		b.WriteString("- [知识库: ")
		b.WriteString(kbName)
		b.WriteString("] ")
		b.WriteString(title)
		b.WriteString("\n")
		if c.Content != "" {
			b.WriteString("  ")
			b.WriteString(truncateRunes(c.Content, maxChunkChars))
			b.WriteString("\n")
		}
		if c.URL != "" {
			b.WriteString("  来源: ")
			b.WriteString(c.URL)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// truncateRunes 按 rune 截断字符串，避免切断多字节 UTF-8 字符。
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-3]) + "..."
}
