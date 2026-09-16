package bot

import (
	"context"
	"time"

	"github.com/zrurf/conduit"

	"github.com/DaWesen/lanmei-dream/internal/gateway"
)

// dedupTTL 消息去重键的存活时间。
// 覆盖 OneBot 实现重复推送 / 断线重连重放的最长间隔，远大于正常消息生命周期。
const dedupTTL = 5 * time.Minute

// Deduper 消息去重器：基于 Redis SETNX 的 message_id 幂等去重。
//
// 设计要点：
//   - 键 = dedup:msg:<conn>:<message_id>，同一连接内相同 message_id 的帧只处理一次；
//   - 无 message_id 的消息（部分 notice 事件）不判重，直接放行；
//   - 存储故障时放行（fail-open），不阻塞业务。
type Deduper struct {
	store conduit.StateStore
	ttl   time.Duration
}

// NewDeduper 创建消息去重器。
//
// 参数：
//   - store：Conduit 状态存储（Redis SETNX 语义）；nil 时 Accept 全部放行
//   - ttl：去重键存活时间；<=0 时使用默认 dedupTTL
//
// 返回：去重器（无可变状态，可被多 goroutine 并发调用）。
func NewDeduper(store conduit.StateStore, ttl time.Duration) *Deduper {
	if ttl <= 0 {
		ttl = dedupTTL
	}
	return &Deduper{store: store, ttl: ttl}
}

// Accept 返回 true 表示该消息应被处理（通过去重检查）。
//
// 参数：
//   - msg：网关标准化消息；nil、无 MessageID 或存储未配置时直接放行
//
// 返回：首次出现的 (ConnID, MessageID) 返回 true，重复消息返回 false；
// 存储故障时 fail-open 返回 true，不阻塞业务。
func (d *Deduper) Accept(msg *gateway.NormalizedMessage) bool {
	if d == nil || d.store == nil || msg == nil || msg.MessageID == "" {
		return true
	}
	// 带超时：存储挂起时不无限阻塞网关消息分发（fail-open 仅在返回 error 时生效，
	// 无超时的 Background 会在网络挂起时永久阻塞）。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ok, err := d.store.SetIfNotExists(ctx,
		conduit.MakeStoreKey("dedup", "msg", msg.ConnID, msg.MessageID), "1", d.ttl)
	if err != nil {
		// 存储故障时放行，不阻塞业务
		return true
	}
	return ok
}
