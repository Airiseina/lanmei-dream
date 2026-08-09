package topic

import "testing"

// testBot 测试用机器人身份：selfID + 主名与外号。
var testBot = &BotIdentity{SelfID: "bot_self", Nicknames: []string{"蓝妹", "蓝莓"}}

// TestDetectAt 覆盖 at 提及的正反用例（含非祈使 at 降级）。
func TestDetectAt(t *testing.T) {
	d := NewDetector()
	cases := []struct {
		name   string
		text   string
		at     []string
		want   MentionMode
		strong bool
	}{
		{name: "纯@bot", text: "@蓝妹", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+冒号", text: "@蓝妹：", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+省略号", text: "@蓝妹。。。", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+叹号提问", text: "@蓝妹！！在吗", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+中文逗号", text: "@蓝妹，帮我看下", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+请求", text: "@蓝妹 帮我看看天气", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+提问", text: "@蓝妹 今天会下雨吗？", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+问候", text: "@蓝妹 你好呀", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+在吗", text: "@蓝妹 在吗", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+正反疑问", text: "@蓝妹 在不在", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+观点询问", text: "@蓝妹 你怎么看", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+晚安", text: "@蓝妹 晚安", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+安排询问", text: "@蓝妹 这周有什么安排", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+陈述(降级)", text: "@蓝妹 上次说的那家店不错", at: []string{"bot_self"}, want: MentionPassive, strong: false},
		{name: "@bot+陈述无空格(降级)", text: "@蓝妹上次说的那家店不错", at: []string{"bot_self"}, want: MentionPassive, strong: false},
		{name: "@bot+请求无空格", text: "@蓝妹在吗", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@bot+请求无空格2", text: "@蓝莓帮我看看这个", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@别名+请求", text: "@蓝莓 帮我转发一下", at: []string{"bot_self"}, want: MentionAt, strong: true},
		{name: "@他人(不触发)", text: "你好啊", at: []string{"other_user"}, want: MentionNone, strong: false},
		{name: "多at含bot", text: "@张三 @蓝妹 都来帮我看看", at: []string{"other_user", "bot_self"}, want: MentionAt, strong: true},
		{name: "多at含bot-陈述(降级)", text: "@张三 @蓝妹 都来看看这个照片", at: []string{"other_user", "bot_self"}, want: MentionPassive, strong: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := d.Detect(c.text, c.at, testBot)
			if got.Mentioned != (c.want != MentionNone) {
				t.Fatalf("Detect(%q) Mentioned=%v, want %v", c.text, got.Mentioned, c.want != MentionNone)
			}
			if got.Mode != c.want || got.Strong != c.strong {
				t.Fatalf("Detect(%q) = mode %v strong %v, want mode %v strong %v",
					c.text, got.Mode, got.Strong, c.want, c.strong)
			}
		})
	}
}

