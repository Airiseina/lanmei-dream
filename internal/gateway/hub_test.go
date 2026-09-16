package gateway

import "testing"

// TestSpaceAfterAt 覆盖出站段空格补齐：只有紧跟 at 段的非空文本段会被补前导空格，
// 其余情况保持原样，且不修改调用方传入的切片。
func TestSpaceAfterAt(t *testing.T) {
	atSeg := func(id string) NormalizedSegment {
		return NormalizedSegment{Type: "at", Data: map[string]string{"user_id": id}}
	}
	textSeg := func(s string) NormalizedSegment {
		return NormalizedSegment{Type: "text", Text: s}
	}

	cases := []struct {
		name   string
		segs   []NormalizedSegment
		textAt int // 断言用的文本段下标
		want   string
	}{
		{"at 后紧跟文本补一个空格", []NormalizedSegment{atSeg("1"), textSeg("你好")}, 1, " 你好"},
		{"文本已有前导空格不重复补", []NormalizedSegment{atSeg("1"), textSeg(" 你好")}, 1, " 你好"},
		{"文本为空保持原样", []NormalizedSegment{atSeg("1"), textSeg("")}, 1, ""},
		{"at 后紧跟图片不处理", []NormalizedSegment{atSeg("1"), {Type: "image"}}, 1, ""},
		{"连续多个 at 最终只补跟随文本的那个", []NormalizedSegment{atSeg("1"), atSeg("2"), textSeg("你好")}, 2, " 你好"},
		{"文本不在 at 之后不处理", []NormalizedSegment{textSeg("你好"), atSeg("1")}, 0, "你好"},
		{"非 at 段后紧跟文本不处理", []NormalizedSegment{{Type: "reply"}, textSeg("你好")}, 1, "你好"},
		{"at 后是引用段再跟文本不处理", []NormalizedSegment{atSeg("1"), {Type: "reply"}, textSeg("你好")}, 2, "你好"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := tc.segs[tc.textAt].Text
			got := spaceAfterAt(tc.segs)
			if got[tc.textAt].Text != tc.want {
				t.Fatalf("文本段 = %q, 期望 %q", got[tc.textAt].Text, tc.want)
			}
			if tc.segs[tc.textAt].Text != origin {
				t.Fatalf("调用方切片被修改: %q -> %q", origin, tc.segs[tc.textAt].Text)
			}
		})
	}
}
