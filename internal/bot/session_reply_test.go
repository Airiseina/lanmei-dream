package bot

import (
	"testing"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/gateway"
)

// msgFor 构造一条用于测试的标准化消息。
func msgFor(msgType gateway.MessageType, isGroup bool, platform gateway.Platform, groupID, userID, msgID string, at time.Time) *gateway.NormalizedMessage {
	return &gateway.NormalizedMessage{
		Platform:    platform,
		UserID:      userID,
		GroupID:     groupID,
		IsGroup:     isGroup,
		MessageID:   msgID,
		MessageType: msgType,
		ReceivedAt:  at,
	}
}

// TestSessionKey 会话键：群聊按 (平台,群ID)，私聊按 (平台,用户ID)。
func TestSessionKey(t *testing.T) {
	group := msgFor(gateway.MessageTypeMessage, true, gateway.PlatformQQ, "g123", "u1", "m1", time.Now())
	if k := sessionKey(group); k != "g:qq:g123" {
		t.Errorf("group sessionKey = %q, want %q", k, "g:qq:g123")
	}
	private := msgFor(gateway.MessageTypeMessage, false, gateway.PlatformQQ, "", "u1", "m1", time.Now())
	if k := sessionKey(private); k != "p:qq:u1" {
		t.Errorf("private sessionKey = %q, want %q", k, "p:qq:u1")
	}
	if sessionKey(nil) != "" {
		t.Errorf("nil sessionKey should be empty")
	}
}

// TestRecordAndDetectNewerMessages 验证"回复前会话已有新消息"的判定：
// 触发消息即最新 → 不引用；其后又有新消息 → 引用。
func TestRecordAndDetectNewerMessages(t *testing.T) {
	base := time.Now()
	b := &Bot{sessions: make(map[string]*sessionInfo)}

	trigger := msgFor(gateway.MessageTypeMessage, true, gateway.PlatformQQ, "g1", "uA", "m1", base.Add(-time.Minute))
	// 消息到达即记录（模拟 OnMessage 流程）
	b.recordSession(trigger)
	if b.hasNewerMessages(trigger) {
		t.Errorf("trigger is latest, should NOT quote")
	}

	// 回复生成期间新消息到达（同群不同人）
	newMsg := msgFor(gateway.MessageTypeMessage, true, gateway.PlatformQQ, "g1", "uB", "m2", base)
	b.recordSession(newMsg)
	if !b.hasNewerMessages(trigger) {
		t.Errorf("new message arrived after trigger, should quote")
	}

	// 新触发消息（针对新消息的回复）不应引用
	if b.hasNewerMessages(newMsg) {
		t.Errorf("newMsg is latest, should NOT quote")
	}
}

// TestRecordAndDetectPrivate 私聊场景的新消息判定。
func TestRecordAndDetectPrivate(t *testing.T) {
	base := time.Now()
	b := &Bot{sessions: make(map[string]*sessionInfo)}

	trigger := msgFor(gateway.MessageTypeMessage, false, gateway.PlatformQQ, "", "u1", "m1", base.Add(-time.Second))
	b.recordSession(trigger)
	if b.hasNewerMessages(trigger) {
		t.Errorf("private trigger is latest, should NOT quote")
	}

	follow := msgFor(gateway.MessageTypeMessage, false, gateway.PlatformQQ, "", "u1", "m2", base)
	b.recordSession(follow)
	if !b.hasNewerMessages(trigger) {
		t.Errorf("private follow-up arrived, should quote")
	}
}

// TestRecordSessionTimeFallback 消息 ID 缺失时退化为到达时间比较。
func TestRecordSessionTimeFallback(t *testing.T) {
	base := time.Now()
	b := &Bot{sessions: make(map[string]*sessionInfo)}

	trigger := msgFor(gateway.MessageTypeMessage, true, gateway.PlatformQQ, "g1", "uA", "", base.Add(-time.Minute))
	b.recordSession(trigger)
	if b.hasNewerMessages(trigger) {
		t.Errorf("no ID and no newer time, should NOT quote")
	}

	newMsg := msgFor(gateway.MessageTypeMessage, true, gateway.PlatformQQ, "g1", "uB", "", base)
	b.recordSession(newMsg)
	if !b.hasNewerMessages(trigger) {
		t.Errorf("newer time should quote")
	}
}

// TestRecordSessionOutOfOrder 乱序到达的旧消息不得覆盖新消息。
func TestRecordSessionOutOfOrder(t *testing.T) {
	base := time.Now()
	b := &Bot{sessions: make(map[string]*sessionInfo)}

	newMsg := msgFor(gateway.MessageTypeMessage, true, gateway.PlatformQQ, "g1", "uB", "m2", base)
	b.recordSession(newMsg)
	// 迟到的旧消息（时间更早）不得覆盖
	stale := msgFor(gateway.MessageTypeMessage, true, gateway.PlatformQQ, "g1", "uA", "m1", base.Add(-time.Minute))
	b.recordSession(stale)

	trigger := msgFor(gateway.MessageTypeMessage, true, gateway.PlatformQQ, "g1", "uA", "m0", base.Add(-2*time.Minute))
	if !b.hasNewerMessages(trigger) {
		t.Errorf("stale msg should NOT overwrite newer session record")
	}
}

// TestRecordSessionIgnoresNotice 通知事件不参与会话记录。
func TestRecordSessionIgnoresNotice(t *testing.T) {
	b := &Bot{sessions: make(map[string]*sessionInfo)}
	notice := msgFor(gateway.MessageTypeNotice, true, gateway.PlatformQQ, "g1", "uA", "n1", time.Now())
	b.recordSession(notice)
	if len(b.sessions) != 0 {
		t.Errorf("notice should not be recorded, sessions = %d", len(b.sessions))
	}
}

// TestRecordSessionIgnoresSelf 机器人自身消息（网关回显）不参与会话记录，
// 防止 bot 回复被记为"会话最新消息"导致后续回复全部误判为"已有新消息"。
func TestRecordSessionIgnoresSelf(t *testing.T) {
	b := &Bot{sessions: make(map[string]*sessionInfo)}

	// 用户消息正常记录
	user := msgFor(gateway.MessageTypeMessage, true, gateway.PlatformQQ, "g1", "uA", "m1", time.Now().Add(-time.Second))
	b.recordSession(user)
	if len(b.sessions) != 1 {
		t.Fatalf("user message should be recorded, sessions = %d", len(b.sessions))
	}

	// 机器人自己的消息（UserID == SelfID）不得覆盖用户消息
	self := msgFor(gateway.MessageTypeMessage, true, gateway.PlatformQQ, "g1", "botX", "m2", time.Now())
	self.SelfID = "botX"
	b.recordSession(self)
	if info := b.sessions["g:qq:g1"]; info == nil || info.LastMsgID != "m1" {
		t.Errorf("self echo must not overwrite session record, got %+v", info)
	}
}
