package local

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	kbpkg "github.com/DaWesen/lanmei-dream/internal/kb"
	"github.com/DaWesen/lanmei-dream/internal/model"
	"github.com/pgvector/pgvector-go"
	"go.uber.org/zap"
	"gorm.io/datatypes"
	"gorm.io/gorm/clause"
)

// llmSourcePrefix kb_add 工具写入行的 source_id 前缀（文件同步删除时保护此类行）。
const llmSourcePrefix = "llm:"

// maxTitleRunes Title 列 varchar(256) 上限对应的字符数（PostgreSQL 按字符计数）。
// CSV 关键词可能超长，截断避免整批 upsert 因 value too long 失败。
const maxTitleRunes = 256

// fileRowCSV CSV 数据行的 source_id 后缀分隔符：相对路径#行号。
const fileRowSep = "#"

var (
	frontMatterRe = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)
	tagsRe        = regexp.MustCompile(`(?m)^\s*tags\s*:\s*\[(.*?)\]`)
)

// fileEntry 一个待摄入的知识文件条目。
// Markdown 文件整文件为一个分块（row=-1）；CSV 文件按行拆分（row>=0）。
type fileEntry struct {
	path string // 绝对路径
	rel  string // 相对 docs_dir 路径（/ 分隔）
	row  int    // CSV 数据行号（0 起）；-1 表示整文件（Markdown）
	// CSV 行内容（Markdown 文件为空）
	keyword string // A 列：关键词（作为分块标题）
	reply   string // B 列：回复内容（作为分块内容主体）
}

// sourceID 返回分块的外部源唯一标识（(knowledge_base_id, source_id) 幂等键）。
// Markdown 用相对路径；CSV 行用 "相对路径#行号"，保证行级独立、行内内容变更可识别。
func (e fileEntry) sourceID() string {
	if e.row < 0 {
		return e.rel
	}
	return e.rel + fileRowSep + strconv.Itoa(e.row)
}

