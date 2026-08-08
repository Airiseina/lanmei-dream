package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

var (
	frontMatterRe = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)
	tagsRe        = regexp.MustCompile(`(?m)^\s*tags\s*:\s*\[(.*?)\]`)
)

// fileEntry 一个待摄入的 Markdown 文件。
type fileEntry struct {
	path string // 绝对路径
	rel  string // 相对 docs_dir 路径（/ 分隔）
}

// Sync 实现 kb.Syncer：将 docs_dir 下的 Markdown 文件同步为知识分块（幂等）。
//
//   - 内容未变化的文件跳过（不重复嵌入）；
//   - 已删除的文件从库中移除；
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

	files, err := walkMarkdown(p.docsDir)
	if err != nil {
		return fmt.Errorf("kb local: 扫描 docs_dir: %w", err)
	}

	// 已存在的分块（source_id -> content），用于跳过未变化文件
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
		text, err := os.ReadFile(f.path)
		if err != nil {
			p.logger.Warn("kb local: 读取文件失败，跳过", zap.String("file", f.rel), zap.Error(err))
			continue
		}
		content := string(text)
		if existing[f.rel] == content {
			continue // 内容未变化，跳过（避免重复嵌入）
		}
		row, ok := p.buildChunk(ctx, f.rel, content)
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

	// 删除目录中已不存在的文件（保护 llm: 工具录入行）
	if err := p.deleteMissing(ctx, files); err != nil {
		return fmt.Errorf("kb local: 清理失效分块: %w", err)
	}
	return nil
}

// Store 实现 kb.Ingester：写入/更新一条分块（按 SourceID 幂等）。
func (p *Provider) Store(ctx context.Context, chunk *kbpkg.Chunk) error {
	row, ok := p.buildChunk(ctx, chunk.ID, chunk.Content)
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
// 返回 ok=false 表示该文件应跳过（读取/嵌入失败，已记录日志）。
func (p *Provider) buildChunk(ctx context.Context, sourceID, content string) (model.KnowledgeChunk, bool) {
	title, meta := parseMarkdown(content, sourceID)

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

// upsertChunks 按 (knowledge_base_id, source_id) 唯一约束 upsert。
func (p *Provider) upsertChunks(ctx context.Context, rows []model.KnowledgeChunk) error {
	return p.orm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "knowledge_base_id"}, {Name: "source_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"title", "content", "embedding", "meta", "updated_at",
		}),
	}).Create(&rows).Error
}

// deleteMissing 删除目录中已不存在的文件分块（source_id 不以 "llm:" 开头）。
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
		current = append(current, f.rel)
	}
	return q.Where("source_id NOT IN ?", current).Delete(&model.KnowledgeChunk{}).Error
}

// walkMarkdown 递归收集 docs_dir 下所有 .md 文件。
func walkMarkdown(root string) ([]fileEntry, error) {
	var files []fileEntry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, fileEntry{path: path, rel: filepath.ToSlash(rel)})
		return nil
	})
	return files, err
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
