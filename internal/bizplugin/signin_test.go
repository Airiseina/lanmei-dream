package bizplugin

import "testing"

// TestTruncateRunes 按 rune 截断，UTF-8 多字节字符不被切断。
func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"短于上限原样返回", "蓝妹", 10, "蓝妹"},
		{"恰好等于上限", "一二三四五", 5, "一二三四五"},
		{"超出截断加省略号", "一二三四五六七八九", 5, "一二三四五…"},
		{"emoji 不切断（rune 截断）", "🎉🎉🎉🎉🎉🎉", 3, "🎉🎉🎉…"},
		{"中英混排", "hello蓝莓hello蓝莓", 7, "hello蓝莓…"},
		{"n<=0 返回空", "任意文本", 0, ""},
	}
	for _, c := range cases {
		if got := truncateRunes(c.in, c.n); got != c.want {
			t.Errorf("%s: truncateRunes(%q, %d) = %q, want %q", c.name, c.in, c.n, got, c.want)
		}
	}
}

// TestTruncateRunesValidUTF8 截断结果必须是合法 UTF-8（不产生乱码）。
func TestTruncateRunesValidUTF8(t *testing.T) {
	got := truncateRunes("超长昵称😀😀😀😀😀😀😀😀", 6)
	for _, r := range []rune(got) {
		if r == '\uFFFD' {
			t.Fatalf("截断结果包含替换符（无效 UTF-8）：%q", got)
		}
	}
}
