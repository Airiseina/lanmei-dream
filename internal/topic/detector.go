package topic

import (
	"strings"
	"unicode"
)

// MentionMode 提及模式分类。
type MentionMode int

const (
	// MentionNone 未提及
	MentionNone MentionMode = iota
	// MentionAt @了 bot
	MentionAt
	// MentionVocative 呼格：句首/标点后 + 昵称 + 逗号/冒号（如"蓝妹，帮我写个报告"）
	MentionVocative
	// MentionImperative 祈使宾语：让/叫/请/问/找 + 昵称（如"让蓝妹看看"）
	MentionImperative
	// MentionRelay 传话：告诉/转告 + 昵称（弱信号，对象不是 bot）
	MentionRelay
	// MentionPassive 被动提及：昵称作为主语/一般宾语（弱信号）
	MentionPassive
)

// String 返回提及模式的文本表示。
func (m MentionMode) String() string {
	switch m {
	case MentionNone:
		return "none"
	case MentionAt:
		return "at"
	case MentionVocative:
		return "vocative"
	case MentionImperative:
		return "imperative"
	case MentionRelay:
		return "relay"
	case MentionPassive:
		return "passive"
	default:
		return "unknown"
	}
}

// IsStrong 是否为强提及信号（At/Vocative/Imperative）。
func (m MentionMode) IsStrong() bool {
	return m == MentionAt || m == MentionVocative || m == MentionImperative
}

// MentionResult 提及检测结果。
type MentionResult struct {
	// Mentioned 是否提及了 bot
	Mentioned bool
	// Mode 提及模式
	Mode MentionMode
	// Strong 是否为强信号（At/Vocative/Imperative）
	Strong bool
	// Offset 昵称首次出现位置（rune 偏移），未匹配昵称时为 -1
	Offset int
}

// vocativePrefixRunes 呼格允许的前缀字符（句首或标点后，含中文括号）。
var vocativePrefixRunes = "，。！？；、\n\r\t 　（("

// vocativeSuffixRunes 呼格允许的后缀字符（逗号/冒号/问号或空白，含中文括号）。
var vocativeSuffixRunes = "，。！？；：\n\r\t 　）)"

// imperativeVerbs 祈使宾语动词模式。
var imperativeVerbs = []string{"让", "叫", "请", "问", "找", "唤", "召唤", "喊"}

// relayVerbs 传话动词模式（对象不是 bot）。
var relayVerbs = []string{"告诉", "转告", "通知"}

// requestMarkers 提问/请求结构标记（用于 at 消息强弱判定与无分隔符呼格识别）。
// 含问候词、正反疑问式（"在不在""好不好"）、观点询问（"怎么看"）与命令提醒（"记得"），
// 覆盖中文口语中"跟 bot 说话"的常见句式。
var requestMarkers = []string{
	"帮我", "请", "可以", "能不能", "能帮", "吗", "呢", "吧", "？", "?",
	"你好", "您好", "早上好", "中午好", "下午好", "晚上好", "晚安",
	// 正反疑问（V不V）
	"在不在", "好不好", "行不行", "要不要", "有没有", "是不是",
	// 观点/情况询问、命令提醒与"告诉我"句式
	"怎么看", "怎么样", "有什么", "记得", "告诉我", "和我说说", "跟我说说",
	// 复数打招呼与请求句式
	"你看看", "过来一下",
}

// Detector 提及检测器：基于 rune 级字符串规则的确定性判定，无 LLM 依赖。
//
// 信号强度：At / Vocative / Imperative 为强信号；Relay / Passive 为弱信号。
type Detector struct{}

// NewDetector 创建提及检测器。
func NewDetector() *Detector { return &Detector{} }

