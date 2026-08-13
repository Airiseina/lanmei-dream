// Package trace 实现 Conduit 执行链路 Trace 的采集与落库：
//   - 每条消息处理完成后（含错误/超时/yield），将 conduit.GetTraceResult 序列化为
//     JSON 写入 conduit_trace 表（面板可审计查看节点状态/耗时/错误）；
//   - 从 trace 树聚合节点级流量（管线/Pass 维度），按分钟桶写入 node_traffic 表
//     （面板查看"经过某节点的流量"）。
//
// 采集回调（Sink）由 Bot 的消息处理回调并发调用，本包内部以互斥锁保护聚合状态，
// 绝不阻塞消息主链路。
package trace

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/bot"
	"github.com/DaWesen/lanmei-dream/internal/manager/store"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

// flushInterval 节点流量聚合落库间隔。
const flushInterval = 30 * time.Second

// nodeTrafficKey 节点流量聚合键（分钟桶 + 管线 + 节点）。
type nodeTrafficKey struct {
	bucket     time.Time
	pipelineID string
	nodeName   string
}

// nodeTrafficAcc 节点流量聚合值。
type nodeTrafficAcc struct {
	count      int64
	errCount   int64
	durationMS int64
}

// Collector Trace 采集器。
type Collector struct {
	store  *store.Store
	logger *zap.Logger

	mu      sync.Mutex
	nodeAgg map[nodeTrafficKey]*nodeTrafficAcc // 节点流量聚合缓冲
	stopped bool

	subMu sync.Mutex
	subs  []*subscriber // 实时 Trace 订阅者（面板 SSE）

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// subscriber 一个实时 Trace 订阅（内部持有可写 channel，对外只暴露只读接口）。
type subscriber struct {
	ch chan model.ConduitTrace
}

// NewCollector 创建 Trace 采集器。
func NewCollector(s *store.Store, logger *zap.Logger) *Collector {
	return &Collector{
		store:   s,
		logger:  logger,
		nodeAgg: make(map[nodeTrafficKey]*nodeTrafficAcc),
		stopCh:  make(chan struct{}),
	}
}

// Subscribe 订阅实时 Trace（返回带缓冲的只读通道；慢消费者自动丢弃不阻塞采集）。
// 面板 SSE 端点使用；退出时调用 Unsubscribe。
func (c *Collector) Subscribe() <-chan model.ConduitTrace {
	sub := &subscriber{ch: make(chan model.ConduitTrace, 64)}
	c.subMu.Lock()
	c.subs = append(c.subs, sub)
	c.subMu.Unlock()
	return sub.ch
}

// Unsubscribe 取消订阅。
func (c *Collector) Unsubscribe(ch <-chan model.ConduitTrace) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for i, s := range c.subs {
		if s.ch == ch {
			c.subs = append(c.subs[:i], c.subs[i+1:]...)
			return
		}
	}
}

// broadcast 向全部订阅者推送一条 Trace（不阻塞：满则丢弃该订阅者）。
func (c *Collector) broadcast(rec model.ConduitTrace) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for _, s := range c.subs {
		select {
		case s.ch <- rec:
		default:
		}
	}
}

// Sink 返回 bot.TraceSink 回调（注入 Bot.SetTraceSink）。
// trace 可能为 nil（引擎异常路径），此时跳过（不产生脏数据）。
func (c *Collector) Sink() bot.TraceSink {
	return func(ctx *conduit.MessageContext, err error, meta bot.TraceMeta) {
		if c.isStopped() {
			return
		}
		trace := conduit.GetTraceResult(ctx)
		if trace == nil {
			return
		}
		c.persistTrace(meta, trace, err)
		c.aggregateNodes(meta, trace)
	}
}