// Sync 实现 kb.Syncer：将 docs_dir 下的 Markdown/CSV 文件同步为知识分块（幂等）。
//
//   - Markdown：整文件为一个分块（解析标题/front-matter）；
//   - CSV：按行拆分（A 列=关键词、B 列=回复，每行一个分块，可跳过表头）；
//   - 内容未变化的分块跳过（不重复嵌入）；
//   - 已删除的文件/行从库中移除；
//   - source_id 以 "llm:" 开头的行（kb_add 工具录入）不受文件同步影响。
func (p *Provider) Sync(ctx context.Context) error {
	if p.docsDir == "" {
		return nil
	}

	info, err := os.Stat(p.docsDir)
	if err != nil {
		if os.IsNotExist(err) {
			p.logger.Warn("kb local: docs_dir 不存在，视为空库", zap.String("dir", p.docsDir))
			return nil
		}
		return fmt.Errorf("kb local: 读取 docs_dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("kb local: docs_dir 不是目录: %s", p.docsDir)
	}

	files := p.walkFiles(p.docsDir)

	// 已存在的分块（source_id -> content），用于跳过未变化条目
	existing := map[string]string{}
	{
		var rows []model.KnowledgeChunk
		if err := p.orm.WithContext(ctx).
			Select("source_id", "content").
			Where("knowledge_base_id = ?", p.kb.ID).
			Find(&rows).Error; err != nil {
			return fmt.Errorf("kb local: 读取已有分块: %w", err)
		}
		for _, r := range rows {
			existing[r.SourceID] = r.Content
		}
	}

	upserts := make([]model.KnowledgeChunk, 0, len(files))
	for _, f := range files {
		sourceID := f.sourceID()
		title, content, ok := p.renderEntry(f)
		if !ok {
			continue // 读取/解析失败已记录日志
		}
		if existing[sourceID] == content {
			continue // 内容未变化，跳过（避免重复嵌入）
		}
		row, ok := p.buildChunk(ctx, sourceID, title, content, f.rel)
		if !ok {
			continue // 嵌入失败已记录日志
		}
		upserts = append(upserts, row)
	}

	if len(upserts) > 0 {
		if err := p.upsertChunks(ctx, upserts); err != nil {
			return fmt.Errorf("kb local: 同步写入: %w", err)
		}
		p.logger.Info("kb local: 知识文件同步完成",
			zap.String("kb", p.kb.ID), zap.Int("changed", len(upserts)), zap.Int("total", len(files)))
	}

	// 删除目录中已不存在的文件/行（保护 llm: 工具录入行）
	if err := p.deleteMissing(ctx, files); err != nil {
		return fmt.Errorf("kb local: 清理失效分块: %w", err)
	}
	return nil
}

// Store 实现 kb.Ingester：写入/更新一条分块（按 SourceID 幂等）。
func (p *Provider) Store(ctx context.Context, chunk *kbpkg.Chunk) error {
	row, ok := p.buildChunk(ctx, chunk.ID, chunk.Title, chunk.Content, chunk.ID)
	if !ok {
		return fmt.Errorf("kb local: 知识录入嵌入失败")
	}
	row.KnowledgeBaseID = chunk.KnowledgeBaseID
	row.SourceID = chunk.ID
	row.Title = chunk.Title
	if chunk.Meta != nil {
		metaJSON, err := json.Marshal(chunk.Meta)
		if err != nil {
			return fmt.Errorf("kb local: 序列化 meta: %w", err)
		}
		row.Meta = datatypes.JSON(metaJSON)
	}
	return p.upsertChunks(ctx, []model.KnowledgeChunk{row})
}

// buildChunk 由内容构造数据库行（解析标题/front-matter + 向量化）。
// title 非空时直接采用（CSV 行的关键词）；为空则解析 Markdown 标题。
// metaSource 用于生成 meta["source"]（Markdown 传相对路径，CSV 传文件相对路径）。
// 返回 ok=false 表示该条目应跳过（读取/嵌入失败，已记录日志）。
func (p *Provider) buildChunk(ctx context.Context, sourceID, title, content, metaSource string) (model.KnowledgeChunk, bool) {
	var meta map[string]any
	if title == "" {
		title, meta = parseMarkdown(content, metaSource)
	} else {
		meta = map[string]any{"source": "file:" + filepath.ToSlash(metaSource)}
	}

	// Title 列 varchar(256) 上限防御：CSV 关键词超长时按字符截断，
	// 避免整批 upsert 因 value too long for type character varying(256) 失败
	// （截断仅影响展示，content 仍保留完整"关键词：回复"，不影响检索）。
	if r := []rune(title); len(r) > maxTitleRunes {
		title = string(r[:maxTitleRunes])
	}

	var emb pgvector.Vector
	if p.embedder != nil {
		vecs, err := p.embedder.EmbedBatch(ctx, []string{content})
		if err != nil {
			p.logger.Warn("kb local: 内容向量化失败，跳过", zap.String("source", sourceID), zap.Error(err))
			return model.KnowledgeChunk{}, false
		}
		if len(vecs) > 0 {
			emb = pgvector.NewVector(vecs[0])
		}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		p.logger.Warn("kb local: 序列化 meta 失败，跳过", zap.String("source", sourceID), zap.Error(err))
		return model.KnowledgeChunk{}, false
	}

	return model.KnowledgeChunk{
		KnowledgeBaseID: p.kb.ID,
		Provider:        providerName,
		SourceID:        sourceID,
		Title:           title,
		Content:         content,
		Embedding:       emb,
		Meta:            datatypes.JSON(metaJSON),
		UpdatedAt:       time.Now(),
	}, true
}

// renderEntry 将文件条目渲染为 (标题, 内容)。
//   - Markdown：整文件原文（标题由 buildChunk 内部解析）；
//   - CSV 行：内容 = "关键词：回复"，保证关键词语义参与向量/模糊检索。
//
// ok=false 表示读取/解析失败（已记录日志，调用方跳过）。
func (p *Provider) renderEntry(f fileEntry) (string, string, bool) {
	if f.row < 0 {
		text, err := os.ReadFile(f.path)
		if err != nil {
			p.logger.Warn("kb local: 读取文件失败，跳过", zap.String("file", f.rel), zap.Error(err))
			return "", "", false
		}
		return "", string(text), true
	}
	content := f.reply
	if f.keyword != "" {
		content = f.keyword + "：" + f.reply
	}
	return f.keyword, content, true
}

// upsertChunks 按 (knowledge_base_id, source_id) 唯一约束 upsert。
func (p *Provider) upsertChunks(ctx context.Context, rows []model.KnowledgeChunk) error {
	return p.orm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "knowledge_base_id"}, {Name: "source_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"title", "content", "embedding", "meta", "updated_at",
		}),
	}).Create(&rows).Error
}

