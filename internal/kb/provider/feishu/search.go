package feishu

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
// 长文档截取前缀，控制嵌入成本与噪音。
const maxContentRunes = 4000

// Search 实现 kb.Provider：按请求模式在本地缓存文档上执行召回。
// 单模式失败仅记日志跳过，不中断整体（与引擎容错语义一致）。
func (p *Provider) Search(ctx context.Context, req *kbpkg.RecallRequest) (*kbpkg.RecallResult, error) {
	result := &kbpkg.RecallResult{ByMode: map[kbpkg.RecallMode][]kbpkg.ScoredChunk{}}

	// 文档缓存未就绪时直接返回空结果（拉取失败不阻塞对话主流程）
	if err := p.ensureDocs(ctx); err != nil {
		p.logger.Warn("kb feishu: 召回前置文档拉取失败", zap.Error(err))
		return result, nil
	}
	docs := p.snapshotDocs()
	if len(docs) == 0 {
		return result, nil
	}

	for _, mode := range req.Modes {
		switch mode {
		case kbpkg.RecallModeVector:
			if req.QueryVector == nil || p.embedder == nil {
				continue
			}
			items, err := p.searchVector(ctx, req, docs)
			if err != nil {
				p.logger.Warn("kb feishu: 向量召回失败", zap.Error(err))
				continue
			}
			result.ByMode[mode] = items
		case kbpkg.RecallModeFuzzy:
			result.ByMode[mode] = p.searchFuzzy(req, docs)
		case kbpkg.RecallModeTime:
			result.ByMode[mode] = p.searchTime(req, docs)
		}
	}
	return result, nil
}

// embedBatchSize 单次批量向量化的文档数。
const embedBatchSize = 16

// effectiveLimit 返回有效的召回上限（<=0 时用默认值），防止外部直接调用时截断异常。
func effectiveLimit(n int) int {
	if n <= 0 {
		return 5
	}
	return n
}

// searchVector 向量召回：惰性计算文档向量并缓存，对查询向量做余弦相似度排序。
func (p *Provider) searchVector(ctx context.Context, req *kbpkg.RecallRequest, docs []*cachedDoc) ([]kbpkg.ScoredChunk, error) {
	if err := p.ensureEmbeddings(ctx, docs); err != nil {
		return nil, err
	}

	type scored struct {
		doc   *cachedDoc
		score float64
	}
	list := make([]scored, 0, len(docs))
	p.mu.Lock()
	for _, d := range docs {
		vec, ok := p.embeddings[d.nodeToken]
		if !ok {
			continue
		}
		list = append(list, scored{doc: d, score: kbscore.CosineSimilarity(req.QueryVector, vec)})
	}
	p.mu.Unlock()

	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
	limit := effectiveLimit(req.Limit)
	if len(list) > limit {
		list = list[:limit]
	}
	items := make([]kbpkg.ScoredChunk, 0, len(list))
	for _, s := range list {
		items = append(items, kbpkg.ScoredChunk{Chunk: s.doc.toChunk(p.kb), Score: s.score})
	}
	return items, nil
}

// ensureEmbeddings 确保缓存中已有各文档向量，缺则批量计算（每批 embedBatchSize 个）。
func (p *Provider) ensureEmbeddings(ctx context.Context, docs []*cachedDoc) error {
	var missing []*cachedDoc
	p.mu.Lock()
	for _, d := range docs {
		if _, ok := p.embeddings[d.nodeToken]; !ok {
			missing = append(missing, d)
		}
	}
	p.mu.Unlock()
	if len(missing) == 0 {
		return nil
	}

	// 跳过空内容（embedding 接口不接受空文本）
	texts := make([]string, 0, len(missing))
	keys := make([]string, 0, len(missing))
	for _, d := range missing {
		content := kbscore.TruncateRunes(d.content, maxContentRunes)
		if strings.TrimSpace(content) == "" {
			continue
		}
		texts = append(texts, content)
		keys = append(keys, d.nodeToken)
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
			return fmt.Errorf("kb feishu: 文档向量化失败: %w", err)
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

// searchFuzzy 模糊召回：对标题 + 内容做 token 命中评分，高于阈值才返回。
func (p *Provider) searchFuzzy(req *kbpkg.RecallRequest, docs []*cachedDoc) []kbpkg.ScoredChunk {
	type scored struct {
		doc   *cachedDoc
		score float64
	}
	list := make([]scored, 0, len(docs))
	for _, d := range docs {
		score := kbscore.FuzzyScore(req.Query, d.title, d.content)
		if score >= p.fuzzyThreshold {
			list = append(list, scored{doc: d, score: score})
		}
	}

	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
	limit := effectiveLimit(req.Limit)
	if len(list) > limit {
		list = list[:limit]
	}
	items := make([]kbpkg.ScoredChunk, 0, len(list))
	for _, s := range list {
		items = append(items, kbpkg.ScoredChunk{Chunk: s.doc.toChunk(p.kb), Score: s.score})
	}
	return items
}

// searchTime 时间召回：按最近编辑时间倒序取 top-N（未解析出时间的排最后）。
func (p *Provider) searchTime(req *kbpkg.RecallRequest, docs []*cachedDoc) []kbpkg.ScoredChunk {
	sorted := make([]*cachedDoc, len(docs))
	copy(sorted, docs)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].updatedAt.After(sorted[j].updatedAt)
	})
	limit := effectiveLimit(req.Limit)
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	items := make([]kbpkg.ScoredChunk, 0, len(sorted))
	for _, d := range sorted {
		items = append(items, kbpkg.ScoredChunk{Chunk: d.toChunk(p.kb), Score: 0})
	}
	return items
}

// toChunk 将缓存文档映射为 provider 无关的分块表示。
func (d *cachedDoc) toChunk(kbb *kbpkg.KnowledgeBase) *kbpkg.Chunk {
	meta := map[string]any{"source": "feishu"}
	return &kbpkg.Chunk{
		ID:                d.nodeToken,
		KnowledgeBaseID:   kbb.ID,
		KnowledgeBaseName: kbb.Name,
		Provider:          providerName,
		Title:             d.title,
		Content:           d.content,
		URL:               d.url,
		Meta:              meta,
		CreatedAt:         d.createdAt,
		UpdatedAt:         d.updatedAt,
	}
}
