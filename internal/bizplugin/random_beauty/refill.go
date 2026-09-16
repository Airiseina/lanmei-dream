package random_beauty

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/model"
	"go.uber.org/zap"
)

const (
	// refillConcurrency 补图并发峰值：同时补图的 goroutine 数上限。
	refillConcurrency = 3
	// seedFailureBreaker 种子补图连续失败熔断阈值：达到后退出，避免上游故障时空转。
	seedFailureBreaker = 10
)

// seedInterval 种子补图单张之间的固定间隔（var 以便测试缩短等待）。
var seedInterval = 3 * time.Second

// refillOne 补一张图入池：候选 → 查重 → 元数据过滤 → 下载 → vision 审核 → 上传 → 入库；
// 任何一步失败只 Warn 后结束、不重试，返回是否成功入库。
// parent 必须派生自 refillCtx，绝不引用消息 ctx；attemptCtx 与 moderationCtx 是两个正交预算。
func (p *Plugin) refillOne(parent context.Context) bool {
	if p.moderator == nil {
		// 视觉审核未配置，fail-closed 不补图（New 时已 Warn，这里不刷日志）。
		return false
	}
	// 并发闸门：插件停止（parent 取消）时直接退出。
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-parent.Done():
		return false
	}

	// 超时树：attempt 只覆盖「候选+下载」，moderation 只覆盖「审核+上传」。
	attemptCtx, cancelAttempt := context.WithTimeout(parent, time.Duration(p.cfg.TimeoutSeconds)*time.Second)
	defer cancelAttempt()
	moderationCtx, cancelModeration := context.WithTimeout(parent, time.Duration(p.cfg.RefillModerationTimeoutSeconds)*time.Second)
	defer cancelModeration()

	candidate, err := p.provider.Next(attemptCtx)
	if err != nil {
		p.warnRefill(0, "获取候选", err)
		return false
	}
	pid := candidateID(candidate)

	// 查重：已在池则弃置，避免重复下载与审核。
	exists, err := p.db.HasRandomBeautyImage(attemptCtx, candidate.LocalPath)
	if err != nil {
		p.warnRefill(pid, "查重", err)
		return false
	}
	if exists {
		p.logger.Debug("random_beauty: 候选已在池中，弃置",
			zap.Int64("pid", pid), zap.String("image_key", candidate.LocalPath))
		return false
	}
	if !metadataSafe(candidate) {
		p.logger.Debug("random_beauty: 元数据审核拒绝", zap.Int64("pid", pid))
		return false
	}

	image, err := p.downloader.Download(attemptCtx, candidate)
	if err != nil {
		p.warnRefill(pid, "下载", err)
		return false
	}

	result, err := p.moderator.ModerateImage(moderationCtx, image.Data, image.MIME)
	if err != nil {
		p.warnRefill(pid, "视觉审核", err)
		return false
	}
	if !result.IsSafe(p.cfg.SafeConfidence) {
		p.logger.Warn("random_beauty: 补图失败",
			zap.Int64("pid", pid),
			zap.String("stage", "图片安全审核拒绝"),
			zap.String("verdict", string(result.Verdict)),
			zap.Float64("confidence", result.Confidence))
		return false
	}

	objectKey, err := p.store.Put(moderationCtx, image.Data, image.MIME)
	if err != nil {
		p.warnRefill(pid, "上传对象存储", err)
		return false
	}

	tagsJSON, _ := json.Marshal(candidate.Tags)
	rec := &model.RandomBeautyPool{
		ImageKey:    candidate.LocalPath,
		IllustID:    candidate.IllustID,
		Title:       candidate.Title,
		Author:      candidate.Author,
		Tags:        string(tagsJSON),
		ObjectKey:   objectKey,
		Mime:        image.MIME,
		Width:       image.Width,
		Height:      image.Height,
		SizeBytes:   int64(len(image.Data)),
		ModeratedAt: time.Now(),
	}
	// 入库用 parent：不受 attempt/moderation 预算约束，仅随 refillCtx 停止而中止。
	if err := p.db.InsertRandomBeautyImage(parent, rec); err != nil {
		if errors.Is(err, database.ErrRandomBeautyDuplicate) {
			// 并发竞态：同图已被其他 goroutine 入库，
			// 补偿删除刚上传的对象，防止留下孤儿内容。
			if delErr := p.store.Delete(parent, objectKey); delErr != nil {
				p.warnRefill(pid, "补偿删除重复对象", delErr)
			}
			return false
		}
		p.warnRefill(pid, "入库", err)
		// 非重复入库失败不删对象：无法排除「已提交但响应丢失」，
		// 误删会把已入库的图变成坏记录；孤儿对象由内容寻址去重兜底。
		return false
	}
	// 入库成功日志附最新存量，便于观测补图进度。
	if count, err := p.db.CountRandomBeautyPool(parent); err == nil {
		p.logger.Info("random_beauty: 补图入库成功",
			zap.Int64("pid", pid), zap.Int64("pool_size", count))
	} else {
		p.logger.Info("random_beauty: 补图入库成功",
			zap.Int64("pid", pid), zap.Error(err))
	}
	return true
}

// seedLoop 种子补图：补到 PoolInitSize 为止，单张之间间隔 seedInterval；
// 连续 seedFailureBreaker 张未产出（含在池弃置）则熔断退出。
func (p *Plugin) seedLoop() {
	target := int64(p.cfg.PoolInitSize)
	consecutiveFailures := 0
	for {
		if p.refillCtx.Err() != nil {
			return
		}
		count, err := p.db.CountRandomBeautyPool(p.refillCtx)
		if err != nil {
			p.logger.Warn("random_beauty: 种子统计池数量失败", zap.Error(err))
			return
		}
		if count >= target {
			return
		}
		if p.refillOne(p.refillCtx) {
			consecutiveFailures = 0
		} else {
			consecutiveFailures++
			if consecutiveFailures >= seedFailureBreaker {
				p.logger.Warn("random_beauty: 种子补图连续失败，熔断退出",
					zap.Int("consecutive_failures", consecutiveFailures))
				return
			}
		}
		// 单张之间固定间隔，避免种子期间打爆上游。
		select {
		case <-time.After(seedInterval):
		case <-p.refillCtx.Done():
			return
		}
	}
}

// warnRefill 统一记录补图失败日志（带作品 ID 与阶段）。
func (p *Plugin) warnRefill(pid int64, stage string, err error) {
	p.logger.Warn("random_beauty: 补图失败",
		zap.Int64("pid", pid), zap.String("stage", stage), zap.Error(err))
}
