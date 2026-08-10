package ai

import (
	"reflect"
	"testing"
)

// collect 将 Feed/Flush 结果收集为字符串列表。
func collect(segs []string, flush string) []string {
	out := append([]string(nil), segs...)
	if flush != "" {
		out = append(out, flush)
	}
	return out
}

// TestSegmenterPlain 普通文本：双换行正常分段。
func TestSegmenterPlain(t *testing.T) {
	s := NewStreamSegmenter()
	got := s.Feed("第一段\n\n第二段\n\n第三段")
	want := []string{"第一段", "第二段"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed = %v, want %v", got, want)
	}
	if flush := s.Flush(); flush != "第三段" {
		t.Fatalf("Flush = %q, want %q", flush, "第三段")
	}
}

// TestSegmenterCodeBlockKeepsNewlines 代码块内部的双换行不分割。
func TestSegmenterCodeBlockKeepsNewlines(t *testing.T) {
	s := NewStreamSegmenter()
	got := s.Feed("```go\nfunc main() {\n\n\tprintln(\"hi\")\n}\n```\n\n以上是代码")
	want := []string{"```go\nfunc main() {\n\n\tprintln(\"hi\")\n}\n```"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed = %v, want %v", got, want)
	}
	if flush := s.Flush(); flush != "以上是代码" {
		t.Fatalf("Flush = %q, want %q", flush, "以上是代码")
	}
}

// TestSegmenterCodeBlockAcrossChunks 代码块跨多个 chunk 时保持完整。
func TestSegmenterCodeBlockAcrossChunks(t *testing.T) {
	s := NewStreamSegmenter()
	if got := s.Feed("```go\nline1"); len(got) != 0 {
		t.Fatalf("chunk1 不应产出段落，got %v", got)
	}
	if got := s.Feed("\n\nline2\n```"); len(got) != 0 {
		t.Fatalf("chunk2 仍在代码块内，不应产出段落，got %v", got)
	}
	want := "```go\nline1\n\nline2\n```"
	if flush := s.Flush(); flush != want {
		t.Fatalf("Flush = %q, want %q", flush, want)
	}
}

// TestSegmenterUnclosedCodeBlock 未闭合代码块（LLM 漏写结尾围栏）整体保留。
func TestSegmenterUnclosedCodeBlock(t *testing.T) {
	s := NewStreamSegmenter()
	if got := s.Feed("```go\nif x {\n\n  y()\n}"); len(got) != 0 {
		t.Fatalf("未闭合代码块不应分割，got %v", got)
	}
	want := "```go\nif x {\n\n  y()\n}"
	if flush := s.Flush(); flush != want {
		t.Fatalf("Flush = %q, want %q", flush, want)
	}
}

// TestSegmenterCodeBlockBetweenText 代码块前后均有文本时正确分段。
func TestSegmenterCodeBlockBetweenText(t *testing.T) {
	s := NewStreamSegmenter()
	got := s.Feed("前置说明\n\n```\na=1\n\nb=2\n```\n\n后置说明")
	want := []string{"前置说明", "```\na=1\n\nb=2\n```"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed = %v, want %v", got, want)
	}
	if flush := s.Flush(); flush != "后置说明" {
		t.Fatalf("Flush = %q, want %q", flush, "后置说明")
	}
}

// TestSegmenterMultipleCodeBlocks 多个代码块各自保持完整。
// 两个代码块之间的 \n\n 是前一块的边界；第二个代码块后无外部边界，作为末段由 Flush 输出。
func TestSegmenterMultipleCodeBlocks(t *testing.T) {
	s := NewStreamSegmenter()
	got := s.Feed("```go\none\n\ntwo\n```\n\n```py\nthree\n\nfour\n```")
	want := []string{"```go\none\n\ntwo\n```"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed = %v, want %v", got, want)
	}
	if flush := s.Flush(); flush != "```py\nthree\n\nfour\n```" {
		t.Fatalf("Flush = %q, want 第二个代码块", flush)
	}
}

// TestSegmenterEmptySegmentsFiltered 空段落被过滤。
func TestSegmenterEmptySegmentsFiltered(t *testing.T) {
	s := NewStreamSegmenter()
	got := s.Feed("\n\n第一段\n\n\n\n第二段")
	want := []string{"第一段"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed = %v, want %v", got, want)
	}
}