// Detect 检测群消息是否提及了 bot。
// text 为消息纯文本（at 段已转为 @昵称）；atTargets 为网关标准化的 at 目标列表。
//
// 判定优先级：at → 昵称位置分析（呼格/祈使/传话）→ 兜底被动提及。
func (d *Detector) Detect(text string, atTargets []string, bot *BotIdentity) MentionResult {
	if bot == nil || len(bot.Nicknames) == 0 {
		return MentionResult{Offset: -1}
	}

	// 1. at 检测：at 目标含 bot 自身 ID
	atBot := containsString(atTargets, bot.SelfID)

	// 2. 昵称匹配：取首次出现位置（rune 偏移）
	runes := []rune(text)
	offset := -1
	var matchedNick string
	for _, nick := range bot.Nicknames {
		if nick == "" {
			continue
		}
		if idx := runeIndexOf(runes, nick); idx >= 0 && (offset == -1 || idx < offset) {
			offset = idx
			matchedNick = nick
		}
	}

	// at 命中优先：分析文本结构判定强弱
	if atBot {
		mode := MentionAt
		strong := true
		// 排除"把 bot 当宾语提及"的非祈使 at：文本为陈述且无请求结构 → 降级弱信号
		if text != "" && !isPureAtText(text, bot.Nicknames) && !looksLikeRequest(text) {
			mode = MentionPassive
			strong = false
		}
		return MentionResult{Mentioned: true, Mode: mode, Strong: strong, Offset: offset}
	}

	// 无 at：依赖昵称位置分析
	if offset < 0 {
		return MentionResult{Offset: -1}
	}
	nickRunes := []rune(matchedNick)

	// 3. 呼格判定：昵称位于句首/标点后（前缀 OK）
	prefixOK := offset == 0 || strings.ContainsRune(vocativePrefixRunes, runes[offset-1])
	if prefixOK {
		after := offset + len(nickRunes)
		// 3a. 后缀为逗号/冒号/问号/空白或句尾 → 典型呼格（"蓝妹，帮我写个报告"）
		if after >= len(runes) || strings.ContainsRune(vocativeSuffixRunes, runes[after]) {
			return MentionResult{Mentioned: true, Mode: MentionVocative, Strong: true, Offset: offset}
		}
		// 3b. 无分隔符口语（"蓝妹在吗""蓝妹帮我看看"）：
		//     昵称后文本含请求/提问/问候结构 → 仍判呼格，避免常见口语被降级为弱信号
		if looksLikeRequest(string(runes[after:])) {
			return MentionResult{Mentioned: true, Mode: MentionVocative, Strong: true, Offset: offset}
		}
		// 3c. 昵称紧跟重复的同一昵称（"蓝妹蓝妹"）→ 急切呼唤，判呼格
		if strings.HasPrefix(string(runes[after:]), matchedNick) {
			return MentionResult{Mentioned: true, Mode: MentionVocative, Strong: true, Offset: offset}
		}
	}

	// 4. 祈使宾语判定：昵称前的滑动窗口（≤4 rune）内含祈使动词
	if windowContainsAny(runes, offset, 4, imperativeVerbs) {
		return MentionResult{Mentioned: true, Mode: MentionImperative, Strong: true, Offset: offset}
	}

	// 5. 传话判定：昵称前含传话动词（弱信号）
	if windowContainsAny(runes, offset, 8, relayVerbs) {
		return MentionResult{Mentioned: true, Mode: MentionRelay, Strong: false, Offset: offset}
	}

	// 6. 兜底：仅出现昵称 → 被动提及（弱信号）
	return MentionResult{Mentioned: true, Mode: MentionPassive, Strong: false, Offset: offset}
}

// windowContainsAny 检查 runes[start-window, start) 窗口内是否含任一关键词。
func windowContainsAny(runes []rune, start, window int, keywords []string) bool {
	if start <= 0 {
		return false
	}
	lo := start - window
	if lo < 0 {
		lo = 0
	}
	windowText := string(runes[lo:start])
	for _, kw := range keywords {
		if strings.Contains(windowText, kw) {
			return true
		}
	}
	return false
}

// isPureAtText 判断文本是否仅为 @ 提及（去除 @ 昵称后无其他有效文字）。
// nicknames 用于精确匹配已知昵称：无分隔符时（如"@蓝妹在吗"）只跳过昵称本身，
// 避免把昵称后的正文一并吞掉导致强弱判定不稳定。
func isPureAtText(text string, nicknames []string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	var b strings.Builder
	runes := []rune(trimmed)
	for i := 0; i < len(runes); {
		if runes[i] == '@' {
			// 优先精确匹配已知昵称（最长优先），只跳过昵称本身
			if matched := matchNickAt(runes, i+1, nicknames); matched > 0 {
				i += 1 + matched
				continue
			}
			// 未知昵称兜底：跳到空白或标点
			i++
			for i < len(runes) && !isAtTerminator(runes[i]) {
				i++
			}
			continue
		}
		b.WriteRune(runes[i])
		i++
	}
	// 剩余仅标点/空白视为纯 at（"@蓝妹："等于直接呼唤 bot，属强信号）
	return isAllPunctOrSpace(b.String())
}

// isAllPunctOrSpace 判断文本是否仅由标点与空白组成。
func isAllPunctOrSpace(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) {
			return false
		}
	}
	return true
}

// matchNickAt 尝试从 runes[start] 起匹配任一昵称（最长优先），返回匹配的 rune 数。
// 未匹配任何昵称时返回 0。
func matchNickAt(runes []rune, start int, nicknames []string) int {
	best := 0
	for _, nick := range nicknames {
		nickRunes := []rune(nick)
		if len(nickRunes) == 0 || start+len(nickRunes) > len(runes) {
			continue
		}
		match := true
		for j := range nickRunes {
			if runes[start+j] != nickRunes[j] {
				match = false
				break
			}
		}
		if match && len(nickRunes) > best {
			best = len(nickRunes)
		}
	}
	return best
}

// isAtTerminator 判断 rune 是否为 @ 昵称的终止字符。
func isAtTerminator(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '，' || r == '。' || r == '！' || r == '？' || r == ',' || r == '!' || r == '?'
}

// looksLikeRequest 判断文本是否含提问/请求结构（at 消息强弱判定的依据）。
func looksLikeRequest(text string) bool {
	t := strings.TrimSpace(text)
	for _, marker := range requestMarkers {
		if strings.Contains(t, marker) {
			return true
		}
	}
	return false
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

// runeIndexOf 在 rune 序列中查找子串的首次出现位置（rune 偏移），未找到返回 -1。
func runeIndexOf(runes []rune, sub string) int {
	subRunes := []rune(sub)
	if len(subRunes) == 0 || len(runes) < len(subRunes) {
		return -1
	}
	for i := 0; i+len(subRunes) <= len(runes); i++ {
		match := true
		for j := range subRunes {
			if runes[i+j] != subRunes[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
