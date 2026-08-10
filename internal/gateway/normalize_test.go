package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// v12Evt 构造 OneBot 12 事件。
func v12Evt(id, typ, detail, sub string) *EventV12 {
	return &EventV12{
		ID:         id,
		Impl:       "test",
		Platform:   "qq",
		SelfID:     "10001",
		Type:       typ,
		DetailType: detail,
		SubType:    sub,
	}
}

// ── V12 notice 事件映射 ──

func TestNormalizeV12_NoticeMappings(t *testing.T) {
	cases := []struct {
		name      string
		detail    string
		sub       string
		wantType  string
		wantSub   string
		wantGroup string
		wantData  map[string]string // 抽查字段
	}{
		{
			name: "群成员增加(invite)", detail: "group_member_increase", sub: "invite",
			wantType: EventTypeGroupIncrease, wantSub: "invite",
			wantData: map[string]string{"user_id": "22222", "group_id": "33333", "operator_id": "44444"},
		},
		{
			name: "群成员减少(kick)", detail: "group_member_decrease", sub: "kick",
			wantType: EventTypeGroupDecrease, wantSub: "kick",
		},
		{
			name: "好友增加", detail: "friend_increase", sub: "",
			wantType: EventTypeFriendIncrease,
		},
		{
			name: "撤回-规范detail", detail: "group_message_delete", sub: "recall",
			wantType: EventTypeGroupRecall, wantSub: "recall",
		},
		{
			name: "撤回-旧detail", detail: "group_recall", sub: "recall",
			wantType: EventTypeGroupRecall, wantSub: "recall",
		},
		{
			name: "禁言", detail: "group_ban", sub: "ban",
			wantType: EventTypeGroupBan, wantSub: "ban",
		},
		{
			name: "解禁-lift_ban", detail: "group_ban", sub: "lift_ban",
			wantType: EventTypeGroupUnban, wantSub: "lift_ban",
		},
		{
			name: "解禁-unban兼容", detail: "group_ban", sub: "unban",
			wantType: EventTypeGroupUnban, wantSub: "unban",
		},
		{
			name: "poke", detail: "poke", sub: "",
			wantType: EventTypePoke,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evt := v12Evt("e1", "notice", tc.detail, tc.sub)
			evt.UserID = "22222"
			evt.GroupID = "33333"
			evt.OperatorID = "44444"
			msg := NormalizeV12("c1", evt, PlatformQQ)
			if msg == nil {
				t.Fatalf("期望非 nil，got nil")
			}
			if msg.MessageType != MessageTypeNotice {
				t.Errorf("MessageType = %q, want notice", msg.MessageType)
			}
			if msg.EventType != tc.wantType {
				t.Errorf("EventType = %q, want %q", msg.EventType, tc.wantType)
			}
			if msg.EventSubType != tc.wantSub {
				t.Errorf("EventSubType = %q, want %q", msg.EventSubType, tc.wantSub)
			}
			if msg.GroupID != "33333" || !msg.IsGroup {
				t.Errorf("GroupID/IsGroup 错误: %q %v", msg.GroupID, msg.IsGroup)
			}
			for k, want := range tc.wantData {
				if got := msg.EventData[k]; got != want {
					t.Errorf("EventData[%q] = %v, want %q", k, got, want)
				}
			}
		})
	}
}

func TestNormalizeV12_NoticeWhitelistRejectsUnknown(t *testing.T) {
	evt := v12Evt("e1", "notice", "group_admin", "set") // 不在白名单
	evt.UserID = "22222"
	evt.GroupID = "33333"
	if msg := NormalizeV12("c1", evt, PlatformQQ); msg != nil {
		t.Fatalf("白名单外事件应返回 nil，got %+v", msg)
	}
}

