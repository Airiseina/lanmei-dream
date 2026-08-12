// Package topic 实现群聊智能对话的 Topic（话题）系统。
//
// 核心思想（详见 docs/group-topic-design.md）：
// "是否要回复"是一个语言学判定问题 —— at（平台 ID）为免费精确的强信号，
// 其余提及（呼格/主语/祈使/条件/情感/指代等）由意图分析 LLM 调用
// （internal/ai/intent）的提及判断返回，按置信度划分为强/弱提及。
//
// 模块划分：
//   - mention.go：提及分类（MentionMode / MentionResult）
//   - linguistic.go：提及判断数据类型（LinguisticJudge / MentionRole）
//   - types.go：话题数据模型与状态机
//   - manager.go：TopicManager（群消息决策 + 状态管理 + 持久化）
//   - semantic.go：语义相关性判定（embedding）
//   - archive.go：冷却话题异步归档到记忆层
package topic

import (
	"time"
)

// TopicState 话题生命周期状态。
type TopicState int

const (
	// TopicActive 活跃：最近窗口内有提及或 Bot 回复，且成员非空
	TopicActive TopicState = iota
	// TopicCooling 冷却：窗口内无提及/回复，或成员已清空，等待超时归档
	TopicCooling
)

// String 返回状态的文本表示（用于日志与持久化）。
func (s TopicState) String() string {
	switch s {
	case TopicActive:
		return "active"
	case TopicCooling:
		return "cooling"
	default:
		return "unknown"
	}
}

// Member 话题成员：参与该话题对话的用户。
type Member struct {
	UserID       string    `json:"user_id"`        // 平台 user_id
	Nickname     string    `json:"nickname"`       // 用户昵称（上下文注入用，可能为空）
	LastActiveAt time.Time `json:"last_active_at"` // 最近一次相关发言
	MentionCount int       `json:"mention_count"`  // 本话题内提及 Bot 次数（热度参考）
	Credit       int       `json:"credit"`         // 回复配额：Bot 回复后授 1，下一条相关消息自动回复
}

// TopicMsg 话题内的一条消息（近窗口，供上下文注入与归档）。
type TopicMsg struct {
	UserID   string    `json:"user_id"`  // 发送者（Bot 回复时 UserID 为 bot 自身 ID）
	Nickname string    `json:"nickname"` // 发送者昵称（上下文注入标注发言者，可能为空）
	IsBot    bool      `json:"is_bot"`   // 是否 Bot 回复
	Content  string    `json:"content"`
	At       bool      `json:"at"` // 是否提及了 bot
	SentAt   time.Time `json:"sent_at"`
}

// Topic 一个群内的话题（对话线程）。
//
// 状态机：
//
//	[不存在] --强提及/语义命中--> [Active] --窗口内无提及且成员空/窗口到期--> [Cooling]
//	[Active/Cooling] --窗口内提及--> 刷新 LastMentionAt 保持/恢复 Active
//	[Cooling] --冷却超时--> 归档（Archived，从内存与 Redis 移除）
type Topic struct {
	ID              string             `json:"id"`       // topic:<platform>:<groupID>:<seq>
	Platform        string             `json:"platform"` // 来源平台（qq/wechat/telegram...）
	GroupID         string             `json:"group_id"`
	State           TopicState         `json:"state"`
	Label           string             `json:"label"`         // 话题一句话描述（LLM 懒生成，空=未生成）
	LabelPending    bool               `json:"label_pending"` // 标签生成中（防止重复触发）
	Members         map[string]*Member `json:"members"`
	LastMentionAt   time.Time          `json:"last_mention_at"`  // 最近一次强提及 bot
	LastActiveAt    time.Time          `json:"last_active_at"`   // 最近一次活跃（含成员相关发言/回复）
	LastTouchSeq    int64              `json:"last_touch_seq"`   // 群消息序列中最近一次"触碰"（提及/续聊/回复）
	MsgWindow       []TopicMsg         `json:"msg_window"`       // 近期消息窗口
	Vector          []float32          `json:"vector,omitempty"` // 话题语义中心（embedding EMA）
	ArchiveAttempts int                `json:"archive_attempts"` // 归档重试次数（超过上限放弃，防无限重试）
	CreatedAt       time.Time          `json:"created_at"`
}

// IsActive 判断话题是否活跃。
func (t *Topic) IsActive() bool { return t.State == TopicActive }

