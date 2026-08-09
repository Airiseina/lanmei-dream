package topic

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zrurf/conduit"
	"go.uber.org/zap"

	"github.com/DaWesen/lanmei-dream/internal/ai/embedding"
	"github.com/DaWesen/lanmei-dream/internal/ai/llm"
	"github.com/DaWesen/lanmei-dream/internal/config"
)

// ── 常量 ──

// maxRecordRunes 单条消息写入话题窗口的内容长度上限（rune），防止超长消息拖垮上下文与归档。
const maxRecordRunes = 500

// maxArchiveAttempts 话题归档最大重试次数：超过后放弃归档并丢弃话题（防无限重试耗损 LLM 成本）。
const maxArchiveAttempts = 2

// maxLabelRunes 话题标签最大长度（rune）。
const maxLabelRunes = 24

// recheckSampleDefault llm_recheck_interval_msgs 未配置时的默认抽样间隔。
const recheckSampleDefault = 10

// topicIndexKey 持久化索引键：记录所有存在活跃/冷却话题的群，供启动恢复。
const topicIndexKey = "topic:index"

// Manager 群聊话题（Topic）状态管理器。
//
// 职责：
//   - HandleGroupMessage：对每条群消息做"是否应回复"的确定性决策（提及规则 → 语义 → 成员制）；
//   - 维护每个群的话题状态机（Active → Cooling → Archived）与成员、消息窗口、语义中心；
//   - 持久化到 conduit.StateStore（Redis）：话题状态 JSON + 群索引，支持重启恢复；
//   - 后台扫描：冷却超时话题异步归档到记忆层。
//
// 并发模型：单 Manager 实例；HandleGroupMessage / RecordBotReply / 后台协程之间
// 通过内部读写锁互斥。网络调用（embedding / LLM 复核）在加锁前完成，避免长阻塞。
type Manager struct {
	mu        sync.RWMutex
	groups    map[string][]*Topic // groupKey(platform:groupID) → 话题列表
	seqs      map[string]int64    // groupKey → 群消息序列号（活跃窗口判定）
	indexed   map[string]bool     // groupKey → 是否已登记到持久化索引（避免重复读 Redis）
	store     conduit.StateStore  // 状态存储（Redis），nil 时仅内存运行
	emb       embedding.Embedder  // 语义判定（可 nil，nil 时降级为成员制）
	llm       llm.LLMClient       // 弱信号复核 + 话题标签懒生成（可 nil）
	detector  *Detector
	archive   *Archiver // 冷却归档器（可 nil，nil 时超时直接丢弃）
	cfg       *config.TopicConfig
	nicknames []string // Bot 名字与别名（提及检测用）
	logger    *zap.Logger
	recheckN  atomic.Int64   // LLM 复核抽样计数器
	started   atomic.Bool    // Start 只执行一次
	wg        sync.WaitGroup // 后台协程（标签生成/归档扫描）
}

// NewManager 创建话题管理器。
// nicknames 为 Bot 名字与别名（主名 + 外号，如 ["蓝妹","蓝莓"]），为空时使用默认。
func NewManager(cfg *config.TopicConfig, store conduit.StateStore, emb embedding.Embedder,
	llmClient llm.LLMClient, arch *Archiver, nicknames []string, logger *zap.Logger) *Manager {
	if logger == nil {
		logger = zap.NewNop()
	}
	if len(nicknames) == 0 {
		nicknames = []string{"蓝妹", "蓝莓"}
	}
	if cfg == nil {
		cfg = &config.TopicConfig{}
	}
	return &Manager{
		groups:    make(map[string][]*Topic),
		seqs:      make(map[string]int64),
		indexed:   make(map[string]bool),
		store:     store,
		emb:       emb,
		llm:       llmClient,
		detector:  NewDetector(),
		archive:   arch,
		cfg:       cfg,
		nicknames: nicknames,
		logger:    logger,
	}
}

// ── 群消息决策 ──