func TestNormalizeV12_SelfIncreaseFiltered(t *testing.T) {
	// 机器人自己被拉入群：user_id == self_id，应丢弃
	evt := v12Evt("e1", "notice", "group_member_increase", "invite")
	evt.UserID = "10001" // 等于 SelfID
	evt.GroupID = "33333"
	if msg := NormalizeV12("c1", evt, PlatformQQ); msg != nil {
		t.Fatalf("机器人自入群应被丢弃，got %+v", msg)
	}
}

func TestNormalizeV12_SelfObjectFallback(t *testing.T) {
	// 规范 wire 格式：self.user_id 而非顶层 self_id
	evt := &EventV12{
		ID:         "e1",
		Type:       "notice",
		DetailType: "group_member_increase",
		SubType:    "invite",
		Self:       SelfV12{Platform: "qq", UserID: "10001"},
		UserID:     "10001", // 机器人自己被拉入群
		GroupID:    "33333",
	}
	if msg := NormalizeV12("c1", evt, PlatformQQ); msg != nil {
		t.Fatalf("self.user_id 解析下自入群应被丢弃，got %+v", msg)
	}
}

func TestNormalizeV12_Request(t *testing.T) {
	evt := v12Evt("e1", "request", "group.invite", "")
	evt.UserID = "22222"
	evt.GroupID = "33333"
	msg := NormalizeV12("c1", evt, PlatformQQ)
	if msg == nil {
		t.Fatal("request 事件不应为 nil")
	}
	if msg.MessageType != MessageTypeRequest {
		t.Errorf("MessageType = %q, want request", msg.MessageType)
	}
	if msg.EventType != "group.invite" {
		t.Errorf("EventType = %q, want group.invite", msg.EventType)
	}
}

func TestNormalizeV12_MetaReturnsNil(t *testing.T) {
	if msg := NormalizeV12("c1", v12Evt("e1", "meta", "connect", ""), PlatformQQ); msg != nil {
		t.Fatalf("meta 事件应返回 nil，got %+v", msg)
	}
}

// ── V12 普通消息 ──

func TestNormalizeV12_Message(t *testing.T) {
	evt := v12Evt("m1", "message", "group", "")
	evt.UserID = "22222"
	evt.GroupID = "33333"
	evt.Message = []MessageSegmentV12{
		{Type: "text", Data: map[string]any{"text": "你好"}},
		{Type: "at", Data: map[string]any{"user_id": "10001", "name": "蓝妹"}},
		{Type: "image", Data: map[string]any{"file": "a.png"}},
	}
	evt.AltMessage = "你好@蓝妹"
	msg := NormalizeV12("c1", evt, PlatformQQ)
	if msg == nil {
		t.Fatal("message 事件不应为 nil")
	}
	if msg.MessageType != MessageTypeMessage {
		t.Errorf("MessageType = %q, want message", msg.MessageType)
	}
	if msg.EventType != "" || msg.EventData != nil {
		t.Errorf("普通消息事件字段应为空，got %q %v", msg.EventType, msg.EventData)
	}
	if len(msg.AtTargets) != 1 || msg.AtTargets[0] != "10001" {
		t.Errorf("AtTargets = %v, want [10001]", msg.AtTargets)
	}
	if len(msg.MimeTypes) != 1 || msg.MimeTypes[0] != "image/png" {
		t.Errorf("MimeTypes = %v, want [image/png]", msg.MimeTypes)
	}
	if msg.Content != "你好@蓝妹" {
		t.Errorf("Content = %q, want 你好@蓝妹", msg.Content)
	}
	if msg.SelfID != "10001" {
		t.Errorf("SelfID = %q, want 10001", msg.SelfID)
	}
}

// ── V11 事件 ──

func v11Evt(postType, noticeType string) *EventV11 {
	return &EventV11{
		Time:       1754700000,
		SelfID:     10001,
		PostType:   postType,
		NoticeType: noticeType,
	}
}

