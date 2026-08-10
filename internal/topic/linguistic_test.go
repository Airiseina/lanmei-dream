package topic

// 提及分类（LLM 主判官）测试：classifyMention 三档划分与置信度阈值边界。

import (
	"testing"

	"github.com/DaWesen/lanmei-dream/internal/config"
)

// TestClassifyMentionAtIsStrong at（平台 ID 精确命中）恒为强提及，不依赖 judge。
func TestClassifyMentionAtIsStrong(t *testing.T) {
	m := testManager(nil, nil, nil)
	r := m.classifyMention(groupMsg(testGroup, "u1", "@蓝妹 在吗", "bot_self"), nil)
	if !r.Mentioned || !r.Strong || r.Mode != MentionAt {
		t.Fatalf("at mention should be strong at: got %+v", r)
	}
	// judge 即使否定也不影响 at 恒强
	r = m.classifyMention(groupMsg(testGroup, "u1", "@蓝妹 在吗", "bot_self"),
		&LinguisticJudge{IsTalkingToBot: false, Role: RoleNone, Confidence: 0.9})
	if !r.Strong || r.Mode != MentionAt {
		t.Fatalf("at mention should stay strong regardless of judge: got %+v", r)
	}
}

// TestClassifyMentionJudgeThresholds judge 置信度按阈值划分强/弱/无。
func TestClassifyMentionJudgeThresholds(t *testing.T) {
	m := testManager(&config.TopicConfig{LinguisticStrongThreshold: 0.7, LinguisticWeakThreshold: 0.4}, nil, nil)

	cases := []struct {
		name  string
		judge *LinguisticJudge
		want  MentionResult
	}{
		{"nil judge", nil, MentionResult{}},
		{"强：高于强阈值", judgeStrong(0.9), MentionResult{Mentioned: true, Mode: MentionLinguistic, Strong: true}},
		{"强：恰好等于强阈值", judgeStrong(0.7), MentionResult{Mentioned: true, Mode: MentionLinguistic, Strong: true}},
		{"弱：高于弱阈值低于强阈值", judgeWeak(0.5), MentionResult{Mentioned: true, Mode: MentionLinguistic, Strong: false}},
		{"弱：恰好等于弱阈值", judgeWeak(0.4), MentionResult{Mentioned: true, Mode: MentionLinguistic, Strong: false}},
		{"无：低于弱阈值", judgeWeak(0.3), MentionResult{}},
		{"无：IsTalkingToBot=false", &LinguisticJudge{IsTalkingToBot: false, Role: RoleRelay, Confidence: 0.99}, MentionResult{}},
	}
	for _, c := range cases {
		got := m.classifyMention(groupMsg(testGroup, "u1", "蓝妹在吗"), c.judge)
		if got.Mentioned != c.want.Mentioned || got.Strong != c.want.Strong || got.Mode != c.want.Mode {
			t.Errorf("%s: classifyMention = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestLinguisticThresholdDefaults 阈值默认值：强 0.7 / 弱 0.4（未配置时）。
func TestLinguisticThresholdDefaults(t *testing.T) {
	m := testManager(nil, nil, nil)
	if got, want := m.StrongThreshold(), 0.7; got != want {
		t.Errorf("strong threshold = %v, want %v", got, want)
	}
	if got, want := m.WeakThreshold(), 0.4; got != want {
		t.Errorf("weak threshold = %v, want %v", got, want)
	}
}

// TestLinguisticThresholdConfigured 配置覆盖阈值。
func TestLinguisticThresholdConfigured(t *testing.T) {
	m := testManager(&config.TopicConfig{LinguisticStrongThreshold: 0.85, LinguisticWeakThreshold: 0.3}, nil, nil)
	if got, want := m.StrongThreshold(), 0.85; got != want {
		t.Errorf("strong threshold = %v, want %v", got, want)
	}
	if got, want := m.WeakThreshold(), 0.3; got != want {
		t.Errorf("weak threshold = %v, want %v", got, want)
	}
}

// TestMentionModeString MentionMode 文本表示（日志可观测）。
func TestMentionModeString(t *testing.T) {
	cases := []struct {
		mode MentionMode
		want string
	}{
		{MentionNone, "none"},
		{MentionAt, "at"},
		{MentionLinguistic, "linguistic"},
	}
	for _, c := range cases {
		if got := c.mode.String(); got != c.want {
			t.Errorf("MentionMode(%d).String() = %q, want %q", c.mode, got, c.want)
		}
	}
}
