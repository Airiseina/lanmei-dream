package gateway

import "encoding/json"

// ── 平台与协议标识 ──

// Platform 标识消息来源的 IM 平台
type Platform string

const (
	PlatformQQ       Platform = "qq"       // 通过 Onebots 网关连接的 QQ
	PlatformWechat   Platform = "wechat"   // 微信
	PlatformTelegram Platform = "telegram" // Telegram
	PlatformDing     Platform = "ding"     // 钉钉
	PlatformNapCat   Platform = "napcat"   // NapCat 直连（NTQQ）
)

// Protocol 标识 OneBot 协议版本
type Protocol string

const (
	ProtocolV12 Protocol = "onebot12" // 标准 OneBot 12
	ProtocolV11 Protocol = "onebot11" // OneBot 11（NapCat 方言）
)

// ── OneBot 12 事件 ──

// EventV12 表示 OneBot 12 标准事件
type EventV12 struct {
	ID         string              `json:"id"`
	Impl       string              `json:"impl"`
	Platform   string              `json:"platform"`
	SelfID     string              `json:"self_id"`
	Time       float64             `json:"time"`
	Type       string              `json:"type"`        // meta / message / notice / request
	DetailType string              `json:"detail_type"` // private / group / group_member_increase / ...
	SubType    string              `json:"sub_type"`
	UserID     string              `json:"user_id,omitempty"`
	GroupID    string              `json:"group_id,omitempty"`
	Message    []MessageSegmentV12 `json:"message,omitempty"`
	AltMessage string              `json:"alt_message,omitempty"` // 纯文本表示

	// ── notice 事件附加字段 ──
	OperatorID string `json:"operator_id,omitempty"` // 操作者（拉人者/禁言管理员）
	TargetID   string `json:"target_id,omitempty"`   // 被操作者（poke 被戳者）
	Duration   int64  `json:"duration,omitempty"`    // 禁言时长（秒）
	MessageID  string `json:"message_id,omitempty"`  // 撤回的消息 ID（group_recall）
}

// ── OneBot 11 事件（NapCat 兼容） ──

// EventV11 表示 OneBot 11 风格事件（NapCat 方言）
//
// 注意：Message 字段使用 json.RawMessage 而非具体类型，
// 以避免 API 响应的 "message":""（string）与事件 "message":[{...}]（array）冲突。
type EventV11 struct {
	Time        int64           `json:"time"`
	SelfID      int64           `json:"self_id"`
	PostType    string          `json:"post_type"`              // message / notice / request / meta_event
	MessageType string          `json:"message_type,omitempty"` // private / group
	SubType     string          `json:"sub_type,omitempty"`
	UserID      int64           `json:"user_id,omitempty"`
	GroupID     int64           `json:"group_id,omitempty"`
	Message     json.RawMessage `json:"message,omitempty"` // array of MessageSegmentV11 or string
	RawMessage  string          `json:"raw_message,omitempty"`
	Sender      SenderV11       `json:"sender,omitempty"`
	// 消息 ID（NapCat 扩展）
	MessageID  int64 `json:"message_id,omitempty"`
	MessageSeq int64 `json:"message_seq,omitempty"`

	// ── notice 事件附加字段 ──
	NoticeType string `json:"notice_type,omitempty"` // group_increase / group_decrease / notify / ...
	OperatorID int64  `json:"operator_id,omitempty"` // 操作者（拉人者/禁言管理员）
	TargetID   int64  `json:"target_id,omitempty"`   // 被操作者（poke 被戳者）
	Duration   int64  `json:"duration,omitempty"`    // 禁言时长（秒）
}

// ParseMessageSegments 将 Message 字段解析为 OneBot 11 消息段列表。
//
// 兼容两种格式：
//   - array:  [{"type":"text","data":{"text":"hi"}}]
//   - string: ""（API 响应中的空字符串）或 CQ 码（暂不支持）
func (e *EventV11) ParseMessageSegments() []MessageSegmentV11 {
	if len(e.Message) == 0 {
		return nil
	}
	var segments []MessageSegmentV11
	if err := json.Unmarshal(e.Message, &segments); err == nil {
		return segments
	}
	// 非数组格式（string/null/其他），返回空
	return nil
}

// SenderV11 表示 OneBot 11 消息发送者
type SenderV11 struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Sex      string `json:"sex,omitempty"`
	Age      int    `json:"age,omitempty"`
	Card     string `json:"card,omitempty"` // 群名片
	Role     string `json:"role,omitempty"` // owner / admin / member
}