func TestNormalizeV11_NoticeMappings(t *testing.T) {
	cases := []struct {
		name     string
		notice   string
		sub      string
		wantType string
	}{
		{"群成员增加", "group_increase", "invite", EventTypeGroupIncrease},
		{"群成员减少", "group_decrease", "kick", EventTypeGroupDecrease},
		{"好友添加", "friend_add", "", EventTypeFriendIncrease},
		{"消息撤回", "group_recall", "", EventTypeGroupRecall},
		{"禁言", "group_ban", "ban", EventTypeGroupBan},
		{"解禁", "group_ban", "lift_ban", EventTypeGroupUnban},
		{"戳一戳", "notify", "poke", EventTypePoke},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evt := v11Evt("notice", tc.notice)
			evt.SubType = tc.sub
			evt.UserID = 22222
			evt.GroupID = 33333
			evt.OperatorID = 44444
			msg := NormalizeV11("c1", evt, PlatformNapCat)
			if msg == nil {
				t.Fatalf("期望非 nil，got nil")
			}
			if msg.MessageType != MessageTypeNotice {
				t.Errorf("MessageType = %q, want notice", msg.MessageType)
			}
			if msg.EventType != tc.wantType {
				t.Errorf("EventType = %q, want %q", msg.EventType, tc.wantType)
			}
			if msg.GroupID != "33333" || !msg.IsGroup {
				t.Errorf("GroupID/IsGroup 错误: %q %v", msg.GroupID, msg.IsGroup)
			}
			if got := msg.EventData["user_id"]; got != "22222" {
				t.Errorf("EventData[user_id] = %v, want 22222", got)
			}
			if got := msg.EventData["operator_id"]; got != "44444" {
				t.Errorf("EventData[operator_id] = %v, want 44444", got)
			}
		})
	}
}

func TestNormalizeV11_NotifyNonPokeRejected(t *testing.T) {
	// notify 但 sub_type 不是 poke（如 honor），白名单外 → nil
	evt := v11Evt("notice", "notify")
	evt.SubType = "honor"
	if msg := NormalizeV11("c1", evt, PlatformNapCat); msg != nil {
		t.Fatalf("notify/honor 应返回 nil，got %+v", msg)
	}
}

func TestNormalizeV11_SelfIncreaseFiltered(t *testing.T) {
	evt := v11Evt("notice", "group_increase")
	evt.UserID = 10001 // 等于 SelfID（机器人自己）
	evt.GroupID = 33333
	if msg := NormalizeV11("c1", evt, PlatformNapCat); msg != nil {
		t.Fatalf("机器人自入群应被丢弃，got %+v", msg)
	}
}

func TestNormalizeV11_Request(t *testing.T) {
	evt := v11Evt("request", "")
	evt.RequestType = "group"
	evt.UserID = 22222
	evt.GroupID = 33333
	msg := NormalizeV11("c1", evt, PlatformNapCat)
	if msg == nil {
		t.Fatal("request 事件不应为 nil")
	}
	if msg.MessageType != MessageTypeRequest {
		t.Errorf("MessageType = %q, want request", msg.MessageType)
	}
	if msg.EventType != "group" {
		t.Errorf("EventType = %q, want group", msg.EventType)
	}
	if got := msg.EventData["request_type"]; got != "group" {
		t.Errorf("EventData[request_type] = %v, want group", got)
	}
}

func TestNormalizeV11_GroupMessageFallback(t *testing.T) {
	// message_type 缺失时按 group_id 兜底判群聊
	evt := v11Evt("message", "")
	evt.UserID = 22222
	evt.GroupID = 33333
	evt.RawMessage = "hello"
	msg := NormalizeV11("c1", evt, PlatformNapCat)
	if msg == nil {
		t.Fatal("message 事件不应为 nil")
	}
	if !msg.IsGroup || msg.GroupID != "33333" {
		t.Errorf("群聊兜底失败: IsGroup=%v GroupID=%q", msg.IsGroup, msg.GroupID)
	}
}