// HandleGroupMessage 对一条群消息做决策：是否应回复、命中/创建的话题、提及模式。
//
// 决策顺序（成本从低到高）：
//  1. 强提及（at/呼格/祈使）或 LLM 复核通过的弱提及 → 创建/重入话题并回复；
//  2. 被动提及（名字作主语/宾语）且非成员 → 拉入话题（不立即回复，授回复配额）；
//  3. 成员续聊（语义相关）→ 按回复配额决定是否回复；
//  4. 成员话题切换 → 脱离原话题，成员清空则冷却；
//  5. 冷却检查：窗口内无触碰的话题转冷却。
//
// 注意：向量化与 LLM 复核为网络调用，在加锁前完成（只依赖消息本身）。
func (m *Manager) HandleGroupMessage(ctx context.Context, msg *IncomingMsg) *Decision {
	if m == nil || msg == nil || msg.GroupID == "" {
		return &Decision{}
	}
	now := msg.SentAt
	if now.IsZero() {
		now = time.Now()
	}
	gk := m.groupKey(msg.Platform, msg.GroupID)

	// 提及检测（rune 规则，无网络）
	mention := m.detector.Detect(msg.Content, msg.AtTargets, &BotIdentity{SelfID: msg.SelfID, Nicknames: m.nicknames})
	// 语义向量（每条消息最多一次 embedding，所有判定路径复用）
	vec, vecOK := m.embedMessage(ctx, msg)
	// LLM 弱信号复核（默认关闭；开启时按抽样限流）
	recheckOK := m.shouldRecheck(ctx, msg, mention)

	m.mu.Lock()
	defer m.mu.Unlock()

	seq := m.bumpSeq(gk)
	topics := m.groups[gk]

	// ── 1. 强提及 / 复核通过：创建或重入话题，回复 ──
	if mention.Strong || recheckOK {
		t := semanticMatch(m, topics, msg, vec, vecOK)
		if t == nil {
			t = m.createTopicLocked(gk, msg, seq, now)
			if vecOK {
				t.updateVector(vec) // 新话题以当前消息为语义种子
			}
			topics = append(topics, t)
		} else {
			m.reenterLocked(t, msg, seq, now, vec, vecOK)
		}
		sortTopics(topics)
		m.groups[gk] = topics
		m.persistLocked(ctx, gk, topics)
		m.logger.Info("topic: 强提及 → 回复",
			zap.String("group", gk), zap.String("user", msg.UserID),
			zap.String("mode", mention.Mode.String()), zap.String("topic", t.ID))
		return &Decision{Reply: true, Topic: t, Mention: mention.Mode}
	}

	// ── 2. 被动提及且非成员：拉入话题但不立即回复（授回复配额，下一条相关消息回复）──
	if mention.Mentioned && mention.Mode == MentionPassive {
		if memberTopicOf(topics, msg.UserID) == nil {
			t := semanticMatch(m, activeOnly(topics), msg, vec, vecOK)
			if t == nil {
				t = m.createTopicLocked(gk, msg, seq, now)
				if vecOK {
					t.updateVector(vec) // 新话题以当前消息为语义种子
				}
				topics = append(topics, t)
			} else {
				m.joinPassiveLocked(t, msg, seq, now, vec, vecOK)
			}
			t.grantCredit(msg.UserID) // 下次相关消息自动回复
			sortTopics(topics)
			m.groups[gk] = topics
			m.persistLocked(ctx, gk, topics)
			m.logger.Debug("topic: 被动提及 → 拉入话题（静默）",
				zap.String("group", gk), zap.String("user", msg.UserID), zap.String("topic", t.ID))
			return &Decision{Reply: false, Topic: t, Mention: mention.Mode}
		}
		// 已是成员：落入成员续聊分支
	}

	// ── 3. 成员续聊 / 话题切换 ──
	t := memberTopicOf(topics, msg.UserID)
	if t != nil {
		if semanticRelevant(m, t, msg, vec, vecOK) {
			m.continueChatLocked(t, msg, seq, now, vec, vecOK)
			if m.replyByCredit(t, msg.UserID) {
				m.persistLocked(ctx, gk, topics)
				m.logger.Info("topic: 成员续聊（配额）→ 回复",
					zap.String("group", gk), zap.String("user", msg.UserID), zap.String("topic", t.ID))
				return &Decision{Reply: true, Topic: t, Mention: mention.Mode}
			}
			m.persistLocked(ctx, gk, topics)
			m.logger.Debug("topic: 成员续聊（无配额）→ 静默",
				zap.String("group", gk), zap.String("user", msg.UserID), zap.String("topic", t.ID))
			return &Decision{Reply: false, Topic: t, Mention: mention.Mode}
		}
		// 用户切换了话题：脱离原话题，成员清空则冷却
		t.detachMember(msg.UserID)
		if t.MemberCount() == 0 {
			t.markCooling()
		}
		m.logger.Debug("topic: 成员话题切换 → 脱离",
			zap.String("group", gk), zap.String("user", msg.UserID), zap.String("topic", t.ID))
	}

	// ── 4. 冷却检查：窗口内无触碰的话题转冷却 ──
	changed := m.coolExpired(topics, seq)
	if changed || t != nil {
		m.persistLocked(ctx, gk, topics)
	}

	return &Decision{Reply: false, Mention: mention.Mode}
}