// TestDetectNicknamePosition 覆盖昵称位置分析（呼格/祈使/传话/被动）。
func TestDetectNicknamePosition(t *testing.T) {
	d := NewDetector()
	cases := []struct {
		name   string
		text   string
		want   MentionMode
		strong bool
	}{
		// ── 呼格（强）──
		{name: "呼格-句首逗号", text: "蓝妹，帮我写个报告", want: MentionVocative, strong: true},
		{name: "呼格-标点后", text: "对了，蓝妹：你周末有空吗", want: MentionVocative, strong: true},
		{name: "呼格-空格", text: "蓝妹 在吗", want: MentionVocative, strong: true},
		{name: "呼格-句尾", text: "帮我拿一下 蓝妹", want: MentionVocative, strong: true},
		{name: "呼格-叹号", text: "蓝妹！帮我看下", want: MentionVocative, strong: true},
		{name: "呼格-无分隔在吗", text: "蓝妹在吗", want: MentionVocative, strong: true},
		{name: "呼格-无分隔请求", text: "蓝妹帮我看看这个文件", want: MentionVocative, strong: true},
		{name: "呼格-无分隔提问", text: "蓝妹你周末有空吗", want: MentionVocative, strong: true},
		{name: "呼格-无分隔问候", text: "蓝妹你好", want: MentionVocative, strong: true},
		{name: "呼格-无分隔能力询问", text: "蓝妹会翻译英文吗", want: MentionVocative, strong: true},
		{name: "呼格-正反疑问", text: "蓝妹在不在", want: MentionVocative, strong: true},
		{name: "呼格-要不要", text: "蓝妹这周末要不要一起去爬山", want: MentionVocative, strong: true},
		{name: "呼格-好不好", text: "蓝妹好不好嘛", want: MentionVocative, strong: true},
		{name: "呼格-观点询问", text: "蓝妹你怎么看", want: MentionVocative, strong: true},
		{name: "呼格-情况询问", text: "蓝妹这店怎么样", want: MentionVocative, strong: true},
		{name: "呼格-命令提醒", text: "蓝妹记得把文件发群里", want: MentionVocative, strong: true},
		{name: "呼格-晚安", text: "蓝妹晚安", want: MentionVocative, strong: true},
		{name: "呼格-是不是", text: "蓝妹是不是管理员", want: MentionVocative, strong: true},
		{name: "呼格-括号", text: "（蓝妹）帮我看看这个", want: MentionVocative, strong: true},
		{name: "呼格-括号后缀", text: "蓝妹（帮我看看）", want: MentionVocative, strong: true},
		{name: "呼格-急切呼唤", text: "蓝妹蓝妹", want: MentionVocative, strong: true},
		{name: "呼格-告诉我", text: "蓝妹告诉我明天天气", want: MentionVocative, strong: true},
		{name: "呼格-和我说说", text: "蓝妹和我说说周末安排", want: MentionVocative, strong: true},
		{name: "呼格-跟我说说", text: "蓝妹跟我说说刚才的事", want: MentionVocative, strong: true},
		{name: "呼格-你看看", text: "蓝妹你看看这个", want: MentionVocative, strong: true},
		{name: "呼格-过来一下", text: "蓝妹过来一下", want: MentionVocative, strong: true},
		{name: "呼格-重复昵称", text: "蓝妹蓝妹在吗", want: MentionVocative, strong: true},

		// ── 祈使宾语（强）──
		{name: "祈使宾语-让", text: "让蓝妹看看这个", want: MentionImperative, strong: true},
		{name: "祈使宾语-请", text: "请蓝妹来一趟", want: MentionImperative, strong: true},
		{name: "祈使宾语-问", text: "问蓝妹今天几号", want: MentionImperative, strong: true},
		{name: "祈使宾语-叫", text: "快叫蓝妹过来", want: MentionImperative, strong: true},
		{name: "祈使宾语-找", text: "去找蓝妹要文件", want: MentionImperative, strong: true},
		{name: "祈使宾语-请问(歧义)", text: "请问蓝妹在哪", want: MentionImperative, strong: true},
		{name: "祈使宾语-让背锅(歧义)", text: "让蓝妹背锅", want: MentionImperative, strong: true},

		// ── 传话（弱）──
		{name: "传话(弱)", text: "帮我告诉蓝妹今晚开会", want: MentionRelay, strong: false},
		{name: "转告(弱)", text: "麻烦转告蓝莓记得交作业", want: MentionRelay, strong: false},
		{name: "传话-第三人(弱)", text: "让小红告诉蓝妹一声", want: MentionRelay, strong: false},
		{name: "通知(弱)", text: "记得通知蓝妹改时间", want: MentionRelay, strong: false},

		// ── 被动提及（弱）──
		{name: "被动提及(弱)", text: "你们知道蓝妹吗", want: MentionPassive, strong: false},
		{name: "被动-主语", text: "蓝妹说这周末要下雨", want: MentionPassive, strong: false},
		{name: "被动-转述", text: "蓝妹上次说的那家店不错", want: MentionPassive, strong: false},
		{name: "被动-赞同", text: "呵呵蓝妹说的对", want: MentionPassive, strong: false},
		{name: "被动-介绍", text: "给大家介绍一下蓝妹", want: MentionPassive, strong: false},
		{name: "被动-活跃描述", text: "蓝妹在群里很活跃", want: MentionPassive, strong: false},
		{name: "被动-回忆", text: "你还记得蓝妹吗", want: MentionPassive, strong: false},
		{name: "被动-发了什么", text: "蓝妹昨天在群里发了什么", want: MentionPassive, strong: false},
		{name: "被动-和我说过(不误伤)", text: "蓝妹昨天和我说过这事", want: MentionPassive, strong: false},
		{name: "被动-跟我说过(不误伤)", text: "蓝妹上次跟我说过", want: MentionPassive, strong: false},
		{name: "被动-帮蓝妹看(不误伤)", text: "大家帮蓝妹看看这照片", want: MentionPassive, strong: false},
		{name: "被动-说你看(不误伤)", text: "蓝妹说你看这个", want: MentionPassive, strong: false},
		{name: "被动-安排陈述", text: "蓝妹3点开会", want: MentionPassive, strong: false},
		{name: "被动-位置询问(记录行为)", text: "蓝妹在哪", want: MentionPassive, strong: false},

		// ── 无提及（不触发）──
		{name: "无提及-闲聊", text: "今天天气不错", want: MentionNone, strong: false},
		{name: "无提及-他名", text: "小红，帮我拿一下", want: MentionNone, strong: false},
		{name: "无提及-群聊提问", text: "大家周末去爬山吗", want: MentionNone, strong: false},
		{name: "无提及-安排", text: "谁知道明天的安排", want: MentionNone, strong: false},
		{name: "无提及-代词", text: "她昨天说了什么", want: MentionNone, strong: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := d.Detect(c.text, nil, testBot)
			if got.Mentioned != (c.want != MentionNone) {
				t.Fatalf("Detect(%q) Mentioned=%v, want %v", c.text, got.Mentioned, c.want != MentionNone)
			}
			if got.Mode != c.want || got.Strong != c.strong {
				t.Fatalf("Detect(%q) = mode %v strong %v, want mode %v strong %v",
					c.text, got.Mode, got.Strong, c.want, c.strong)
			}
		})
	}
}