func TestNormalizeV11_PrivateMessage(t *testing.T) {
	evt := v11Evt("message", "")
	evt.MessageType = "private"
	evt.UserID = 22222
	evt.RawMessage = "hello"
	msg := NormalizeV11("c1", evt, PlatformNapCat)
	if msg == nil {
		t.Fatal("message 事件不应为 nil")
	}
	if msg.IsGroup || msg.GroupID != "" {
		t.Errorf("私聊不应为群聊: IsGroup=%v GroupID=%q", msg.IsGroup, msg.GroupID)
	}
}

// TestNormalizeV11_MediaOnlyKeepsEmptyContent 纯媒体消息（如图片）Content 保持空，
// 不 fallback 为原始 CQ 码（避免意图/话题层被 CQ 码污染，媒体走媒体管线）。
func TestNormalizeV11_MediaOnlyKeepsEmptyContent(t *testing.T) {
	evt := v11Evt("message", "")
	evt.MessageType = "group"
	evt.UserID = 2812899726
	evt.GroupID = 1055835299
	evt.SelfID = 3303679079
	evt.RawMessage = "[CQ:image,file=C6A1.png,url=https://example.com/a.png]"
	evt.Message = json.RawMessage(`""`)

	msg := NormalizeV11("c1", evt, PlatformNapCat)
	if msg == nil {
		t.Fatal("message 事件不应为 nil")
	}
	if msg.Content != "" {
		t.Errorf("纯媒体消息 Content = %q, want 空（不 fallback 为 CQ 码）", msg.Content)
	}
	if len(msg.Segments) != 1 || msg.Segments[0].Type != "image" {
		t.Errorf("Segments = %+v, want 1 个 image 段", msg.Segments)
	}
	if len(msg.MimeTypes) != 1 || msg.MimeTypes[0] != "image/png" {
		t.Errorf("MimeTypes = %v, want [image/png]", msg.MimeTypes)
	}
}

func TestNormalizeV11_MetaReturnsNil(t *testing.T) {
	if msg := NormalizeV11("c1", v11Evt("meta_event", ""), PlatformNapCat); msg != nil {
		t.Fatalf("meta_event 应返回 nil，got %+v", msg)
	}
}

// ── CQ 码字符串解析（raw_message）──

func TestParseCQSegmentsV11(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantSegs []MessageSegmentV11
	}{
		{
			name: "纯文本",
			raw:  "你好",
			wantSegs: []MessageSegmentV11{
				{Type: "text", Data: map[string]any{"text": "你好"}},
			},
		},
		{
			name: "at+文本混排",
			raw:  "[CQ:at,qq=3303679079,name=蓝妹]我喜欢你",
			wantSegs: []MessageSegmentV11{
				{Type: "at", Data: map[string]any{"qq": "3303679079", "name": "蓝妹"}},
				{Type: "text", Data: map[string]any{"text": "我喜欢你"}},
			},
		},
		{
			name: "文本在前+at",
			raw:  "大家看[CQ:at,qq=123,name=张三]来了",
			wantSegs: []MessageSegmentV11{
				{Type: "text", Data: map[string]any{"text": "大家看"}},
				{Type: "at", Data: map[string]any{"qq": "123", "name": "张三"}},
				{Type: "text", Data: map[string]any{"text": "来了"}},
			},
		},
		{
			name: "值内转义逗号",
			raw:  "[CQ:reply,id=1,text=你好&#44;世界]ok",
			wantSegs: []MessageSegmentV11{
				{Type: "reply", Data: map[string]any{"id": "1", "text": "你好,世界"}},
				{Type: "text", Data: map[string]any{"text": "ok"}},
			},
		},
		{
			name: "文本转义",
			raw:  "a&amp;b&#91;c&#93;",
			wantSegs: []MessageSegmentV11{
				{Type: "text", Data: map[string]any{"text": "a&b[c]"}},
			},
		},
		{
			name: "未闭合CQ码按文本",
			raw:  "前缀[CQ:at,qq=123",
			wantSegs: []MessageSegmentV11{
				{Type: "text", Data: map[string]any{"text": "前缀"}},
				{Type: "text", Data: map[string]any{"text": "[CQ:at,qq=123"}},
			},
		},
		{
			name: "at全体",
			raw:  "[CQ:at,qq=all]集合",
			wantSegs: []MessageSegmentV11{
				{Type: "at", Data: map[string]any{"qq": "all"}},
				{Type: "text", Data: map[string]any{"text": "集合"}},
			},
		},
		{
			name: "空串",
			raw:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseCQSegmentsV11(tc.raw)
			if len(got) != len(tc.wantSegs) {
				t.Fatalf("ParseCQSegmentsV11(%q) 段数 = %d, want %d: %+v", tc.raw, len(got), len(tc.wantSegs), got)
			}
			for i := range got {
				if got[i].Type != tc.wantSegs[i].Type {
					t.Errorf("段[%d] Type = %q, want %q", i, got[i].Type, tc.wantSegs[i].Type)
				}
				for k, v := range tc.wantSegs[i].Data {
					if gotV, ok := got[i].Data[k]; !ok || gotV != v {
						t.Errorf("段[%d] Data[%q] = %v, want %v", i, k, gotV, v)
					}
				}
			}
		})
	}
}