// RecordBotReply 记录一次 Bot 回复到话题：追加消息窗口、刷新活跃时间、
// 并给被回复用户授回复配额（下一次相关消息自动回复）。
// 由 RoleplayStreamPass 在流式回复完成后调用。
func (m *Manager) RecordBotReply(ctx context.Context, platform, groupID, topicID, selfID, userID, content string) {
	if m == nil || topicID == "" {
		return
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()

	gk := m.groupKey(platform, groupID)
	topics := m.groups[gk]
	for _, t := range topics {
		if t.ID != topicID {
			continue
		}
		t.pushMsg(TopicMsg{UserID: selfID, IsBot: true, Content: truncateRunes(content, maxRecordRunes), SentAt: now})
		t.LastActiveAt = now
		t.LastTouchSeq = m.bumpSeq(gk)
		t.grantCredit(userID)
		m.persistLocked(ctx, gk, topics)
		m.logger.Debug("topic: Bot 回复已记录", zap.String("group", gk), zap.String("topic", t.ID))
		return
	}
}

// BuildTopicContext 构建供对话管线注入的话题上下文。
// excludeTail 为排除窗口末尾的消息条数（通常为 1：当前消息已入窗，避免与用户消息重复）。
// 内部加读锁：该方法通常在 HandleGroupMessage 返回后（锁已释放）由管线调用，
// 需与并发消息处理（pushMsg/upsertMember）互斥。
func (m *Manager) BuildTopicContext(t *Topic, excludeTail int) *llm.TopicContext {
	if t == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	tc := &llm.TopicContext{TopicID: t.ID, Label: t.DisplayLabel()}
	// 成员昵称排序，保证上下文注入顺序确定
	memberNames := make([]string, 0, len(t.Members))
	for _, mem := range t.Members {
		n := mem.Nickname
		if n == "" {
			n = mem.UserID
		}
		memberNames = append(memberNames, n)
	}
	sort.Strings(memberNames)
	tc.Members = memberNames
	end := len(t.MsgWindow) - excludeTail
	if end < 0 {
		end = 0
	}
	if end > len(t.MsgWindow) {
		end = len(t.MsgWindow)
	}
	for _, tm := range t.MsgWindow[:end] {
		tc.Recent = append(tc.Recent, llm.TopicMsg{
			UserID:  tm.UserID,
			IsBot:   tm.IsBot,
			Content: tm.Content,
			At:      tm.At,
			SentAt:  tm.SentAt,
		})
	}
	return tc
}

// ── 生命周期 ──

// Start 启动后台协程：恢复持久化话题 + 周期性冷却归档扫描。
// ctx 取消时优雅退出（归档扫描停止；进行中的归档不中断）。
func (m *Manager) Start(ctx context.Context) {
	if m.started.Swap(true) {
		return
	}
	m.restore(ctx)

	interval := m.cfg.ArchiveIntervalSeconds
	if interval <= 0 {
		interval = 60
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.scanAndArchive(ctx)
			}
		}
	}()
	m.logger.Info("topic: 管理器已启动",
		zap.Int("window", m.windowMsgs()), zap.Int("cooling_timeout_min", m.cfg.CoolingTimeoutMinutes))
}

// archiveJob 归档任务：话题引用 + 锁内冻结的归档快照。
// 归档在锁外异步执行，快照保证归档期间话题被重入修改也不会产生数据竞争。
type archiveJob struct {
	t    *Topic
	snap *ArchiveSnapshot
}