// persistTrace 序列化 trace 树并写入 conduit_trace。
func (c *Collector) persistTrace(meta bot.TraceMeta, trace *conduit.TraceSpan, err error) {
	raw, marshalErr := json.Marshal(trace)
	if marshalErr != nil {
		c.logger.Warn("trace: 序列化 Trace 失败", zap.Error(marshalErr))
		return
	}

	status := "ok"
	errMsg := ""
	if err != nil {
		status = "error"
		errMsg = err.Error()
		if len(errMsg) > 512 {
			errMsg = errMsg[:512]
		}
	}
	// 根 span 的耗时即整条消息的处理耗时
	durMS := int64(trace.Duration / time.Millisecond)
	// 从 bt_decision span 的 Detail 提取最终选择的管线
	pipeline := ""
	for _, child := range trace.Children {
		if child.Category == conduit.TraceCategoryBT && child.Detail != "" {
			pipeline = child.Detail
			break
		}
	}

	rec := model.ConduitTrace{
		TraceID:    traceID(meta),
		MessageID:  meta.MessageID,
		UserID:     meta.UserID,
		GroupID:    meta.GroupID,
		Platform:   meta.Platform,
		Pipeline:   pipeline,
		Status:     status,
		ErrMsg:     errMsg,
		DurationMS: durMS,
		Trace:      raw,
	}
	if err := c.store.BatchCreateTraces(context.Background(), []model.ConduitTrace{rec}); err != nil {
		c.logger.Warn("trace: 写库失败", zap.Error(err))
		return
	}
	c.broadcast(rec)
}

// traceID 生成 Trace 标识：优先复用 message_id（可跨段落重入关联），为空时随机生成。
func traceID(meta bot.TraceMeta) string {
	if meta.MessageID != "" {
		return meta.MessageID
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", buf)
}

// aggregateNodes 遍历 trace 树，聚合管线/Pass 级流量到内存缓冲。
func (c *Collector) aggregateNodes(meta bot.TraceMeta, trace *conduit.TraceSpan) {
	bucket := time.Now().UTC().Truncate(time.Minute)
	c.walkSpans(bucket, "", trace)
}

// walkSpans 深度遍历 trace span 树：
// pipeline span（Category=pipeline）作为当前管线上下文，其下的 pass span 归入该管线。
func (c *Collector) walkSpans(bucket time.Time, pipelineID string, span *conduit.TraceSpan) {
	switch span.Category {
	case conduit.TraceCategoryPipeline:
		pipelineID = span.Name
		c.accumulate(bucket, pipelineID, span.Name, span)
	case conduit.TraceCategoryPass:
		if pipelineID == "" {
			pipelineID = "?"
		}
		c.accumulate(bucket, pipelineID, span.Name, span)
	}
	for _, child := range span.Children {
		c.walkSpans(bucket, pipelineID, child)
	}
}

// accumulate 累加一次节点流量。
func (c *Collector) accumulate(bucket time.Time, pipelineID, nodeName string, span *conduit.TraceSpan) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := nodeTrafficKey{bucket: bucket, pipelineID: pipelineID, nodeName: nodeName}
	acc, ok := c.nodeAgg[key]
	if !ok {
		acc = &nodeTrafficAcc{}
		c.nodeAgg[key] = acc
	}
	acc.count++
	if span.Status != "" && span.Status != "ok" {
		acc.errCount++
	}
	acc.durationMS += span.Duration.Milliseconds()
}

// flush 将聚合缓冲批量落库（Upsert 累计）。
func (c *Collector) flush() {
	c.mu.Lock()
	if len(c.nodeAgg) == 0 {
		c.mu.Unlock()
		return
	}
	agg := c.nodeAgg
	c.nodeAgg = make(map[nodeTrafficKey]*nodeTrafficAcc)
	c.mu.Unlock()

	ctx := context.Background()
	for key, acc := range agg {
		if err := c.store.UpsertNodeTraffic(ctx, key.bucket, key.pipelineID, key.nodeName, acc.count, acc.errCount, acc.durationMS); err != nil {
			c.logger.Warn("trace: 节点流量落库失败",
				zap.String("pipeline", key.pipelineID),
				zap.String("node", key.nodeName),
				zap.Error(err),
			)
		}
	}
}

// Start 启动后台聚合落库协程。
func (c *Collector) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-c.stopCh:
				c.flush()
				return
			case <-ticker.C:
				c.flush()
			}
		}
	}()
}

// Stop 停止采集并排空聚合缓冲。
func (c *Collector) Stop() {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true
	c.mu.Unlock()
	close(c.stopCh)
	c.wg.Wait()
}

// isStopped 判断采集器是否已停止（并发安全）。
func (c *Collector) isStopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}
