package gateway

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ── OneBot 12 消息段 ──

// MessageSegmentV12 表示 OneBot 12 消息段
type MessageSegmentV12 struct {
	Type string         `json:"type"` // text / image / at / reply / ...
	Data map[string]any `json:"data"`
}

// TextContent 提取消息段的纯文本内容
func (m MessageSegmentV12) TextContent() string {
	switch m.Type {
	case "text":
		if s, ok := m.Data["text"].(string); ok {
			return s
		}
	case "at":
		if uid, ok := m.Data["user_id"].(string); ok {
			return "@" + uid
		}
	}
	return ""
}

// ExtractPlainText 从 OneBot 12 消息段列表提取纯文本
func ExtractPlainTextV12(segments []MessageSegmentV12) string {
	var parts []string
	for _, seg := range segments {
		if t := seg.TextContent(); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "")
}

// ── OneBot 11 消息段 ──

// MessageSegmentV11 表示 OneBot 11 消息段（NapCat 方言）
type MessageSegmentV11 struct {
	Type string         `json:"type"` // text / image / at / face / reply / ...
	Data map[string]any `json:"data"`
}

// TextContent 提取消息段的纯文本内容
func (m MessageSegmentV11) TextContent() string {
	switch m.Type {
	case "text":
		if s, ok := m.Data["text"].(string); ok {
			return s
		}
	case "at":
		// NapCat 的 at 消息段，qq 字段可能是 number 或 string
		if qq := m.Data["qq"]; qq != nil {
			return "@" + formatIntOrString(qq)
		}
	}
	return ""
}

// ExtractPlainTextV11 从 OneBot 11 消息段列表提取纯文本
func ExtractPlainTextV11(segments []MessageSegmentV11) string {
	var parts []string
	for _, seg := range segments {
		if t := seg.TextContent(); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "")
}

// ── 标准化消息 ──

// NormalizedMessage 是跨平台标准化后的消息，供 bot 层消费
type NormalizedMessage struct {
	Platform   Platform // 来源平台
	Protocol   Protocol // 协议版本
	SelfID     string   // 机器人自身 ID
	UserID     string   // 平台用户 ID（字符串，跨平台）
	GroupID    string   // 平台群 ID（空字符串 = 私聊）
	IsGroup    bool     // 是否群消息
	Content    string   // 纯文本内容
	SenderName string   // 发送者昵称
	MessageID  string   // 消息 ID
	ConnID     string   // 来源连接 ID（用于回复路由）
}

// NormalizeV12 将 OneBot 12 事件标准化为 NormalizedMessage
func NormalizeV12(connID string, evt *EventV12, platform Platform) *NormalizedMessage {
	if evt.Type != "message" {
		return nil
	}

	userID := evt.UserID
	groupID := evt.GroupID
	isGroup := evt.DetailType == "group"

	content := evt.AltMessage
	if content == "" {
		content = ExtractPlainTextV12(evt.Message)
	}

	// 如果事件平台字段有效，优先使用事件中的平台标识
	p := platform
	if evt.Platform != "" {
		p = Platform(evt.Platform)
	}

	return &NormalizedMessage{
		Platform:   p,
		Protocol:   ProtocolV12,
		SelfID:     evt.SelfID,
		UserID:     userID,
		GroupID:    groupID,
		IsGroup:    isGroup,
		Content:    content,
		SenderName: "", // OB12 消息事件不直接包含 sender nickname，需从 sender 子对象获取（如有）
		MessageID:  evt.ID,
		ConnID:     connID,
	}
}

// NormalizeV11 将 OneBot 11 事件标准化为 NormalizedMessage
func NormalizeV11(connID string, evt *EventV11, platform Platform) *NormalizedMessage {
	if evt.PostType != "message" {
		return nil
	}

	userID := strconv.FormatInt(evt.UserID, 10)
	groupID := ""
	isGroup := evt.MessageType == "group"
	if isGroup {
		groupID = strconv.FormatInt(evt.GroupID, 10)
	}

	content := evt.RawMessage
	if content == "" {
		content = ExtractPlainTextV11(evt.ParseMessageSegments())
	}

	senderName := evt.Sender.Nickname
	if isGroup && evt.Sender.Card != "" {
		senderName = evt.Sender.Card
	}

	messageID := strconv.FormatInt(evt.MessageID, 10)

	return &NormalizedMessage{
		Platform:   platform,
		Protocol:   ProtocolV11,
		SelfID:     strconv.FormatInt(evt.SelfID, 10),
		UserID:     userID,
		GroupID:    groupID,
		IsGroup:    isGroup,
		Content:    content,
		SenderName: senderName,
		MessageID:  messageID,
		ConnID:     connID,
	}
}

// ── 动作请求/响应 ──

// ActionRequest 表示要发送给 OneBot 实现的动作请求
type ActionRequest struct {
	Action string         `json:"action"`         // 动作名，如 send_message / send_group_msg
	Params map[string]any `json:"params"`         // 动作参数
	Echo   string         `json:"echo,omitempty"` // 请求标识（用于匹配响应）
}

// ActionResponse 表示 OneBot 实现返回的动作响应
type ActionResponse struct {
	Status  string          `json:"status"`            // ok / failed
	RetCode int64           `json:"retcode"`           // 返回码
	Data    json.RawMessage `json:"data"`              // 响应数据
	Message string          `json:"message,omitempty"` // 错误信息
	Echo    string          `json:"echo,omitempty"`    // 请求标识
}

// ── 构建动作辅助 ──

// BuildSendMessageV12 构建 OneBot 12 send_message 动作
func BuildSendMessageV12(detailType, userID, groupID, text string) *ActionRequest {
	params := map[string]any{
		"detail_type": detailType, // private / group
		"message": []MessageSegmentV12{
			{Type: "text", Data: map[string]any{"text": text}},
		},
	}
	if detailType == "private" {
		params["user_id"] = userID
	} else {
		params["group_id"] = groupID
	}
	return &ActionRequest{
		Action: "send_message",
		Params: params,
	}
}

// BuildSendMessageV11 构建 OneBot 11 send_private_msg / send_group_msg 动作
func BuildSendMessageV11(isGroup bool, userID, groupID, text string) *ActionRequest {
	if isGroup {
		gid, _ := strconv.ParseInt(groupID, 10, 64)
		return &ActionRequest{
			Action: "send_group_msg",
			Params: map[string]any{
				"group_id": gid,
				"message": []MessageSegmentV11{
					{Type: "text", Data: map[string]any{"text": text}},
				},
			},
		}
	}
	uid, _ := strconv.ParseInt(userID, 10, 64)
	return &ActionRequest{
		Action: "send_private_msg",
		Params: map[string]any{
			"user_id": uid,
			"message": []MessageSegmentV11{
				{Type: "text", Data: map[string]any{"text": text}},
			},
		},
	}
}

// ── 辅助函数 ──

// formatIntOrString 将值格式化为字符串（处理 JSON number 或 string）
func formatIntOrString(v any) string {
	switch n := v.(type) {
	case json.Number:
		return n.String()
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case int64:
		return strconv.FormatInt(n, 10)
	case string:
		return n
	default:
		return fmt.Sprintf("%v", v)
	}
}