// scanAndArchive 周期性扫描：冷却超时的话题触发异步归档。
func (m *Manager) scanAndArchive(ctx context.Context) {
	timeout := time.Duration(m.cfg.CoolingTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}

	var archived []*archiveJob
	m.mu.Lock()
	for gk, topics := range m.groups {
		keep := topics[:0]
		changed := false
		for _, t := range topics {
			if t.State == TopicCooling && time.Since(t.LastActiveAt) > timeout {
				if m.archive == nil {
					// 无归档器：直接丢弃（数据已保存在会话历史表）
					m.logger.Warn("topic: 无归档器，冷却话题直接丢弃", zap.String("topic", t.ID))
					changed = true
					continue
				}
				t.ArchiveAttempts++
				if t.ArchiveAttempts > maxArchiveAttempts {
					m.logger.Warn("topic: 归档重试耗尽，丢弃话题", zap.String("topic", t.ID), zap.Int("attempts", t.ArchiveAttempts))
					changed = true
					continue
				}
				// 锁内冻结归档快照（窗口/成员/标签），供锁外异步归档使用
				snap := &ArchiveSnapshot{
					ID:       t.ID,
					Platform: t.Platform,
					GroupID:  t.GroupID,
					Label:    t.Label,
					Window:   append([]TopicMsg(nil), t.MsgWindow...),
					Members:  make([]string, 0, len(t.Members)),
				}
				for uid, mem := range t.Members {
					n := mem.Nickname
					if n == "" {
						n = uid
					}
					snap.Members = append(snap.Members, n)
				}
				archived = append(archived, &archiveJob{t: t, snap: snap})
				keep = append(keep, t) // 保留至异步归档成功
				continue
			}
			keep = append(keep, t)
		}
		m.groups[gk] = keep
		if len(keep) == 0 {
			delete(m.groups, gk)
			m.deleteGroupLocked(ctx, gk)
		} else if changed {
			m.persistLocked(ctx, gk, keep)
		}
	}
	m.mu.Unlock()

	for _, job := range archived {
		m.wg.Add(1)
		go m.archiveTopic(job)
	}
}

