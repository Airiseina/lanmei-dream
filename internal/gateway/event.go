package gateway

import "encoding/json"

// Platform 标识消息来源的 IM 平台。
type Platform string

const (
	// PlatformQQ 通过 Onebots 网关连接的 QQ。
	PlatformQQ Platform = "qq" // 通过 Onebots 网关连接的 QQ
	// PlatformWechat 微信平台。
	PlatformWechat Platform = "wechat" // 微信
	// PlatformTelegram Telegram 平台。
	PlatformTelegram Platform = "telegram" // Telegram
	// PlatformDing 钉钉平台。
	PlatformDing Platform = "ding" // 钉钉
	// PlatformNapCat NapCat 直连（NTQQ）。
	PlatformNapCat Platform = "napcat" // NapCat 直连（NTQQ）
)

// Protocol 标识 OneBot 协议版本。
type Protocol string

const (
	// ProtocolV12 标准 OneBot 12：出站统一用 send_message 动作，消息段字段见 MessageSegmentV12。
	ProtocolV12 Protocol = "onebot12" // 标准 OneBot 12
	// ProtocolV11 OneBot 11（NapCat 方言）：出站用 send_group_msg / send_private_msg 动作，
	// at 段用 qq 字段、引用段用 id 字段，详见 MessageSegmentV11。
	ProtocolV11 Protocol = "onebot11" // OneBot 11（NapCat 方言）
)

// EventV12 表示 OneBot 12 标准事件。
//
// 注意：notice / request 事件仅在 Type 匹配时填充 OperatorID / TargetID / Duration /
// MessageID 等附加字段；消息段字段差异（at 段 user_id、引用段 message_id）见
// MessageSegmentV12。OneBot 11 的事件结构为 EventV11。
type EventV12 struct {
	ID         string              `json:"id"`
	Impl       string              `json:"impl"`
	Platform   string              `json:"platform"`
	SelfID     string              `json:"self_id"`
	Self       SelfV12             `json:"self,omitempty"` // 机器人自身标识（OneBot 12 self 对象）
	Time       float64             `json:"time"`
	Type       string              `json:"type"`        // meta / message / notice / request
	DetailType string              `json:"detail_type"` // private / group / group_member_increase / ...
	SubType    string              `json:"sub_type"`
	UserID     string              `json:"user_id,omitempty"`
	GroupID    string              `json:"group_id,omitempty"`
	Message    []MessageSegmentV12 `json:"message,omitempty"`
	AltMessage string              `json:"alt_message,omitempty"` // 纯文本表示

	OperatorID string `json:"operator_id,omitempty"` // 操作者（拉人者/禁言管理员/戳人者）
	TargetID   string `json:"target_id,omitempty"`   // 被操作者（poke 被戳者）
	Duration   int64  `json:"duration,omitempty"`    // 禁言时长（秒）
	MessageID  string `json:"message_id,omitempty"`  // 撤回的消息 ID（group_recall）
}

// SelfV12 表示 OneBot 12 事件的 self 对象（机器人自身标识）。
// 规范要求事件携带 self.user_id 标识机器人；部分实现仍发顶层 self_id，两者都兼容。
type SelfV12 struct {
	Platform string `json:"platform"`
	UserID   string `json:"user_id"`
}

// ResolveSelfID 解析机器人自身 ID：优先顶层 self_id，其次 self.user_id（OneBot 12 规范）。
//
// 返回：机器人自身 ID；两处均为空时返回空字符串。
func (e *EventV12) ResolveSelfID() string {
	if e.SelfID != "" {
		return e.SelfID
	}
	return e.Self.UserID
}

// EventV11 表示 OneBot 11 风格事件（NapCat 方言）。
//
// 注意：
//   - Message 使用 json.RawMessage 而非具体类型：API 响应的 "message":""（string）
//     与事件 "message":[{...}]（array）冲突，解析统一走 ParseMessageSegments。
//   - 顶层 ID（self_id / user_id / group_id / message_id）是 JSON number（int64）；
//     消息段 data 内的值可能是 number 或 string，经 ParseSegmentsV11 统一转字符串。
//   - 与 OneBot 12 的字段差异：at 段目标在 qq 字段（V12 为 user_id），引用段用 id
//     字段（V12 为 message_id）。
type EventV11 struct {
	Time        int64           `json:"time"`
	SelfID      int64           `json:"self_id"`
	PostType    string          `json:"post_type"`              // message / notice / request / meta_event
	MessageType string          `json:"message_type,omitempty"` // private / group
	SubType     string          `json:"sub_type,omitempty"`
	UserID      int64           `json:"user_id,omitempty"`
	GroupID     int64           `json:"group_id,omitempty"`
	Message     json.RawMessage `json:"message,omitempty"` // MessageSegmentV11 数组或字符串
	RawMessage  string          `json:"raw_message,omitempty"`
	Sender      SenderV11       `json:"sender,omitempty"`
	// 消息 ID（NapCat 扩展）
	MessageID  int64 `json:"message_id,omitempty"`
	MessageSeq int64 `json:"message_seq,omitempty"`

	NoticeType string `json:"notice_type,omitempty"` // group_increase / group_decrease / notify / ...
	OperatorID int64  `json:"operator_id,omitempty"` // 操作者（拉人者/禁言管理员/戳人者）
	TargetID   int64  `json:"target_id,omitempty"`   // 被操作者（poke 被戳者）
	Duration   int64  `json:"duration,omitempty"`    // 禁言时长（秒）

	RequestType string `json:"request_type,omitempty"` // friend / group（好友请求 / 加群请求）
}

// ParseMessageSegments 将 Message 字段解析为 OneBot 11 消息段列表。
// 兼容三种格式：消息段数组、空字符串（API 响应中的 ""）、CQ 码字符串（如 "[CQ:at,qq=123,name=张三]你好"）。
//
// 返回：解析出的消息段列表；Message 为空、null、空字符串或无法识别时返回 nil。
func (e *EventV11) ParseMessageSegments() []MessageSegmentV11 {
	if len(e.Message) == 0 {
		return nil
	}
	var segments []MessageSegmentV11
	if err := json.Unmarshal(e.Message, &segments); err == nil {
		return segments
	}
	// 非数组格式按 CQ 码字符串解析（NapCat 部分配置下 message 字段直接是 raw_message）
	var raw string
	if err := json.Unmarshal(e.Message, &raw); err == nil && raw != "" {
		return ParseCQSegmentsV11(raw)
	}
	return nil
}

// SenderV11 表示 OneBot 11 消息发送者。
type SenderV11 struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Sex      string `json:"sex,omitempty"`
	Age      int    `json:"age,omitempty"`
	Card     string `json:"card,omitempty"` // 群名片
	Role     string `json:"role,omitempty"` // owner / admin / member
}