// deleteMissing 删除目录中已不存在的文件/行分块（source_id 不以 "llm:" 开头）。
func (p *Provider) deleteMissing(ctx context.Context, files []fileEntry) error {
	q := p.orm.WithContext(ctx).
		Where("knowledge_base_id = ?", p.kb.ID).
		Where("source_id NOT LIKE ?", llmSourcePrefix+"%")
	if len(files) == 0 {
		// 目录为空：清空全部非 llm 行
		return q.Delete(&model.KnowledgeChunk{}).Error
	}
	current := make([]string, 0, len(files))
	for _, f := range files {
		current = append(current, f.sourceID())
	}
	return q.Where("source_id NOT IN ?", current).Delete(&model.KnowledgeChunk{}).Error
}

// walkFiles 递归收集 docs_dir 下的知识文件条目：
//   - .md 文件 → 整文件一个条目（row=-1）；
//   - .csv 文件 → 按行拆分为多个条目（row=0,1,...）。
//
// 单个 CSV 解析失败时记录告警并跳过该文件，不中断整体扫描。
func (p *Provider) walkFiles(root string) []fileEntry {
	var files []fileEntry
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			p.logger.Warn("kb local: 遍历 docs_dir 出错", zap.String("path", path), zap.Error(err))
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".csv" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			p.logger.Warn("kb local: 计算相对路径失败，跳过", zap.String("path", path), zap.Error(err))
			return nil
		}
		rel = filepath.ToSlash(rel)

		if ext == ".md" {
			files = append(files, fileEntry{path: path, rel: rel, row: -1})
			return nil
		}

		rows, err := parseCSVFile(path, rel, p.skipCSVHeader)
		if err != nil {
			p.logger.Warn("kb local: 解析 CSV 失败，跳过", zap.String("file", rel), zap.Error(err))
			return nil
		}
		files = append(files, rows...)
		return nil
	})
	return files
}

// parseCSVFile 解析 CSV 文件为逐行知识条目（与飞书表格结构同构）：
//
//		| 关键词(A) | 回复(B) | 匹配形式(C，忽略) |
//
//	  - 兼容 UTF-8 BOM；默认跳过首行表头（skipHeader）；
//	  - 空行跳过；行号从 0 起，作为 source_id 的后缀保证行级唯一。
func parseCSVFile(path, rel string, skipHeader bool) ([]fileEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("kb local: 打开 CSV %s: %w", rel, err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1 // 容错：列数不一致不报错
	reader.LazyQuotes = true    // 容错：引号不严格匹配的单元格

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("kb local: 解析 CSV %s: %w", rel, err)
	}

	var entries []fileEntry
	start := 0
	if skipHeader && len(records) > 0 {
		start = 1
	}
	for i := start; i < len(records); i++ {
		rec := records[i]
		keyword := ""
		reply := ""
		if len(rec) > 0 {
			keyword = strings.TrimSpace(strings.TrimPrefix(rec[0], "\ufeff"))
		}
		if len(rec) > 1 {
			reply = strings.TrimSpace(rec[1])
		}
		if keyword == "" && reply == "" {
			continue // 空行跳过
		}
		entries = append(entries, fileEntry{
			path:    path,
			rel:     rel,
			row:     len(entries),
			keyword: keyword,
			reply:   reply,
		})
	}
	return entries, nil
}

// parseMarkdown 解析 Markdown 的标题与 front-matter 元数据。
//
//   - 标题：首个 "# " 一级标题；缺失则用文件名（去掉扩展名）；
//   - meta：固定注入 source="file:<相对路径>"，可选读取 front-matter 的 tags。
func parseMarkdown(text, rel string) (string, map[string]any) {
	meta := map[string]any{"source": "file:" + filepath.ToSlash(rel)}
	body := text

	if m := frontMatterRe.FindStringSubmatch(text); m != nil {
		body = strings.TrimSpace(text[len(m[0]):])
		if tm := tagsRe.FindStringSubmatch(m[1]); tm != nil {
			var tags []string
			for _, t := range strings.Split(tm[1], ",") {
				t = strings.Trim(strings.TrimSpace(t), `"'`)
				if t != "" {
					tags = append(tags, t)
				}
			}
			if len(tags) > 0 {
				meta["tags"] = tags
			}
		}
	}

	title := ""
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			break
		}
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	}
	return title, meta
}