// archiveTopic 异步归档单个话题；成功后从内存与持久化移除。
// job.snap 为锁内冻结的归档快照，归档期间话题被重入也不影响归档数据。
func (m *Manager) archiveTopic(job *archiveJob) {
	defer m.wg.Done()
	bgCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := m.archive.Archive(bgCtx, job.snap); err != nil {
		m.logger.Error("topic: 归档失败（将重试）", zap.String("topic", job.t.ID), zap.Error(err))
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// 归档期间话题被强提及恢复（Active）时，放弃移除（归档结果保留为快照）
	if job.t.State != TopicCooling {
		m.logger.Debug("topic: 归档完成但话题已恢复活跃，保留状态", zap.String("topic", job.t.ID))
		return
	}
	gk := m.groupKey(job.snap.Platform, job.snap.GroupID)
	topics := m.groups[gk]
	keep := make([]*Topic, 0, len(topics))
	for _, x := range topics {
		if x.ID != job.t.ID {
			keep = append(keep, x)
		}
	}
	m.groups[gk] = keep
	if len(keep) == 0 {
		delete(m.groups, gk)
		m.deleteGroupLocked(context.Background(), gk)
	} else {
		m.persistLocked(context.Background(), gk, keep)
	}
}

// ── 状态机操作（需在持锁状态下调用）──

// createTopicLocked 创建新话题（强提及/被动提及路径）。
func (m *Manager) createTopicLocked(gk string, msg *IncomingMsg, seq int64, now time.Time) *Topic {
	t := &Topic{
		ID:           fmt.Sprintf("topic:%s:%s:%d", msg.Platform, msg.GroupID, m.nextSeq(gk)),
		Platform:     msg.Platform,
		GroupID:      msg.GroupID,
		State:        TopicActive,
		Members:      make(map[string]*Member),
		LastTouchSeq: seq,
		CreatedAt:    now,
	}
	t.LastMentionAt = now
	t.LastActiveAt = now
	t.upsertMember(msg.UserID, msg.UserName, now, true)
	t.pushMsg(m.msgToTopicMsg(msg))
	m.lazyLabel(gk, t)
	return t
}

// reenterLocked 强提及重入已有话题（含冷却恢复）。
func (m *Manager) reenterLocked(t *Topic, msg *IncomingMsg, seq int64, now time.Time, vec []float32, vecOK bool) {
	t.restoreActive(now)
	t.LastTouchSeq = seq
	t.upsertMember(msg.UserID, msg.UserName, now, true)
	t.touchMention(m.msgToTopicMsg(msg), now)
	if vecOK {
		t.updateVector(vec)
	}
	m.lazyLabel(m.groupKey(msg.Platform, msg.GroupID), t)
}

// joinPassiveLocked 被动提及用户加入已有活跃话题（不刷新提及时间）。
func (m *Manager) joinPassiveLocked(t *Topic, msg *IncomingMsg, seq int64, now time.Time, vec []float32, vecOK bool) {
	t.LastTouchSeq = seq
	t.upsertMember(msg.UserID, msg.UserName, now, false)
	t.touchChat(m.msgToTopicMsg(msg), now)
	if vecOK {
		t.updateVector(vec)
	}
}

// continueChatLocked 成员在话题内续聊（不刷新提及时间）。
func (m *Manager) continueChatLocked(t *Topic, msg *IncomingMsg, seq int64, now time.Time, vec []float32, vecOK bool) {
	t.LastTouchSeq = seq
	t.upsertMember(msg.UserID, msg.UserName, now, false)
	t.touchChat(m.msgToTopicMsg(msg), now)
	if vecOK {
		t.updateVector(vec)
	}
}

// replyByCredit 判断成员续聊是否应回复（回复配额机制）。
// credit_enabled 关闭时成员续聊一律不回复（仅强提及时回复）。
func (m *Manager) replyByCredit(t *Topic, userID string) bool {
	if m.cfg == nil || !m.cfg.CreditEnabled {
		return false
	}
	return t.consumeCredit(userID)
}

// coolExpired 将窗口内无触碰的话题转冷却。返回是否有状态变化。
func (m *Manager) coolExpired(topics []*Topic, seq int64) bool {
	window := int64(m.windowMsgs())
	changed := false
	for _, t := range topics {
		if t.IsActive() && seq-t.LastTouchSeq > window {
			t.markCooling()
			changed = true
		}
	}
	return changed
}

// msgToTopicMsg 将入站消息转换为窗口消息记录。
func (m *Manager) msgToTopicMsg(msg *IncomingMsg) TopicMsg {
	sentAt := msg.SentAt
	if sentAt.IsZero() {
		sentAt = time.Now()
	}
	return TopicMsg{
		UserID:  msg.UserID,
		Content: truncateRunes(msg.Content, maxRecordRunes),
		At:      containsString(msg.AtTargets, msg.SelfID),
		SentAt:  sentAt,
	}
}

// ── 弱信号 LLM 复核（成本受限）──

// shouldRecheck 判断弱提及消息是否应做 LLM 复核并是否通过。
// 仅当配置开启且消息为弱提及时抽样调用；返回 true 表示复核认为用户在跟 Bot 对话。
func (m *Manager) shouldRecheck(ctx context.Context, msg *IncomingMsg, mention MentionResult) bool {
	if m.cfg == nil || !m.cfg.LLMRecheck || m.llm == nil || msg == nil {
		return false
	}
	if !mention.Mentioned || mention.Strong {
		return false
	}
	interval := m.cfg.LLMRecheckIntervalMsgs
	if interval <= 0 {
		interval = recheckSampleDefault
	}
	if m.recheckN.Add(1)%int64(interval) != 0 {
		return false // 抽样限流：每 interval 条弱信号最多复核 1 条
	}

	resp, err := m.llm.Chat(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: recheckSystemPrompt},
			{Role: llm.RoleUser, Content: msg.Content},
		},
	})
	if err != nil || resp == nil {
		m.logger.Debug("topic: LLM 复核失败，按不通过处理", zap.Error(err))
		return false
	}
	var res struct {
		IsTalkingToBot bool `json:"is_talking_to_bot"`
	}
	if json.Unmarshal([]byte(resp.Content), &res) != nil {
		return false
	}
	m.logger.Debug("topic: LLM 复核结果", zap.Bool("talking", res.IsTalkingToBot), zap.String("msg", truncateRunes(msg.Content, 32)))
	return res.IsTalkingToBot
}

