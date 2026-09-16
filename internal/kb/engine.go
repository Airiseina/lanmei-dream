package kb

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"go.uber.org/zap"
)

// searchTimeout 单个 Provider 召回的软超时，防止外部 API 拖慢对话主流程。
const searchTimeout = 15 * time.Second

// defaultFinalLimit 最终返回条数的默认值（RecallRequest.Limit <= 0 时）。
const defaultFinalLimit = 5

// Engine 多路召回引擎：负责并发召回 → rank 加权合并 → 去重 → 筛选 → 排序截断。
type Engine struct {
	mu        sync.RWMutex
	kbs       []*KnowledgeBase
	providers map[string]Provider // kbID -> Provider 实例
	weights   RecallWeights
	embedder  embedding.Embedder // 可为 nil
	logger    *zap.Logger
}

// NewEngine 创建召回引擎。
//
// 参数：
//   - weights：多路合并权重；权重 <=0 的模式在召回时整路跳过
//   - embedder：向量计算（可为 nil，vector 模式降级为不可用）
//   - logger：日志器，调用方需保证非 nil（引擎直接使用，不兜底）
func NewEngine(weights RecallWeights, embedder embedding.Embedder, logger *zap.Logger) *Engine {
	return &Engine{
		providers: make(map[string]Provider),
		weights:   weights,
		embedder:  embedder,
		logger:    logger,
	}
}

// AddProvider 注册一个知识库及其 provider 实例。
//
// 参数：
//   - kbb：知识库元信息
//   - p：provider 实例；为 nil 时忽略本次注册
//
// 注意：并发安全；同一知识库 ID 重复注册时关闭旧实例并替换（配置热加载场景），
// 知识库列表保持无重复项。
func (e *Engine) AddProvider(kbb *KnowledgeBase, p Provider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p == nil {
		return
	}
	// 同一 ID 重复注册时替换旧实例（配置热加载场景），并保持 kbs 无重复项
	if old, ok := e.providers[kbb.ID]; ok {
		_ = old.Close()
		for i, existing := range e.kbs {
			if existing.ID == kbb.ID {
				e.kbs[i] = kbb
				e.providers[kbb.ID] = p
				e.logger.Warn("kb: 知识库重复注册，已替换", zap.String("kb", kbb.ID))
				return
			}
		}
	}
	e.kbs = append(e.kbs, kbb)
	e.providers[kbb.ID] = p
}

// List 返回全部已注册知识库。
//
// 返回：知识库元信息的副本（保持注册顺序）；调用方修改不影响引擎内部状态。
func (e *Engine) List() []KnowledgeBase {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]KnowledgeBase, 0, len(e.kbs))
	for _, kbb := range e.kbs {
		out = append(out, *kbb)
	}
	return out
}

// Count 返回已注册知识库数量。
func (e *Engine) Count() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.kbs)
}

// Provider 按知识库 ID 返回对应 provider 实例。
//
// 参数：
//   - kbID：知识库 ID（配置 bases[].id）
//
// 返回：provider 实例与是否存在；实例由引擎持有，调用方不要自行关闭。
func (e *Engine) Provider(kbID string) (Provider, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p, ok := e.providers[kbID]
	return p, ok
}

// Close 关闭全部 provider。
//
// 注意：单个 provider 的 Close 错误会被忽略（仅用于资源释放），由 Service.Close 统一调用。
func (e *Engine) Close() {
	e.mu.RLock()
	providers := make([]Provider, 0, len(e.providers))
	for _, p := range e.providers {
		providers = append(providers, p)
	}
	e.mu.RUnlock()
	for _, p := range providers {
		_ = p.Close()
	}
}

// mergedItem 合并中间态：同一分块多路命中时累加分数。
type mergedItem struct {
	chunk *Chunk
	score float64
}

