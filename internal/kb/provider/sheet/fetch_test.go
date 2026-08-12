package sheet

import "testing"

// TestParseValues 验证 KV 行解析：表头跳过、空行过滤、上限截断、id 为行号。
func TestParseValues(t *testing.T) {
	cases := []struct {
		name       string
		values     [][]any
		skipHeader int
		maxRows    int
		want       int
		firstIndex string
		firstID    string
	}{
		{
			name:       "带表头跳过",
			values:     [][]any{{"索引", "知识内容"}, {"退货", "7天无理由"}, {"发货", "48小时"}},
			skipHeader: 1,
			maxRows:    100,
			want:       2,
			firstIndex: "退货",
			firstID:    "2",
		},
		{
			name:       "跳过表头后仍有空行被过滤",
			values:     [][]any{{"索引", "知识内容"}, {}, {"", "只有内容"}, {"只有索引", ""}},
			skipHeader: 1,
			maxRows:    100,
			want:       2,
			firstIndex: "",
			firstID:    "3",
		},
		{
			name:       "maxRows 截断",
			values:     [][]any{{"索引", "知识内容"}, {"a", "1"}, {"b", "2"}, {"c", "3"}},
			skipHeader: 1,
			maxRows:    2,
			want:       2,
			firstIndex: "a",
			firstID:    "2",
		},
		{
			name:       "无有效记录返回 nil",
			values:     [][]any{{"索引", "知识内容"}, {nil, nil}},
			skipHeader: 1,
			maxRows:    100,
			want:       0,
		},
		{
			name:       "负数表头按 0 处理",
			values:     [][]any{{"a", "1"}},
			skipHeader: -1,
			maxRows:    100,
			want:       1,
			firstIndex: "a",
			firstID:    "1",
		},
	}
	for _, c := range cases {
		got := parseValues(c.values, c.skipHeader, c.maxRows)
		if c.want == 0 {
			if got != nil {
				t.Errorf("%s: 期望 nil，实际 %d 行", c.name, len(got))
			}
			continue
		}
		if got == nil || len(got) != c.want {
			t.Errorf("%s: 期望 %d 行，实际 %v", c.name, c.want, len(got))
			continue
		}
		if got[0].index != c.firstIndex {
			t.Errorf("%s: 首行索引 = %q，期望 %q", c.name, got[0].index, c.firstIndex)
		}
		if got[0].id != c.firstID {
			t.Errorf("%s: 首行 id = %q，期望 %q", c.name, got[0].id, c.firstID)
		}
	}
}

// TestCellString 验证单元格值文本化：字符串/数字/布尔/空值。
func TestCellString(t *testing.T) {
	cases := []struct {
		name string
		row  []any
		idx  int
		want string
	}{
		{"字符串", []any{"退货", "内容"}, 0, "退货"},
		{"数字", []any{"价格", 99.5}, 1, "99.5"},
		{"布尔", []any{"是否", true}, 1, "true"},
		{"nil 单元格", []any{"索引", nil}, 1, ""},
		{"越界索引", []any{"索引"}, 5, ""},
		{"空行", nil, 0, ""},
	}
	for _, c := range cases {
		if got := cellString(c.row, c.idx); got != c.want {
			t.Errorf("%s: cellString(%v, %d) = %q，期望 %q", c.name, c.row, c.idx, got, c.want)
		}
	}
}