// recheckSystemPrompt 弱提及复核 prompt：一次性判断，无上下文。
const recheckSystemPrompt = `你是一个群聊对话判断器。用户发送了一条提到机器人（名为"蓝妹"/"蓝莓"）的消息。判断这条消息是否在"跟机器人说话"（即期望机器人回应）。
规则：
- @机器人、以机器人名字呼格开头、请求机器人做事、直接问机器人问题 → true
- 只是在向其他人提及机器人（如"蓝妹上次说的"、"帮我告诉蓝妹"、"你们知道蓝妹吗"）→ false
只输出 JSON：{"is_talking_to_bot": true 或 false, "reason": "简短原因"}`

// ── 话题标签懒生成 ──

// lazyLabel 异步生成话题标签（LLM 一次调用；无 LLM/已生成/生成中则跳过）。
// 在锁内快照消息窗口后交 goroutine 使用，避免与主协程 pushMsg 的数据竞争。
func (m *Manager) lazyLabel(gk string, t *Topic) {
	if m.llm == nil || t.Label != "" || t.LabelPending {
		return
	}
	t.LabelPending = true
	// 锁内快照（lazyLabel 由 createTopicLocked/reenterLocked 在持锁状态下调用）
	window := make([]TopicMsg, len(t.MsgWindow))
	copy(window, t.MsgWindow)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		label := m.genLabel(bgCtx, window)
		m.mu.Lock()
		t.Label = label
		t.LabelPending = false
		m.persistLocked(context.Background(), gk, m.groups[gk])
		m.mu.Unlock()
	}()
}

// genLabel 通过 LLM 为话题生成一句话标签；失败时返回默认值。
// window 为调用方（lazyLabel）快照的消息窗口，本函数只读不修改。
func (m *Manager) genLabel(ctx context.Context, window []TopicMsg) string {
	resp, err := m.llm.Chat(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: labelSystemPrompt},
			{Role: llm.RoleUser, Content: formatWindow(window)},
		},
	})
	if err != nil || resp == nil || resp.Content == "" {
		return defaultTopicLabel
	}
	label := strings.TrimSpace(strings.Trim(resp.Content, `"'"。`))
	if label == "" {
		return defaultTopicLabel
	}
	return truncateRunes(label, maxLabelRunes)
}

// labelSystemPrompt 话题命名 prompt。
const labelSystemPrompt = `你是群聊话题命名助手。根据以下群聊对话片段，用不超过 12 个字概括这个话题的核心内容。
只输出话题名本身，不要引号、不要解释、不要标点。例如：周末爬山计划`

// ── 持久化 ──

// persistLocked 将某群的话题列表序列化写入 Redis（TTL = 冷却超时 × 2），并登记群索引。
// 空列表时删除键与索引（需在持锁状态下调用）。
func (m *Manager) persistLocked(ctx context.Context, gk string, topics []*Topic) {
	if m.store == nil {
		return
	}
	ttl := m.persistTTL()
	if len(topics) == 0 {
		m.deleteGroupLocked(ctx, gk)
		return
	}
	data, err := json.Marshal(topics)
	if err != nil {
		m.logger.Warn("topic: 序列化失败", zap.String("group", gk), zap.Error(err))
		return
	}
	if err := m.store.Set(ctx, m.redisKey(gk), string(data), ttl); err != nil {
		m.logger.Warn("topic: 持久化失败", zap.String("group", gk), zap.Error(err))
		return
	}
	m.indexAddLocked(ctx, gk, ttl)
}

// deleteGroupLocked 删除某群的持久化状态与索引（需在持锁状态下调用）。
func (m *Manager) deleteGroupLocked(ctx context.Context, gk string) {
	if m.store == nil {
		return
	}
	_ = m.store.Delete(ctx, m.redisKey(gk))
	m.indexRemoveLocked(ctx, gk)
	delete(m.seqs, gk)
	delete(m.indexed, gk)
}

// indexAddLocked 将群登记到索引（幂等，进程内缓存避免重复读）。
func (m *Manager) indexAddLocked(ctx context.Context, gk string, ttl time.Duration) {
	if m.indexed[gk] {
		return
	}
	keys, err := m.readIndexLocked(ctx)
	if err != nil {
		m.logger.Warn("topic: 索引读取失败", zap.Error(err))
		return
	}
	for _, k := range keys {
		if k == gk {
			m.indexed[gk] = true
			return
		}
	}
	keys = append(keys, gk)
	if err := m.store.Set(ctx, topicIndexKey, mustJSON(keys), ttl); err != nil {
		m.logger.Warn("topic: 索引写入失败", zap.Error(err))
		return
	}
	m.indexed[gk] = true
}