// TestNormalizeV11_CQCodeContent 验证 CQ 码消息被标准化为纯文本，且 at 目标为平台 ID。
func TestNormalizeV11_CQCodeContent(t *testing.T) {
	evt := v11Evt("message", "")
	evt.MessageType = "group"
	evt.UserID = 2812899726
	evt.GroupID = 1055835299
	evt.SelfID = 3303679079
	evt.RawMessage = "[CQ:at,qq=3303679079,name=蓝妹]蓝莓我喜欢你"
	evt.Message = json.RawMessage(`""`) // message 字段为空字符串，回退用 raw_message 解析

	msg := NormalizeV11("c1", evt, PlatformNapCat)
	if msg == nil {
		t.Fatal("message 事件不应为 nil")
	}
	// Content 必须是纯文本，不能含原始 CQ 码
	if strings.Contains(msg.Content, "[CQ:") {
		t.Fatalf("Content 含原始 CQ 码: %q", msg.Content)
	}
	if msg.Content != "@蓝妹蓝莓我喜欢你" {
		t.Errorf("Content = %q, want @蓝妹蓝莓我喜欢你", msg.Content)
	}
	if len(msg.AtTargets) != 1 || msg.AtTargets[0] != "3303679079" {
		t.Errorf("AtTargets = %v, want [3303679079]（平台 ID）", msg.AtTargets)
	}
	if len(msg.Segments) != 2 {
		t.Errorf("Segments 段数 = %d, want 2", len(msg.Segments))
	}
}

// TestNormalizeV11_ArraySegmentsPlainContent 验证 message 为数组时 Content 同样使用纯文本。
func TestNormalizeV11_ArraySegmentsPlainContent(t *testing.T) {
	evt := v11Evt("message", "")
	evt.MessageType = "group"
	evt.UserID = 2812899726
	evt.GroupID = 1055835299
	evt.SelfID = 3303679079
	evt.RawMessage = "[CQ:at,qq=3303679079,name=蓝妹]在吗" // 与数组不一致，数组优先
	evt.Message = json.RawMessage(`[{"type":"at","data":{"qq":"3303679079","name":"蓝妹"}},{"type":"text","data":{"text":"在吗"}}]`)

	msg := NormalizeV11("c1", evt, PlatformNapCat)
	if msg == nil {
		t.Fatal("message 事件不应为 nil")
	}
	if strings.Contains(msg.Content, "[CQ:") {
		t.Fatalf("Content 含原始 CQ 码: %q", msg.Content)
	}
	if msg.Content != "@蓝妹在吗" {
		t.Errorf("Content = %q, want @蓝妹在吗", msg.Content)
	}
	if len(msg.AtTargets) != 1 || msg.AtTargets[0] != "3303679079" {
		t.Errorf("AtTargets = %v, want [3303679079]", msg.AtTargets)
	}
}