// TestDetectAtAndNickname 覆盖同时存在 at 与昵称文本的情况（at 优先）。
func TestDetectAtAndNickname(t *testing.T) {
	d := NewDetector()
	// at 了 bot 但文本是陈述 → 降级弱信号（即使文本含昵称）
	got := d.Detect("@蓝妹 我刚才说的不算数", []string{"bot_self"}, testBot)
	if got.Mode != MentionPassive || got.Strong {
		t.Fatalf("expected passive weak, got mode=%v strong=%v", got.Mode, got.Strong)
	}
	// 文本为空串（纯 at 段）→ 强
	got = d.Detect("", []string{"bot_self"}, testBot)
	if got.Mode != MentionAt || !got.Strong {
		t.Fatalf("expected strong at, got mode=%v strong=%v", got.Mode, got.Strong)
	}
	// 文本为纯 @ 昵称（无其他文字）→ 强
	got = d.Detect("@蓝妹", []string{"bot_self"}, testBot)
	if got.Mode != MentionAt || !got.Strong {
		t.Fatalf("expected strong at, got mode=%v strong=%v", got.Mode, got.Strong)
	}
}

// TestDetectEdgeCases 覆盖边界与异常输入（空文本/空昵称/自 ID 为空等）。
func TestDetectEdgeCases(t *testing.T) {
	d := NewDetector()

	// bot 身份缺失 → 不提及
	if got := d.Detect("蓝妹在吗", nil, nil); got.Mentioned {
		t.Fatal("nil bot should not mention")
	}
	if got := d.Detect("蓝妹在吗", nil, &BotIdentity{SelfID: "bot_self"}); got.Mentioned {
		t.Fatal("empty nicknames should not mention")
	}

	// at 目标含 bot 自身 ID 但 SelfID 为空 → at 不匹配，靠昵称规则
	got := d.Detect("蓝妹在吗", []string{""}, testBot)
	if got.Mode != MentionVocative || !got.Strong {
		t.Fatalf("empty at target should fall back to nickname, got mode=%v strong=%v", got.Mode, got.Strong)
	}

	// 空文本且无 at → 不提及
	if got := d.Detect("", nil, testBot); got.Mentioned {
		t.Fatal("empty text should not mention")
	}

	// 空文本但有 at → 强 at
	got = d.Detect("", []string{"bot_self"}, testBot)
	if got.Mode != MentionAt || !got.Strong {
		t.Fatalf("empty text with at should be strong at, got mode=%v strong=%v", got.Mode, got.Strong)
	}
}
