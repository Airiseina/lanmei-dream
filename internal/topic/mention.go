package topic

// 提及分类：决定群消息"是否应回复"的强/弱/无三档信号。
//
// 设计（LLM 主判官方案）：
//   - at（平台 ID 精确命中）为免费且精确的强信号，不依赖 LLM；
//   - 其余提及（呼格/主语/祈使宾语/关系从句/条件句/话题标记/情感对象/指代等）
//     由意图分析 LLM 调用返回的 LinguisticJudge 判定，按置信度划分强/弱；
//   - 不再使用硬编码的文本结构规则（无法适配自然语言，易漏判）。

// MentionMode 提及模式（决策与日志用）。
type MentionMode int

const (
	// MentionNone 未提及：不回复（成员继续按话题切换/冷却处理）
	MentionNone MentionMode = iota
	// MentionAt at 直接提及（平台 ID 精确命中，免费强信号）
	MentionAt
	// MentionLinguistic LLM 语言学提及（呼格/主语/祈使/条件/情感/指代等）
	MentionLinguistic
)

// String 返回提及模式的文本表示（用于日志）。
func (m MentionMode) String() string {
	switch m {
	case MentionAt:
		return "at"
	case MentionLinguistic:
		return "linguistic"
	default:
		return "none"
	}
}

// MentionResult 提及分类结果。
type MentionResult struct {
	Mentioned bool
	Mode      MentionMode
	Strong    bool // 强提及（直接回复）还是弱提及（拉入/配额回复）
}

// containsString 判断字符串切片是否包含指定值。
func containsString(slice []string, target string) bool {
	if target == "" {
		return false
	}
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}
