// Package billing 负责 LLM Token 用量采集与计费。
//
// 设计要点：
//   - 实现 llm.UsageHook 回调（由 ChatService / EinoClient / ProviderManager 注入），
//     将每次 LLM 调用的用量异步批量写入 token_usage 表；
//   - 按 Provider 定价表（元/百万 token）实时计算费用（分），价格表由面板
//     LLM Provider 管理接口同步维护（内存缓存，写请求时刷新）；
//   - 批量落库采用"队列 + 后台消费者"模式：单次调用不阻塞主链路，
//     队列积压时主动丢弃并告警，绝不拖慢消息处理。
package billing

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/manager/store"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

const (
	// flushBatchSize 触发批量落库的条数阈值。
	flushBatchSize = 200
	// flushInterval 后台消费者兜底落库间隔。
	flushInterval = 2 * time.Second
	// queueCapacity 用量队列容量（超过后丢弃新记录并告警）。
	queueCapacity = 1024
)

// providerPrice 内存中的 Provider 定价表（元/百万 token）。
type providerPrice struct {
	in  float64 // 每百万输入 token 价格（元）
	out float64 // 每百万输出 token 价格（元）
}

// costCents 按定价计算一次调用的费用（分）。价格为 0 视为不计费。
func (p providerPrice) costCents(inputTokens, outputTokens int64) int64 {
	if p.in <= 0 && p.out <= 0 {
		return 0
	}
	inCost := float64(inputTokens) * p.in / 1_000_000
	outCost := float64(outputTokens) * p.out / 1_000_000
	return int64((inCost + outCost) * 100 + 0.5) // 四舍五入到分
}

// TokenAccounting LLM 用量采集与计费组件。
type TokenAccounting struct {
	store  *store.Store
	logger *zap.Logger

	priceMu sync.RWMutex
	prices  map[string]providerPrice

	queue   chan model.TokenUsage
	stopped atomic.Bool // 置位后 Hook 直接丢弃（Stop 流程中）
	wg      sync.WaitGroup
}

// New 创建 TokenAccounting。调用方需在面板启动时 Start()，退出时 Stop()。
func New(s *store.Store, logger *zap.Logger) *TokenAccounting {
	return &TokenAccounting{
		store:  s,
		logger: logger,
		prices: make(map[string]providerPrice),
		queue:  make(chan model.TokenUsage, queueCapacity),
	}
}

// SetPrices 整体刷新价格表（面板加载/保存 Provider 后调用）。
func (t *TokenAccounting) SetPrices(providers []model.LLMProvider) {
	t.priceMu.Lock()
	defer t.priceMu.Unlock()
	t.prices = make(map[string]providerPrice, len(providers))
	for _, p := range providers {
		t.prices[p.Name] = providerPrice{in: p.InPricePerM, out: p.OutPricePerM}
	}
}

// UpsertPrice 更新单个 Provider 的价格（热更新，无需整体重建）。
func (t *TokenAccounting) UpsertPrice(name string, inPricePerM, outPricePerM float64) {
	t.priceMu.Lock()
	t.prices[name] = providerPrice{in: inPricePerM, out: outPricePerM}
	t.priceMu.Unlock()
}

// RemovePrice 移除 Provider 价格（Provider 被删除后调用）。
func (t *TokenAccounting) RemovePrice(name string) {
	t.priceMu.Lock()
	delete(t.prices, name)
	t.priceMu.Unlock()
}

// Hook 返回一个 llm.UsageHook 回调，供 ChatService.SetUsageHook /
// ProviderManager.SetUsageHook / EinoClient.SetUsageHook 注入。
func (t *TokenAccounting) Hook() llm.UsageHook {
	return func(rec llm.UsageRecord) {
		if t.stopped.Load() || rec.TotalTokens <= 0 {
			return
		}
		u := t.recordToUsage(rec)
		select {
		case t.queue <- u:
		default:
			// 队列满：丢弃并告警，避免阻塞消息主链路。
			t.logger.Warn("billing: usage queue full, drop record",
				zap.String("provider", rec.Provider),
				zap.String("model", rec.Model),
				zap.String("scene", rec.Scene),
			)
		}
	}
}

// recordToUsage 将 LLM 用量记录转换为落库模型并计算费用。
func (t *TokenAccounting) recordToUsage(rec llm.UsageRecord) model.TokenUsage {
	t.priceMu.RLock()
	price := t.prices[rec.Provider]
	t.priceMu.RUnlock()

	userID := ""
	if rec.UserID > 0 {
		userID = strconv.FormatInt(rec.UserID, 10)
	}
	now := time.Now()
	return model.TokenUsage{
		Ts:           now,
		Platform:     rec.Platform,
		UserID:       userID,
		GroupID:      rec.GroupID,
		Provider:     rec.Provider,
		Model:        rec.Model,
		Scene:        normalizeScene(rec.Scene),
		InputTokens:  rec.InputTokens,
		OutputTokens: rec.OutputTokens,
		TotalTokens:  rec.TotalTokens,
		CostCents:    price.costCents(rec.InputTokens, rec.OutputTokens),
		CreatedAt:    now,
	}
}

// normalizeScene 校验并归一化用量场景；未知场景回退为 chat。
func normalizeScene(s string) model.UsageScene {
	switch model.UsageScene(strings.ToLower(strings.TrimSpace(s))) {
	case model.UsageSceneChat,
		model.UsageSceneIntent,
		model.UsageSceneCompress,
		model.UsageSceneTopic,
		model.UsageSceneVision:
		return model.UsageScene(strings.ToLower(s))
	default:
		return model.UsageSceneChat
	}
}

// Start 启动后台批量落库消费者。
// 队列关闭（Stop）后自动排空剩余记录并最终 flush 一次。
func (t *TokenAccounting) Start() {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		batch := make([]model.TokenUsage, 0, flushBatchSize)
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()

		flush := func() {
			if len(batch) == 0 {
				return
			}
			usages := batch
			batch = make([]model.TokenUsage, 0, flushBatchSize)
			if err := t.store.BatchCreateTokenUsage(t.store.Orm().Statement.Context, usages); err != nil {
				t.logger.Warn("billing: flush token usage failed",
					zap.Int("count", len(usages)), zap.Error(err))
			}
		}

		for {
			select {
			case u, ok := <-t.queue:
				if !ok {
					// 队列已关闭：排空后最终落库一次并退出。
					flush()
					return
				}
				batch = append(batch, u)
				if len(batch) >= flushBatchSize {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()
}

// Stop 停止采集：置位丢弃标记并关闭队列（消费者排空后退出）。
func (t *TokenAccounting) Stop() {
	t.stopped.Store(true)
	close(t.queue)
	t.wg.Wait()
}
