package local

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// writeTemp 写一个临时文件，返回绝对路径。
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入临时文件失败: %v", err)
	}
	return path
}

// TestParseCSVFile_Basic 验证 CSV 基本解析：表头跳过、三列取前两列、空行跳过。
func TestParseCSVFile_Basic(t *testing.T) {
	path := writeTemp(t, "rules.csv", "\ufeff关键词,回复,匹配形式\n在吗,在的~,全字匹配\n你好,你也好呀,包含文字\n,\n天气,今天多云转晴,AI检索\n")
	entries, err := parseCSVFile(path, "rules.csv", true)
	if err != nil {
		t.Fatalf("解析 CSV 失败: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("期望 3 条数据行（跳过表头与空行），实际 %d", len(entries))
	}
	if entries[0].keyword != "在吗" || entries[0].reply != "在的~" {
		t.Errorf("首行解析错误: %+v", entries[0])
	}
	if entries[1].keyword != "你好" {
		t.Errorf("次行关键词错误: %+v", entries[1])
	}
	if entries[2].keyword != "天气" || entries[2].reply != "今天多云转晴" {
		t.Errorf("AI检索行解析错误: %+v", entries[2])
	}
}

// TestParseCSVFile_NoHeader 验证 skipHeader=false 时首行也作为数据行。
func TestParseCSVFile_NoHeader(t *testing.T) {
	path := writeTemp(t, "rules.csv", "在吗,在的~\n你好,你也好呀\n")
	entries, err := parseCSVFile(path, "rules.csv", false)
	if err != nil {
		t.Fatalf("解析 CSV 失败: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("期望 2 条数据行，实际 %d", len(entries))
	}
	if entries[0].keyword != "在吗" {
		t.Errorf("首行关键词错误: %+v", entries[0])
	}
}

// TestParseCSVFile_OnlyKeyword 验证只有一列（无回复）时也能保留关键词条目。
func TestParseCSVFile_OnlyKeyword(t *testing.T) {
	path := writeTemp(t, "rules.csv", "关键词,回复\n你好\n")
	entries, err := parseCSVFile(path, "rules.csv", true)
	if err != nil {
		t.Fatalf("解析 CSV 失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("期望 1 条数据行，实际 %d", len(entries))
	}
	if entries[0].keyword != "你好" || entries[0].reply != "" {
		t.Errorf("单列行解析错误: %+v", entries[0])
	}
}

// TestSourceID 验证 source_id 的生成规则：md 用相对路径，csv 行带行号后缀。
func TestSourceID(t *testing.T) {
	md := fileEntry{path: "a.md", rel: "a.md", row: -1}
	if got := md.sourceID(); got != "a.md" {
		t.Errorf("md sourceID 期望 a.md，实际 %s", got)
	}
	csv := fileEntry{path: "r.csv", rel: "sub/r.csv", row: 2}
	if got := csv.sourceID(); got != "sub/r.csv#2" {
		t.Errorf("csv 行 sourceID 期望 sub/r.csv#2，实际 %s", got)
	}
}

// TestRenderEntry 验证条目渲染：md 返回原文，csv 行返回 "关键词：回复"。
func TestRenderEntry(t *testing.T) {
	p := &Provider{logger: zap.NewNop()}

	mdPath := writeTemp(t, "doc.md", "# 标题\n内容\n")
	md, content, ok := p.renderEntry(fileEntry{path: mdPath, rel: "doc.md", row: -1})
	if !ok {
		t.Fatal("md 渲染失败")
	}
	if md != "" {
		t.Errorf("md 渲染标题应为空（由 buildChunk 解析），实际 %q", md)
	}
	if content != "# 标题\n内容\n" {
		t.Errorf("md 渲染内容错误: %q", content)
	}

	kw, c, ok := p.renderEntry(fileEntry{rel: "r.csv", row: 0, keyword: "在吗", reply: "在的~"})
	if !ok {
		t.Fatal("csv 行渲染失败")
	}
	if kw != "在吗" {
		t.Errorf("csv 渲染标题错误: %q", kw)
	}
	if c != "在吗：在的~" {
		t.Errorf("csv 渲染内容应含关键词语义，实际 %q", c)
	}
}
