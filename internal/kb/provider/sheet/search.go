package sheet

import (
	"context"
	"fmt"
	"sort"
	"strings"

	kbpkg "github.com/DaWesen/lanmei-dream/internal/kb"
	kbscore "github.com/DaWesen/lanmei-dream/internal/kb/provider/score"
	"go.uber.org/zap"
)

// maxContentRunes 参与向量化/模糊匹配的内容最大长度（rune），
// 超长知识内容截取前缀，控制嵌入成本与噪音。
const maxContentRunes = 4000

// embedBatchSize 单次批量向量化的行数。
const embedBatchSize = 16

// Search 实现 kb.Provider：按请求模式在本地缓存行上执行召回。
// 单模式失败仅记日志跳过，不中断整体（与引擎容错语义一致）。
func (p *Provider) Search(ctx context.Context, req *kbpkg.RecallRequest) (*kbpkg.RecallResult, error) {
	result := &kbpkg.RecallResult{ByMode: map[kbpkg.RecallMode][]kbpkg.ScoredChunk{}}

	// 行缓存未就绪时直接返回空结果（拉取失败不阻塞对话主流程）
	if err := p.ensureRows(ctx); err != nil {
		p.logger.Warn("kb sheet: 召回前置表格拉取失败", zap.Error(err))
		return result, nil
	}
	rows := p.snapshotRows()
	if len(rows) == 0 {
		return result, nil
	}

	for _, mode := range req.Modes {
		switch mode {
		case kbpkg.RecallModeVector:
			if req.QueryVector == nil || p.embedder == nil {
				continue
			}
			items, err := p.searchVector(ctx, req, rows)
			if err != nil {
				p.logger.Warn("kb sheet: 向量召回失败", zap.Error(err))
				continue
			}
			result.ByMode[mode] = items
		case kbpkg.RecallModeFuzzy:
			result.ByMode[mode] = p.searchFuzzy(req, rows)
		}
	}
	return result, nil
}

// effectiveLimit 返回有效的召回上限（<=0 时用默认值），防止外部直接调用时截断异常。
func effectiveLimit(n int) int {
	if n <= 0 {
		return 5
	}
	return n
}

// searchVector 向量召回：惰性计算每行知识内容的向量并缓存，对查询向量做余弦相似度排序。
func (p *Provider) searchVector(ctx context.Context, req *kbpkg.RecallRequest, rows []*kvRow) ([]kbpkg.ScoredChunk, error) {
	if err := p.ensureEmbeddings(ctx, rows); err != nil {
		return nil, err
	}

	type scored struct {
		row   *kvRow
		score float64
	}
	list := make([]scored, 0, len(rows))
	p.mu.Lock()
	for _, r := range rows {
		vec, ok := p.embeddings[r.id]
		if !ok {
			continue
		}
		list = append(list, scored{row: r, score: kbscore.CosineSimilarity(req.QueryVector, vec)})
	}
	p.mu.Unlock()

	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
	limit := effectiveLimit(req.Limit)
	if len(list) > limit {
		list = list[:limit]
	}
	items := make([]kbpkg.ScoredChunk, 0, len(list))
	for _, s := range list {
		items = append(items, kbpkg.ScoredChunk{Chunk: s.row.toChunk(p.kb), Score: s.score})
	}
	return items, nil
}

// ensureEmbeddings 确保缓存中已有各行向量，缺则批量计算（每批 embedBatchSize 个）。
func (p *Provider) ensureEmbeddings(ctx context.Context, rows []*kvRow) error {
	var missing []*kvRow
	p.mu.Lock()
	for _, r := range rows {
		if _, ok := p.embeddings[r.id]; !ok {
			missing = append(missing, r)
		}
	}
	p.mu.Unlock()
	if len(missing) == 0 {
		return nil
	}

	// 跳过空内容（embedding 接口不接受空文本）
	texts := make([]string, 0, len(missing))
	keys := make([]string, 0, len(missing))
	for _, r := range missing {
		content := kbscore.TruncateRunes(r.content, maxContentRunes)
		if strings.TrimSpace(content) == "" {
			continue
		}
		texts = append(texts, content)
		keys = append(keys, r.id)
	}
	if len(texts) == 0 {
		return nil
	}

	vectors := make(map[string][]float32, len(texts))
	for i := 0; i < len(texts); i += embedBatchSize {
		end := i + embedBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := p.embedder.EmbedBatch(ctx, texts[i:end])
		if err != nil {
			return fmt.Errorf("kb sheet: 行内容向量化失败: %w", err)
		}
		for j, v := range vecs {
			if j < end-i {
				vectors[keys[i+j]] = v
			}
		}
	}

	p.mu.Lock()
	for k, v := range vectors {
		p.embeddings[k] = v
	}
	p.mu.Unlock()
	return nil
}

// searchFuzzy 模糊召回：对索引列（强证据）+ 知识列（主证据）做 token 命中评分，
// 高于阈值才返回。
func (p *Provider) searchFuzzy(req *kbpkg.RecallRequest, rows []*kvRow) []kbpkg.ScoredChunk {
	type scored struct {
		row   *kvRow
		score float64
	}
	list := make([]scored, 0, len(rows))
	for _, r := range rows {
		// 索引列作为"标题"参与评分（0.35 权重），知识列作为"内容"（0.65 权重）
		score := kbscore.FuzzyScore(req.Query, r.index, r.content)
		if score >= p.fuzzyThreshold {
			list = append(list, scored{row: r, score: score})
		}
	}

	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
	limit := effectiveLimit(req.Limit)
	if len(list) > limit {
		list = list[:limit]
	}
	items := make([]kbpkg.ScoredChunk, 0, len(list))
	for _, s := range list {
		items = append(items, kbpkg.ScoredChunk{Chunk: s.row.toChunk(p.kb), Score: s.score})
	}
	return items
}

// toChunk 将 KV 行映射为 provider 无关的分块表示。
func (r *kvRow) toChunk(kbb *kbpkg.KnowledgeBase) *kbpkg.Chunk {
	return &kbpkg.Chunk{
		ID:                r.id,
		KnowledgeBaseID:   kbb.ID,
		KnowledgeBaseName: kbb.Name,
		Provider:          providerName,
		Title:             r.index,
		Content:           r.content,
		Meta:              map[string]any{"source": "sheet"},
	}
}
