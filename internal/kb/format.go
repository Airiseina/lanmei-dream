package kb

import (
	"strings"
)

// maxChunkChars 单条召回结果在提示词中展示的最大字符数（rune）。
const maxChunkChars = 200

// FormatRecall 将召回结果格式化为可注入提示词 / 工具返回的文本。
//
// 输出格式（空结果返回空字符串）：
//
//   - [知识库: 主知识库] 标题
//     内容片段（截断）
//     来源: url
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