// indexRemoveLocked 将群从索引移除（列表清空时删除索引键）。
func (m *Manager) indexRemoveLocked(ctx context.Context, gk string) {
	keys, err := m.readIndexLocked(ctx)
	if err != nil {
		delete(m.indexed, gk)
		return
	}
	out := keys[:0]
	for _, k := range keys {
		if k != gk {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		_ = m.store.Delete(ctx, topicIndexKey)
	} else {
		_ = m.store.Set(ctx, topicIndexKey, mustJSON(out), m.persistTTL())
	}
	delete(m.indexed, gk)
}

// readIndexLocked 读取群索引列表（已删除或空时返回空列表）。
func (m *Manager) readIndexLocked(ctx context.Context) ([]string, error) {
	raw, err := m.store.Get(ctx, topicIndexKey)
	if err != nil || raw == "" {
		return nil, err
	}
	var keys []string
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

// restore 启动时从 Redis 恢复各群话题状态。
func (m *Manager) restore(ctx context.Context) {
	if m.store == nil {
		return
	}
	keys, err := m.readIndexLocked(ctx)
	if err != nil || len(keys) == 0 {
		return
	}
	for _, gk := range keys {
		data, err := m.store.Get(ctx, m.redisKey(gk))
		if err != nil || data == "" {
			continue
		}
		var topics []*Topic
		if err := json.Unmarshal([]byte(data), &topics); err != nil {
			m.logger.Warn("topic: 恢复反序列化失败", zap.String("group", gk), zap.Error(err))
			continue
		}
		for _, t := range topics {
			if t.Members == nil {
				t.Members = make(map[string]*Member)
			}
			t.LastTouchSeq = 0 // 进程内消息序列已重置，冷却窗口重新累计
			if len(t.MsgWindow) == 0 || t.MemberCount() == 0 {
				t.markCooling() // 无窗口/无成员的话题恢复后置冷却，等待归档
			}
		}
		m.groups[gk] = topics
		m.indexed[gk] = true
		m.logger.Info("topic: 已恢复话题状态", zap.String("group", gk), zap.Int("topics", len(topics)))
	}
}

// ── 内部工具 ──

// groupKey 生成内存/索引用的群键（平台隔离，避免跨平台 groupID 冲突）。
func (m *Manager) groupKey(platform, groupID string) string {
	if platform == "" {
		return groupID
	}
	return platform + ":" + groupID
}

// redisKey 生成 Redis 存储键。
func (m *Manager) redisKey(gk string) string {
	return conduit.MakeStoreKey("topic", "g", gk)
}

// persistTTL 话题状态在 Redis 的存活时间（冷却超时 × 2，兜底 1 小时）。
func (m *Manager) persistTTL() time.Duration {
	ttl := time.Duration(m.cfg.CoolingTimeoutMinutes) * 2 * time.Minute
	if ttl <= 0 {
		ttl = time.Hour
	}
	return ttl
}

// windowMsgs 活跃窗口大小（topic_window_msgs，兜底 20）。
func (m *Manager) windowMsgs() int {
	if m.cfg != nil && m.cfg.TopicWindowMsgs > 0 {
		return m.cfg.TopicWindowMsgs
	}
	return topicWindowDefault
}

// bumpSeq 群消息序列号自增（每条群消息 +1，用于活跃窗口判定）。
func (m *Manager) bumpSeq(gk string) int64 {
	m.seqs[gk]++
	return m.seqs[gk]
}

// nextSeq 生成话题 ID 序列号（取群内现有话题最大序号 + 1）。
func (m *Manager) nextSeq(gk string) int64 {
	var max int64
	for _, t := range m.groups[gk] {
		idx := strings.LastIndexByte(t.ID, ':')
		if idx < 0 {
			continue
		}
		if n, err := strconv.ParseInt(t.ID[idx+1:], 10, 64); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}

// sortTopics 按最近活跃时间降序排序话题（确定性遍历顺序）。
func sortTopics(topics []*Topic) {
	sort.Slice(topics, func(i, j int) bool {
		return topics[i].LastActiveAt.After(topics[j].LastActiveAt)
	})
}

// mustJSON 序列化为 JSON 字符串（失败时返回空 JSON 数组，避免破坏持久化键）。
func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(data)
}
