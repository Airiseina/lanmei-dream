package local

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	kbpkg "github.com/DaWesen/lanmei-dream/internal/kb"
	"github.com/DaWesen/lanmei-dream/internal/model"
	"go.uber.org/zap"
)

// modelRow 召回 SQL 的扫描载体：分块全字段 + 原始相关度分数。
type modelRow struct {
	model.KnowledgeChunk
	// Score 原始相关度（向量=余弦相似度，模糊=similarity，时间=0），供日志/调试参考。
	Score float64 `json:"score"`
}

// Search 实现 kb.Provider：按请求模式分发到对应 SQL 通路。
// 单模式失败仅记日志跳过，不中断整体（与引擎容错语义一致）。
func (p *Provider) Search(ctx context.Context, req *kbpkg.RecallRequest) (*kbpkg.RecallResult, error) {
	result := &kbpkg.RecallResult{ByMode: map[kbpkg.RecallMode][]kbpkg.ScoredChunk{}}
	for _, mode := range req.Modes {
		switch mode {
		case kbpkg.RecallModeVector:
			if req.QueryVector == nil || p.embedder == nil {
				continue
			}
			items, err := p.searchVector(ctx, req)
			if err != nil {
				p.logger.Warn("kb local: 向量召回失败", zap.Error(err))
				continue
			}
			result.ByMode[mode] = items
		case kbpkg.RecallModeFuzzy:
			items, err := p.searchFuzzy(ctx, req)
			if err != nil {
				p.logger.Warn("kb local: 模糊召回失败", zap.Error(err))
				continue
			}
			result.ByMode[mode] = items
		case kbpkg.RecallModeTime:
			items, err := p.searchTime(ctx, req)
			if err != nil {
				p.logger.Warn("kb local: 时间召回失败", zap.Error(err))
				continue
			}
			result.ByMode[mode] = items
		}
	}
	return result, nil
}

// searchVector 向量召回：按余弦距离升序取 top-N，分数 = max(0, 1-距离)。
func (p *Provider) searchVector(ctx context.Context, req *kbpkg.RecallRequest) ([]kbpkg.ScoredChunk, error) {
	vecStr := formatVector(req.QueryVector)
	where, whereArgs := p.filterSQL(req.Filter)

	rows := []*modelRow{}
	err := p.orm.WithContext(ctx).Raw(
		`SELECT k.*, GREATEST(0, 1 - (k.embedding <=> ?::vector)) AS score
		 FROM knowledge_chunks k
		 WHERE k.knowledge_base_id = ? AND k.embedding IS NOT NULL`+where+`
		 ORDER BY k.embedding <=> ?::vector ASC
		 LIMIT ?`,
		append([]any{vecStr, p.kb.ID}, append(whereArgs, vecStr, req.Limit)...)...,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("kb local: 向量召回: %w", err)
	}
	return p.rankRows(rows, req.Limit), nil
}

// searchFuzzy 模糊召回：pg_trgm similarity 大于阈值的倒排索引检索，按相似度降序。
//
// 额外支持「包含命中」：查询串直接出现在标题/内容中（如 2 字符英文缩写 "cy"/"kq"，
// 无法形成 trigram、相似度恒为 0），也强制召回并置顶，弥补 pg_trgm 对短词的盲区。
func (p *Provider) searchFuzzy(ctx context.Context, req *kbpkg.RecallRequest) ([]kbpkg.ScoredChunk, error) {
	where, whereArgs := p.filterSQL(req.Filter)

	rows := []*modelRow{}
	err := p.orm.WithContext(ctx).Raw(
		`SELECT k.*,
		    CASE WHEN k.title LIKE '%'||?||'%' OR k.content LIKE '%'||?||'%'
		         THEN 1.0 ELSE similarity(k.content, ?) END AS score
		 FROM knowledge_chunks k
		 WHERE k.knowledge_base_id = ?
		   AND (similarity(k.content, ?) >= ?
		        OR k.title LIKE '%'||?||'%'
		        OR k.content LIKE '%'||?||'%')`+where+`
		 ORDER BY score DESC
		 LIMIT ?`,
		append([]any{req.Query, req.Query, req.Query, p.kb.ID,
			req.Query, p.fuzzyThreshold, req.Query, req.Query},
			append(whereArgs, req.Limit)...)...,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("kb local: 模糊召回: %w", err)
	}
	return p.rankRows(rows, req.Limit), nil
}

// searchTime 时间召回：按最近更新时间倒序取 top-N。
func (p *Provider) searchTime(ctx context.Context, req *kbpkg.RecallRequest) ([]kbpkg.ScoredChunk, error) {
	where, whereArgs := p.filterSQL(req.Filter)

	rows := []*modelRow{}
	err := p.orm.WithContext(ctx).Raw(
		`SELECT k.*, 0 AS score
		 FROM knowledge_chunks k
		 WHERE k.knowledge_base_id = ?`+where+`
		 ORDER BY k.updated_at DESC
		 LIMIT ?`,
		append([]any{p.kb.ID}, append(whereArgs, req.Limit)...)...,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("kb local: 时间召回: %w", err)
	}
	return p.rankRows(rows, req.Limit), nil
}

// rankRows 将数据库行转换为排序结果（行序即相关度降序，直接映射）。
func (p *Provider) rankRows(rows []*modelRow, limit int) []kbpkg.ScoredChunk {
	if len(rows) > limit {
		rows = rows[:limit]
	}
	items := make([]kbpkg.ScoredChunk, 0, len(rows))
	for _, row := range rows {
		items = append(items, kbpkg.ScoredChunk{Chunk: p.toChunk(row), Score: row.Score})
	}
	return items
}

// filterSQL 构建可下推的筛选条件（时序/来源/标签）。
// 返回 WHERE 片段（含 AND 前缀，空串表示无条件）与对应参数。
// 注意：jsonb 的 ?| 运算符不能写成 "?|"（会被 GORM 当占位符），使用等价函数 jsonb_exists_any。
func (p *Provider) filterSQL(f *kbpkg.RecallFilter) (string, []any) {
	if f == nil {
		return "", nil
	}
	var conds []string
	var args []any
	if f.StartTime != nil {
		conds = append(conds, "k.updated_at >= ?")
		args = append(args, *f.StartTime)
	}
	if f.EndTime != nil {
		conds = append(conds, "k.updated_at <= ?")
		args = append(args, *f.EndTime)
	}
	if len(f.Sources) > 0 {
		// = ANY(?) 直接绑定 text[] 数组参数，避免 IN (?) 在原始 SQL 下的展开歧义
		conds = append(conds, "k.meta->>'source' = ANY(?)")
		args = append(args, f.Sources)
	}
	if len(f.Tags) > 0 {
		conds = append(conds, "jsonb_exists_any(k.meta->'tags', ?)")
		args = append(args, f.Tags)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(conds, " AND "), args
}

// formatVector 将 float32 切片格式化为 SQL 向量字面量 '[0.1,0.2,...]'。
func formatVector(vec []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
