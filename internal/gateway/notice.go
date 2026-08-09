package gateway

import "strconv"

// ── 规范化事件类型 ──
//
// 通知事件经 NormalizeV12/V11 归一化后的统一类型名（写入 NormalizedMessage.EventType）。
// 目前仅支持"新人入群"（group_increase），后续新增事件类型时在此补充常量与映射即可。
const (
	EventTypeGroupIncrease = "group_increase" // 新人入群
)

// mapNoticeV12 将 OneBot 12 通知事件的 detail_type 映射为规范化事件类型。
// 返回 (类型, 是否支持)；不支持的事件返回 ("", false)，由调用方丢弃。
func mapNoticeV12(evt *EventV12) (string, bool) {
	switch evt.DetailType {
	case "group.increase":
		return EventTypeGroupIncrease, true
	}
	return "", false
}

// mapNoticeV11 将 OneBot 11 通知事件的 notice_type 映射为规范化事件类型。
// 返回 (类型, 是否支持)；不支持的事件返回 ("", false)，由调用方丢弃。
func mapNoticeV11(evt *EventV11) (string, bool) {
	switch evt.NoticeType {
	case "group_increase":
		return EventTypeGroupIncrease, true
	}
	return "", false
}

// normalizeNoticeV12 将 OneBot 12 通知事件标准化为 NormalizedMessage。
// 由 NormalizeV12 在 Type != "message" 时调用；仅接收白名单内的事件（见 mapNoticeV12），
// 白名单外的事件（request / meta / 其他 notice 类型）返回 nil。
func normalizeNoticeV12(connID string, evt *EventV12, platform Platform) *NormalizedMessage {
	if evt.Type != "notice" {
		return nil
	}
	eventType, ok := mapNoticeV12(evt)
	if !ok {
		return nil
	}
	// 机器人自入群（被拉进自己的群），丢弃
	if eventType == EventTypeGroupIncrease && evt.UserID == evt.SelfID {
		return nil
	}

	// 如果事件平台字段有效，优先使用事件中的平台标识
	p := platform
	if evt.Platform != "" {
		p = Platform(evt.Platform)
	}

	return &NormalizedMessage{
		Platform:     p,
		Protocol:     ProtocolV12,
		SelfID:       evt.SelfID,
		UserID:       evt.UserID,
		GroupID:      evt.GroupID,
		IsGroup:      evt.GroupID != "",
		EventType:    eventType,
		EventSubType: evt.SubType,
		EventData:    noticeEventDataV12(evt),
		ConnID:       connID,
	}
}

// normalizeNoticeV11 将 OneBot 11 通知事件标准化为 NormalizedMessage。
// 由 NormalizeV11 在 PostType != "message" 时调用；仅接收白名单内的事件（见 mapNoticeV11），
// 白名单外的事件（request / meta / 其他 notice 类型）返回 nil。
func normalizeNoticeV11(connID string, evt *EventV11, platform Platform) *NormalizedMessage {
	if evt.PostType != "notice" {
		return nil
	}
	eventType, ok := mapNoticeV11(evt)
	if !ok {
		return nil
	}
	// 机器人自入群（被拉进自己的群），丢弃
	if eventType == EventTypeGroupIncrease && evt.UserID == evt.SelfID {
		return nil
	}

	return &NormalizedMessage{
		Platform:     platform,
		Protocol:     ProtocolV11,
		SelfID:       strconv.FormatInt(evt.SelfID, 10),
		UserID:       formatID(evt.UserID),
		GroupID:      formatID(evt.GroupID),
		IsGroup:      evt.GroupID != 0,
		EventType:    eventType,
		EventSubType: evt.SubType,
		EventData:    noticeEventDataV11(evt),
		ConnID:       connID,
	}
}

// noticeEventDataV12 提取 OneBot 12 事件特有业务字段（仅填充非空字段）。
func noticeEventDataV12(evt *EventV12) map[string]any {
	data := make(map[string]any)
	if evt.UserID != "" {
		data["user_id"] = evt.UserID // 事件主体（入群者）
	}
	if evt.GroupID != "" {
		data["group_id"] = evt.GroupID
	}
	if evt.OperatorID != "" {
		data["operator_id"] = evt.OperatorID // 操作者（拉人入群者）
	}
	if evt.SubType != "" {
		data["sub_type"] = evt.SubType // approve / invite
	}
	return data
}

// noticeEventDataV11 提取 OneBot 11 事件特有业务字段（数字 ID 转字符串，缺字段给空串）。
func noticeEventDataV11(evt *EventV11) map[string]any {
	data := make(map[string]any)
	if v := formatID(evt.UserID); v != "" {
		data["user_id"] = v // 事件主体（入群者）
	}
	if v := formatID(evt.GroupID); v != "" {
		data["group_id"] = v
	}
	if v := formatID(evt.OperatorID); v != "" {
		data["operator_id"] = v // 操作者（拉人入群者）
	}
	if evt.SubType != "" {
		data["sub_type"] = evt.SubType // approve / invite
	}
	return data
}

// formatID 将 OneBot 11 的数字 ID 转为字符串；0（缺字段）返回空串，避免出现 "0" 误导下游。
func formatID(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}