// touchMention 刷新提及时间并追加消息（强提及/重入路径）。
func (t *Topic) touchMention(msg TopicMsg, now time.Time) {
	t.State = TopicActive
	t.LastMentionAt = now
	t.LastActiveAt = now
	t.pushMsg(msg)
}

// touchChat 追加一条普通相关消息（续聊路径，不刷新 LastMentionAt）。
func (t *Topic) touchChat(msg TopicMsg, now time.Time) {
	t.LastActiveAt = now
	t.pushMsg(msg)
}

// pushMsg 追加消息到窗口，并裁剪超出上限的旧消息。
func (t *Topic) pushMsg(msg TopicMsg, maxWindow ...int) {
	limit := topicWindowDefault
	if len(maxWindow) > 0 && maxWindow[0] > 0 {
		limit = maxWindow[0]
	}
	t.MsgWindow = append(t.MsgWindow, msg)
	if len(t.MsgWindow) > limit {
		// 保留最近 limit 条
		t.MsgWindow = t.MsgWindow[len(t.MsgWindow)-limit:]
	}
}

// markCooling 将话题置为冷却（成员清空或窗口内无提及）。
func (t *Topic) markCooling() {
	t.State = TopicCooling
}

// restoreActive 冷却中的话题被重新提及/重入时恢复活跃。
func (t *Topic) restoreActive(now time.Time) {
	t.State = TopicActive
	t.LastMentionAt = now
	t.LastActiveAt = now
}

// MemberCount 返回话题成员数量。
func (t *Topic) MemberCount() int { return len(t.Members) }

// DisplayLabel 返回话题展示名（未生成时使用默认值）。
func (t *Topic) DisplayLabel() string {
	if t.Label != "" {
		return t.Label
	}
	return defaultTopicLabel
}

// defaultTopicLabel 话题标签未生成（无 LLM/生成中）时的兜底展示名。
const defaultTopicLabel = "群聊话题"

// upsertMember 新增或刷新成员（mention 为 true 时累计提及次数）。
func (t *Topic) upsertMember(userID, nickname string, now time.Time, mention bool) {
	if userID == "" {
		return
	}
	m, ok := t.Members[userID]
	if !ok {
		t.Members[userID] = &Member{UserID: userID, Nickname: nickname, LastActiveAt: now}
		return
	}
	m.LastActiveAt = now
	if nickname != "" {
		m.Nickname = nickname // 昵称可能变化，持续更新
	}
	if mention {
		m.MentionCount++
	}
}

// detachMember 移除成员（话题切换时调用）。
func (t *Topic) detachMember(userID string) {
	delete(t.Members, userID)
}

// grantCredit 授予成员回复配额（Bot 回复后调用）。
func (t *Topic) grantCredit(userID string) {
	if m, ok := t.Members[userID]; ok {
		m.Credit = 1
	}
}

// consumeCredit 消耗成员回复配额；返回 true 表示有配额（应回复）。
func (t *Topic) consumeCredit(userID string) bool {
	if m, ok := t.Members[userID]; ok && m.Credit > 0 {
		m.Credit = 0
		return true
	}
	return false
}

// hasCredit 判断成员是否有回复配额（只读检查，不消耗）。
func (t *Topic) hasCredit(userID string) bool {
	if m, ok := t.Members[userID]; ok {
		return m.Credit > 0
	}
	return false
}

// botWindowDefault 消息窗口默认上限（topic_window_msgs 未配置时使用）。
const topicWindowDefault = 20

// IncomingMsg 群消息输入（由 TopicGatePass 构造）。
type IncomingMsg struct {
	Platform  string
	SelfID    string // 机器人自身 ID（at 目标匹配）
	GroupID   string
	UserID    string
	UserName  string // 发送者昵称（上下文注入/归档用）
	Content   string
	AtTargets []string // 网关标准化的 at 目标列表
	SentAt    time.Time
}

// Decision 群消息决策结果（HandleGroupMessage 返回值）。
type Decision struct {
	// Reply 是否应回复（REPLY）
	Reply bool
	// Topic 命中/创建的话题（nil = 未命中任何话题）
	Topic *Topic
	// Mention 提及模式（MentionNone 表示未提及）
	Mention MentionMode
}

// DecisionReply 便捷判断：是否为"应回复"决策。
func (d *Decision) DecisionReply() bool { return d != nil && d.Reply }
