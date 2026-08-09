package gateway

import "strconv"

// ── 规范化事件类型 ──
//
// 通知事件经 NormalizeV12/V11 归一化后的统一类型名（写入 NormalizedMessage.EventType）。
// 覆盖全部常见 notice 事件（进群/退群/好友/戳一戳/撤回/禁言/解禁）；
// 后续新增事件类型时在此补充常量与映射即可。
const (
	EventTypeGroupIncrease  = "group_increase"  // 新人入群
	EventTypeGroupDecrease  = "group_decrease"  // 退群/被踢
	EventTypeFriendIncrease = "friend_increase" // 好友添加
	EventTypePoke           = "poke"            // 戳一戳
	EventTypeGroupRecall    = "group_recall"    // 消息撤回
	EventTypeGroupBan       = "group_ban"       // 禁言
	EventTypeGroupUnban     = "group_unban"     // 解除禁言
)

// mapNoticeV12 将 OneBot 12 通知事件的 detail_type 映射为规范化事件类型。
// 返回 (类型, 是否支持)；不支持的事件返回 ("", false)，由调用方丢弃。
func mapNoticeV12(evt *EventV12) (string, bool) {
	switch evt.DetailType {
	case "group_member_increase":
		return EventTypeGroupIncrease, true
	case "group_member_decrease":
		return EventTypeGroupDecrease, true
	case "friend_increase":
		return EventTypeFriendIncrease, true
	case "group_recall", "group_message_delete":
		// 兼容两套 OneBot 12 实现：规范 detail_type 为 group_message_delete，
		// 部分实现仍沿用早期 draft 的 group_recall
		return EventTypeGroupRecall, true
	case "group_ban":
		// 子类型兼容 lift_ban（规范/OneBot 11 沿用）与 unban（部分实现扩展）
		if evt.SubType == "lift_ban" || evt.SubType == "unban" {
			return EventTypeGroupUnban, true
		}
		return EventTypeGroupBan, true
	case "poke":
		return EventTypePoke, true
	}
	return "", false
}

// mapNoticeV11 将 OneBot 11 通知事件的 notice_type/sub_type 映射为规范化事件类型。
// 返回 (类型, 是否支持)；不支持的事件返回 ("", false)，由调用方丢弃。
func mapNoticeV11(evt *EventV11) (string, bool) {
	switch evt.NoticeType {
	case "group_increase":
		return EventTypeGroupIncrease, true
	case "group_decrease":
		return EventTypeGroupDecrease, true
	case "friend_add":
		return EventTypeFriendIncrease, true
	case "group_recall":
		return EventTypeGroupRecall, true
	case "group_ban":
		if evt.SubType == "lift_ban" {
			return EventTypeGroupUnban, true
		}
		return EventTypeGroupBan, true
	case "notify":
		if evt.SubType == "poke" {
			return EventTypePoke, true
		}
	}
	return "", false
}

// normalizeNoticeV12 将 OneBot 12 通知事件标准化为事件三元组。
// 由 NormalizeV12 在 Type == "notice" 时调用；仅接收白名单内的事件（见 mapNoticeV12）。
// 返回 (事件类型, 事件子类型, 事件数据, 是否支持)。
func normalizeNoticeV12(evt *EventV12) (eventType, subType string, data map[string]any, ok bool) {
	eventType, ok = mapNoticeV12(evt)
	if !ok {
		return "", "", nil, false
	}
	// 机器人自入群（被拉进自己的群），丢弃
	if eventType == EventTypeGroupIncrease && evt.UserID == evt.ResolveSelfID() {
		return "", "", nil, false
	}
	return eventType, evt.SubType, noticeEventDataV12(evt), true
}

// normalizeNoticeV11 将 OneBot 11 通知事件标准化为事件三元组。
// 由 NormalizeV11 在 PostType == "notice" 时调用；仅接收白名单内的事件（见 mapNoticeV11）。
func normalizeNoticeV11(evt *EventV11) (eventType, subType string, data map[string]any, ok bool) {
	eventType, ok = mapNoticeV11(evt)
	if !ok {
		return "", "", nil, false
	}
	// 机器人自入群（被拉进自己的群），丢弃
	if eventType == EventTypeGroupIncrease && evt.UserID == evt.SelfID {
		return "", "", nil, false
	}
	return eventType, evt.SubType, noticeEventDataV11(evt), true
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
		data["operator_id"] = evt.OperatorID // 操作者（拉人者/禁言管理员）
	}
	if evt.TargetID != "" {
		data["target_id"] = evt.TargetID // 被操作者（poke 被戳者）
	}
	if evt.Duration > 0 {
		data["duration"] = evt.Duration // 禁言时长（秒）
	}
	if evt.MessageID != "" {
		data["message_id"] = evt.MessageID // 撤回的消息 ID
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
		data["user_id"] = v
	}
	if v := formatID(evt.GroupID); v != "" {
		data["group_id"] = v
	}
	if v := formatID(evt.OperatorID); v != "" {
		data["operator_id"] = v
	}
	if v := formatID(evt.TargetID); v != "" {
		data["target_id"] = v
	}
	if evt.Duration > 0 {
		data["duration"] = evt.Duration
	}
	if v := formatID(evt.MessageID); v != "" {
		data["message_id"] = v
	}
	if evt.SubType != "" {
		data["sub_type"] = evt.SubType
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
