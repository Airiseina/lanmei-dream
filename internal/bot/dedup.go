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

// NewDeduper 创建消息去重器。ttl <= 0 时使用默认 dedupTTL。
func NewDeduper(store conduit.StateStore, ttl time.Duration) *Deduper {
	if ttl <= 0 {
		ttl = dedupTTL
	}
	return &Deduper{store: store, ttl: ttl}
}

// Accept 返回 true 表示该消息应被处理（通过去重检查）。
func (d *Deduper) Accept(msg *gateway.NormalizedMessage) bool {
	if d == nil || d.store == nil || msg == nil || msg.MessageID == "" {
		return true
	}
	ok, err := d.store.SetIfNotExists(context.Background(),
		conduit.MakeStoreKey("dedup", "msg", msg.ConnID, msg.MessageID), "1", d.ttl)
	if err != nil {
		// 存储故障时放行，不阻塞业务
		return true
	}
	return ok
}
