package bot

import "testing"

// TestStripCodeFences 剔除 markdown 代码块围栏。
func TestStripCodeFences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"无代码块", "普通文本", "普通文本"},
		{"单代码块带语言", "```go\nfmt.Println(\"hi\")\n```", "fmt.Println(\"hi\")"},
		{"单代码块无语言", "```\na=1\n```", "a=1"},
		{"未闭合代码块", "```go\nif x {\n  y()\n}", "if x {\n  y()\n}"},
		{"代码块前后有文字", "解释如下：\n```go\nx := 1\n```\n希望能帮到你", "解释如下：\nx := 1\n希望能帮到你"},
		{"多个代码块", "```go\na\n```\n```py\nb\n```", "a\nb"},
		{"代码块内保留空行", "```\nline1\n\nline2\n```", "line1\n\nline2"},
	}
	for _, c := range cases {
		if got := stripCodeFences(c.in); got != c.want {
			t.Errorf("%s: stripCodeFences(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