// Recall 执行多路召回：解析目标 KB、补齐查询向量后并发调用各 provider，
// 再按 rank 加权合并去重、筛选、排序截断。单个 provider 失败仅记日志返回空，不中断整体。
//
// 参数：
//   - ctx：召回上下文；每个 provider 的调用另带 15 秒软超时
//   - req：召回请求；req 为 nil 或 Query 为空时直接返回 nil, nil
//
// 返回：按合并分数降序的结果；无可用知识库或未召回任何内容时返回 nil, nil；
// error 当前恒为 nil（provider 失败已在内部降级处理）
//
// 注意：
//   - 加权：每个模式内第 n 名得 rankScore(n)=1/n 分，乘以该模式权重后跨模式累加，
//     多路命中同一分块时分数累加（与 ai/memory 的多路召回算法一致，跨 provider 分数可比）；
//   - 去重：键为 Provider+KnowledgeBaseID+ID，避免跨知识库错误合并同 ID 分块；
//   - 阈值：Filter.MinScore 只对合并后的分数生效（provider 返回的原始 Score 不参与比较），
//     权重 <=0 的模式整路跳过；
//   - 降级：embedder 缺失或查询向量化失败时自动移除 vector 模式，provider 不支持的模式
//     跳过并告警；
//   - 排序与截断：分数降序，同分按更新时间（零值回退创建时间）新者优先；
//     返回条数上限为 req.Limit，<=0 时使用默认值 5；下发给各 provider 的召回上限则取
//     所属知识库的 RecallLimit（<=0 时默认 5），req.Limit 不改变单库召回上限。
func (e *Engine) Recall(ctx context.Context, req *RecallRequest) ([]ScoredChunk, error) {
	if req == nil || req.Query == "" {
		return nil, nil
	}

	targets := e.targets(req.Filter)
	if len(targets) == 0 {
		return nil, nil
	}

	modes := normalizedModes(req.Modes)
	queryVec := req.QueryVector
	if containsMode(modes, RecallModeVector) && e.embedder != nil && queryVec == nil {
		vec, err := e.embedder.Embed(ctx, req.Query)
		if err != nil {
			e.logger.Warn("kb: 查询向量化失败，vector 模式降级", zap.Error(err))
			modes = removeMode(modes, RecallModeVector)
		} else {
			queryVec = vec
		}
	}
	if queryVec == nil {
		modes = removeMode(modes, RecallModeVector)
	}

	results := make([]*RecallResult, len(targets))
	var wg sync.WaitGroup
	for i, kbb := range targets {
		wg.Add(1)
		go func(i int, kbb *KnowledgeBase) {
			defer wg.Done()
			p := e.providerFor(kbb.ID)
			if p == nil {
				return
			}
			caps := p.Capabilities()
			allowed := make([]RecallMode, 0, len(modes))
			for _, m := range modes {
				if caps.Supports(m) {
					allowed = append(allowed, m)
				} else {
					e.logger.Warn("kb: provider 不支持该召回模式",
						zap.String("kb", kbb.ID), zap.String("mode", string(m)))
				}
			}
			if len(allowed) == 0 {
				return
			}
			cctx, cancel := context.WithTimeout(ctx, searchTimeout)
			defer cancel()
			rr, err := p.Search(cctx, &RecallRequest{
				Query:       req.Query,
				QueryVector: queryVec,
				Modes:       allowed,
				Limit:       kbb.RecallLimitOrDefault(),
				Filter:      req.Filter,
			})
			if err != nil {
				e.logger.Warn("kb: provider 召回失败", zap.String("kb", kbb.ID), zap.Error(err))
				return
			}
			if rr != nil {
				results[i] = rr
			}
		}(i, kbb)
	}
	wg.Wait()

	merged := make(map[string]*mergedItem, 64)
	for _, rr := range results {
		if rr == nil {
			continue
		}
		for mode, items := range rr.ByMode {
			w := e.weights.Weight(mode)
			if w <= 0 {
				continue
			}
			for rank, sc := range items {
				if sc.Chunk == nil {
					continue
				}
				// 去重键需包含知识库 ID：Provider 接口仅保证 ID 在单个 provider 内唯一，
				// 同 provider 下多个知识库可能返回相同 ID，缺省会导致跨库分块被错误合并。
				key := sc.Chunk.Provider + "::" + sc.Chunk.KnowledgeBaseID + "::" + sc.Chunk.ID
				item, ok := merged[key]
				if !ok {
					item = &mergedItem{chunk: sc.Chunk}
					merged[key] = item
				}
				item.score += w * rankScore(rank, len(items))
			}
		}
	}

	out := make([]ScoredChunk, 0, len(merged))
	for _, item := range merged {
		if !filterChunk(item.chunk, req.Filter) {
			continue
		}
		if req.Filter != nil && req.Filter.MinScore > 0 && item.score < req.Filter.MinScore {
			continue
		}
		out = append(out, ScoredChunk{Chunk: item.chunk, Score: item.score})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// 分数相同时按更新时间新者优先，保证结果稳定
		return chunkUpdatedAt(out[i].Chunk).After(chunkUpdatedAt(out[j].Chunk))
	})
	limit := req.Limit
	if limit <= 0 {
		limit = defaultFinalLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// targets 返回满足白名单与启用状态的 KB 列表（保持配置顺序）。
func (e *Engine) targets(f *RecallFilter) []*KnowledgeBase {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var want map[string]struct{}
	if f != nil && len(f.KnowledgeIDs) > 0 {
		want = make(map[string]struct{}, len(f.KnowledgeIDs))
		for _, id := range f.KnowledgeIDs {
			want[id] = struct{}{}
		}
	}
	out := make([]*KnowledgeBase, 0, len(e.kbs))
	for _, kbb := range e.kbs {
		if !kbb.Enabled {
			continue
		}
		if want != nil {
			if _, ok := want[kbb.ID]; !ok {
				continue
			}
		}
		if _, ok := e.providers[kbb.ID]; ok {
			out = append(out, kbb)
		}
	}
	return out
}

func (e *Engine) providerFor(kbID string) Provider {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.providers[kbID]
}

// chunkUpdatedAt 返回分块更新时间（零值退回创建时间）。
func chunkUpdatedAt(c *Chunk) time.Time {
	if c == nil {
		return time.Time{}
	}
	if !c.UpdatedAt.IsZero() {
		return c.UpdatedAt
	}
	return c.CreatedAt
}

// rankScore 排名衰减分：第 1 名=1.0，第 2 名≈0.5，依此类推。
// 与 ai/memory 的多路召回算法保持一致，跨 provider 可比。
func rankScore(rank, total int) float64 {
	if total <= 0 || rank < 0 {
		return 0
	}
	return 1.0 / float64(rank+1)
}

func normalizedModes(modes []RecallMode) []RecallMode {
	if len(modes) == 0 {
		return []RecallMode{RecallModeVector, RecallModeFuzzy, RecallModeTime}
	}
	out := make([]RecallMode, 0, len(modes))
	seen := make(map[RecallMode]struct{}, len(modes))
	for _, m := range modes {
		if !m.Valid() {
			continue
		}
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

func containsMode(modes []RecallMode, m RecallMode) bool {
	for _, x := range modes {
		if x == m {
			return true
		}
	}
	return false
}

func removeMode(modes []RecallMode, m RecallMode) []RecallMode {
	out := make([]RecallMode, 0, len(modes))
	for _, x := range modes {
		if x != m {
			out = append(out, x)
		}
	}
	return out
}
