package bizplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/database"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/zrurf/conduit"
)

// ============================================================
// GuessNumberPlugin 编程答题插件
// ============================================================

// GuessNumberPlugin 保留历史类型名、插件 ID 和配置键，当前实现新手编程答题游戏：
//   - /答题：从全部支持语言中开始五题混合答题
//   - /答题 java：选择一种语言，可用空格同时选择多种语言
//   - A / B / C / D：在当前题目中抢答
//
// 每轮固定五道四选一题，每题限时一分钟；第一个答对的人积一分并进入下一题，
// 答错会 @ 玩家，同一玩家在同一道题中最多作答两次。任意题超时会立即结束整轮，
// 完成五题后公布本轮排行榜。轮次状态按群聊或私聊隔离，结算后直接删除，积分不跨轮。
//
// 行为树：
//
//	subtree.guess_number → Selector(
//	  Sequence(isQuizCommand,             Action("pipeline.plugin.guess_number.main")),
//	  Sequence(pass.isActiveQuizAnswer, Action("pipeline.plugin.guess_number.main")),
//	)
//
// 管线：
//
//	pipeline.plugin.guess_number.main → [quizPass]
type GuessNumberPlugin struct {
	kv   *database.PluginKVStore
	pass *quizPass
}

// NewGuessNumberPlugin 创建编程答题插件。
func NewGuessNumberPlugin() *GuessNumberPlugin { return &GuessNumberPlugin{} }

// Info 返回编程答题插件元信息。
func (p *GuessNumberPlugin) Info() pluginpkg.PluginInfo {
	return pluginpkg.PluginInfo{
		ID:          "guess_number",
		Name:        "编程答题",
		Description: "Java、Go、Python、C、C++ 新手选择题抢答（每轮五题、每题两次机会）",
		Version:     "2.1.0",
		Commands: []pluginpkg.CommandDef{
			{Name: "答题", Description: "开始编程选择题，可选 Java/Go/Python/C/C++，例如：/答题 go python", Order: 133},
		},
		SubtreeID: pluginpkg.SubtreeID("guess_number"),
	}
}

// OnInit 注册编程答题 Pass、Pipeline 和 Subtree。
func (p *GuessNumberPlugin) OnInit(ctx *pluginpkg.PluginContext) error {
	p.kv = ctx.KV
	p.pass = newQuizPass(p.kv)

	passID := pluginpkg.PassID("guess_number", "main")
	if err := ctx.Engine.RegisterPass(passID, p.pass); err != nil {
		return fmt.Errorf("register guess_number pass: %w", err)
	}
	ctx.Registry.TrackPass("guess_number", passID)

	pipelineID := pluginpkg.PipelineID("guess_number", "main")
	if err := ctx.Engine.RegisterPipeline(conduit.NewPipelineFromIDs(pipelineID, passID)); err != nil {
		return fmt.Errorf("register guess_number pipeline: %w", err)
	}
	ctx.Registry.TrackPipeline("guess_number", pipelineID)

	subtree := conduit.NewSelector(
		conduit.NewSequence(
			conduit.NewCondition(isQuizCommand),
			conduit.NewAction(pipelineID),
		),
		conduit.NewSequence(
			conduit.NewCondition(p.pass.isActiveQuizAnswer),
			conduit.NewAction(pipelineID),
		),
	)
	if err := ctx.Engine.RegisterSubtree(pluginpkg.SubtreeID("guess_number"), subtree); err != nil {
		return fmt.Errorf("register guess_number subtree: %w", err)
	}
	return nil
}

// OnStart 编程答题插件无需额外启动任务；计时器随每轮游戏创建。
func (p *GuessNumberPlugin) OnStart(_ *pluginpkg.PluginContext) error { return nil }

// OnStop 停止所有题目计时器并关闭异步消息通道。
func (p *GuessNumberPlugin) OnStop(_ *pluginpkg.PluginContext) error {
	if p.pass != nil {
		p.pass.stopAll()
	}
	return nil
}

// ============================================================
// 条件判断与输入解析
// ============================================================

// isQuizCommand 判断消息是否为答题开局命令。
func isQuizCommand(ctx *conduit.MessageContext) bool {
	msg := strings.TrimSpace(ctx.RawMsg)
	return msg == "/答题" || strings.HasPrefix(msg, "/答题 ")
}

// isQuizChoiceMessage 判断消息是否是一个可识别的选项。
func isQuizChoiceMessage(ctx *conduit.MessageContext) bool {
	_, ok := parseQuizChoice(ctx.RawMsg)
	return ok
}

// parseQuizChoice 解析 A～D，兼容小写、全角字母和“答案 A”写法。
func parseQuizChoice(raw string) (int, bool) {
	choice := strings.ToUpper(strings.TrimSpace(raw))
	choice = strings.TrimSpace(strings.TrimPrefix(choice, "答案"))
	switch choice {
	case "A", "Ａ":
		return 0, true
	case "B", "Ｂ":
		return 1, true
	case "C", "Ｃ":
		return 2, true
	case "D", "Ｄ":
		return 3, true
	default:
		return 0, false
	}
}

// parseQuizLanguages 解析 /答题 后的语言列表；空参数表示五种语言混合。
func parseQuizLanguages(raw string) ([]quizLanguage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return append([]quizLanguage(nil), allQuizLanguages...), nil
	}

	normalized := strings.NewReplacer(
		",", " ",
		"，", " ",
		"、", " ",
		";", " ",
		"；", " ",
	).Replace(raw)
	fields := strings.Fields(normalized)
	languages := make([]quizLanguage, 0, len(fields))
	seen := make(map[quizLanguage]bool, len(fields))
	for _, field := range fields {
		var language quizLanguage
		switch strings.ToLower(field) {
		case "all", "全部", "混合":
			if len(fields) != 1 {
				return nil, fmt.Errorf("“全部”或“混合”不能与其他语言同时使用")
			}
			return append([]quizLanguage(nil), allQuizLanguages...), nil
		case "java":
			language = quizLanguageJava
		case "go", "golang":
			language = quizLanguageGo
		case "python", "py":
			language = quizLanguagePython
		case "c":
			language = quizLanguageC
		case "c++", "c＋＋", "cpp":
			language = quizLanguageCPP
		default:
			return nil, fmt.Errorf("暂不支持语言 %q", field)
		}
		if !seen[language] {
			seen[language] = true
			languages = append(languages, language)
		}
	}
	if len(languages) == 0 {
		return nil, fmt.Errorf("至少选择一种编程语言")
	}
	return languages, nil
}

// ============================================================
// 轮次状态
// ============================================================

const (
	// quizPluginID 沿用历史插件命名空间，避免破坏已有启用配置。
	quizPluginID = "guess_number"
	// quizQuestionCount 每轮固定题数。
	quizQuestionCount = 5
	// quizQuestionTimeout 每道题的固定作答时间。
	quizQuestionTimeout = time.Minute
	// quizAttemptsPerQuestion 每位玩家在同一道题中的最多作答次数。
	quizAttemptsPerQuestion = 2
	// quizStreamBuffer 同时容纳首题与极端情况下紧随其后的超时通知。
	quizStreamBuffer = 2
	// quizBackgroundTimeout 限制后台计时器访问 KV 的最长时间。
	quizBackgroundTimeout = 5 * time.Second
)

// quizKVStore 是答题状态所需的受限 KV 最小接口。
type quizKVStore interface {
	Get(context.Context, string, string) (string, error)
	Set(context.Context, string, string, string) error
	Delete(context.Context, string, string) error
}

// quizGame 保存一轮编程答题的持久化状态。
type quizGame struct {
	Languages        []quizLanguage    `json:"languages"`
	Questions        []quizQuestion    `json:"questions"`
	Current          int               `json:"current"`
	Scores           map[string]int    `json:"scores"`
	Players          map[string]string `json:"players"`
	AnswerAttempts   map[string]int    `json:"answer_attempts"`
	Creator          string            `json:"creator"`
	CreatedAt        int64             `json:"created_at"`
	QuestionDeadline int64             `json:"question_deadline"`
}

// quizRuntime 保存无法持久化的单进程计时器与主动消息通道。
type quizRuntime struct {
	stream     chan string
	timer      *time.Timer
	deadline   int64
	generation uint64
}

// quizKey 生成轮次 KV 键：群聊按群 ID 隔离，私聊按用户 ID 隔离。
func quizKey(groupID, userID string) string {
	if groupID != "" {
		return "state:group:" + groupID
	}
	return "state:dm:" + userID
}

// ============================================================
// 题目抽取与展示
// ============================================================

// selectQuizQuestions 从所选语言中均衡抽取题目；同轮题目不重复。
func selectQuizQuestions(languages []quizLanguage, count int) ([]quizQuestion, error) {
	if len(languages) == 0 || count <= 0 {
		return nil, fmt.Errorf("题目抽取参数无效")
	}

	order := append([]quizLanguage(nil), languages...)
	shuffleQuizLanguages(order)
	pools := make(map[quizLanguage][]quizQuestion, len(order))
	positions := make(map[quizLanguage]int, len(order))
	for _, language := range order {
		questions := append([]quizQuestion(nil), quizQuestionBank[language]...)
		if len(questions) == 0 {
			return nil, fmt.Errorf("%s 题库为空", language)
		}
		shuffleQuizQuestions(questions)
		pools[language] = questions
	}

	selected := make([]quizQuestion, 0, count)
	for len(selected) < count {
		added := false
		for _, language := range order {
			position := positions[language]
			if position >= len(pools[language]) {
				continue
			}
			selected = append(selected, pools[language][position])
			positions[language] = position + 1
			added = true
			if len(selected) == count {
				break
			}
		}
		if !added {
			return nil, fmt.Errorf("所选题库不足 %d 题", count)
		}
	}
	shuffleQuizQuestions(selected)
	return selected, nil
}

func shuffleQuizLanguages(values []quizLanguage) {
	for i := len(values) - 1; i > 0; i-- {
		j := rand.IntN(i + 1)
		values[i], values[j] = values[j], values[i]
	}
}

func shuffleQuizQuestions(values []quizQuestion) {
	for i := len(values) - 1; i > 0; i-- {
		j := rand.IntN(i + 1)
		values[i], values[j] = values[j], values[i]
	}
}

func quizChoiceLetter(index int) string {
	if index < 0 || index > 3 {
		return "?"
	}
	return string(rune('A' + index))
}

func formatQuizLanguages(languages []quizLanguage) string {
	names := make([]string, 0, len(languages))
	for _, language := range languages {
		names = append(names, language.String())
	}
	return strings.Join(names, "、")
}

func formatQuizQuestion(game *quizGame, timeout time.Duration) string {
	question := game.Questions[game.Current]
	seconds := int((timeout + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf(
		"📘 第 %d/%d 题 [%s]\n%s\n\nA. %s\nB. %s\nC. %s\nD. %s\n\n请直接发送 A/B/C/D 抢答（%d 秒内，每人本题最多作答 %d 次）。",
		game.Current+1,
		len(game.Questions),
		question.Language,
		question.Prompt,
		question.Options[0],
		question.Options[1],
		question.Options[2],
		question.Options[3],
		seconds,
		quizAttemptsPerQuestion,
	)
}

func formatQuizRoundStart(game *quizGame, timeout time.Duration) string {
	return fmt.Sprintf(
		"🎓 编程答题开始！\n本轮语言：%s\n规则：共 %d 题，答对 +1 分；每题限时一分钟，超时整轮结束；每人每题最多作答 %d 次。\n\n%s",
		formatQuizLanguages(game.Languages),
		len(game.Questions),
		quizAttemptsPerQuestion,
		formatQuizQuestion(game, timeout),
	)
}

func formatQuizAnswer(question quizQuestion) string {
	return fmt.Sprintf("答案：%s. %s\n解析：%s",
		quizChoiceLetter(question.AnswerIndex),
		question.Options[question.AnswerIndex],
		question.Explanation,
	)
}

type quizRankingEntry struct {
	UserID string
	Name   string
	Score  int
}

func formatQuizLeaderboard(game *quizGame) string {
	entries := make([]quizRankingEntry, 0, len(game.Players))
	for userID, name := range game.Players {
		entries = append(entries, quizRankingEntry{UserID: userID, Name: name, Score: game.Scores[userID]})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].UserID < entries[j].UserID
	})

	var builder strings.Builder
	builder.WriteString("🏆 本轮积分排行榜")
	for i, entry := range entries {
		builder.WriteString(fmt.Sprintf("\n%d. %s — %d 分", i+1, entry.Name, entry.Score))
	}
	return builder.String()
}

// ============================================================
// Pass 实现
// ============================================================

// quizPass 按命令处理开局与抢答，并串行保护轮次状态和计时器。
type quizPass struct {
	kv               quizKVStore
	mu               sync.Mutex
	runtimes         map[string]*quizRuntime
	generationSerial uint64
	questionTimeout  time.Duration
	now              func() time.Time
}

func newQuizPass(kv quizKVStore) *quizPass {
	return &quizPass{
		kv:              kv,
		runtimes:        make(map[string]*quizRuntime),
		questionTimeout: quizQuestionTimeout,
		now:             time.Now,
	}
}

func (pass *quizPass) Execute(ctx *conduit.MessageContext) error {
	if pass.kv == nil {
		pass.reply(ctx, "编程答题功能暂时不可用，请稍后再试~")
		return nil
	}

	msg := strings.TrimSpace(ctx.RawMsg)
	switch {
	case msg == "/答题":
		return pass.start(ctx, "")
	case strings.HasPrefix(msg, "/答题 "):
		argument := strings.TrimSpace(strings.TrimPrefix(msg, "/答题"))
		return pass.start(ctx, argument)
	default:
		choice, ok := parseQuizChoice(msg)
		if !ok {
			return nil
		}
		return pass.answer(ctx, choice)
	}
}

func (pass *quizPass) reply(ctx *conduit.MessageContext, content string) {
	conduit.AppendOutput(ctx, &conduit.Message{
		UserID:  ctx.UserID,
		GroupID: ctx.GroupID,
		IsGroup: ctx.IsGroup,
		Content: content,
	})
}

// replyUser 在 QQ 群聊中 @ 作答用户；其他平台或私聊降级为带昵称的文本。
// at 段统一使用 OneBot 12 的 user_id，网关发送 OB11 时会转换为 qq 字段。
func (pass *quizPass) replyUser(ctx *conduit.MessageContext, content string) {
	platform, _ := ctx.Extra["platform"].(string)
	if !ctx.IsGroup || ctx.UserID == "" || (platform != "qq" && platform != "napcat") {
		pass.reply(ctx, "@"+quizPlayerName(ctx)+content)
		return
	}
	conduit.Set(ctx, sendSegmentsKey, []map[string]any{
		{"type": "at", "data": map[string]any{"user_id": ctx.UserID}},
		{"type": "text", "data": map[string]any{"text": content}},
	})
}

func quizPlayerName(ctx *conduit.MessageContext) string {
	if nickname, _ := ctx.Extra["nickname"].(string); strings.TrimSpace(nickname) != "" {
		return strings.TrimSpace(nickname)
	}
	if strings.TrimSpace(ctx.UserID) != "" {
		return strings.TrimSpace(ctx.UserID)
	}
	return "匿名玩家"
}

// isActiveQuizAnswer 只在当前会话有持久化轮次时接管裸 A～D，
// 避免普通聊天中的单个字母被插件无条件吞掉。
func (pass *quizPass) isActiveQuizAnswer(ctx *conduit.MessageContext) bool {
	if !isQuizChoiceMessage(ctx) {
		return false
	}
	// 在条件节点尽早记录玩家看到的题目代次。若多人同时抢答，第一位答对后
	// 代次会递增，仍在管线中排队的旧题答案不会误算到下一题。
	pass.mu.Lock()
	runtime := pass.runtimes[quizKey(ctx.GroupID, ctx.UserID)]
	if runtime != nil {
		conduit.Set(ctx, quizObservedGenerationKey, runtime.generation)
	}
	pass.mu.Unlock()
	if runtime != nil {
		return true
	}
	// 服务重启后进程内计时器会丢失，但 KV 中可能仍有待清理的轮次；
	// 这种情况下仍接管一次答案消息，由 Pass 给出“轮次中断”提示并清理状态。
	return pass.hasStoredGame(ctx)
}

func (pass *quizPass) hasStoredGame(ctx *conduit.MessageContext) bool {
	if pass.kv == nil {
		return false
	}
	raw, err := pass.kv.Get(ctx.Ctx, quizPluginID, quizKey(ctx.GroupID, ctx.UserID))
	return err == nil && raw != ""
}

// start 开始一轮；同一群或私聊已有未超时轮次时拒绝重复开局。
func (pass *quizPass) start(ctx *conduit.MessageContext, rawLanguages string) error {
	// 插件命令的自然语言意图路径使用同步 Process 重入，当前不会消费 yield 的
	// 流式通道。此处不创建“题目未发出但已经计时”的隐形轮次，明确引导用户
	// 发送斜杠命令；直接输入 /答题 会由高优先级插件子树正常进入异步路径。
	if commandReentry, _ := ctx.Extra[quizCommandReentryKey].(bool); commandReentry {
		command := "/答题"
		if strings.TrimSpace(rawLanguages) != "" {
			command += " " + strings.TrimSpace(rawLanguages)
		}
		pass.reply(ctx, "请直接发送 `"+command+"` 开始编程答题~")
		return nil
	}

	languages, err := parseQuizLanguages(rawLanguages)
	if err != nil {
		pass.reply(ctx, err.Error()+"。\n支持 Java、Go、Python、C、C++；例如：/答题 go python。不写语言时为五种混合。")
		return nil
	}

	pass.mu.Lock()
	defer pass.mu.Unlock()

	key := quizKey(ctx.GroupID, ctx.UserID)
	existing, expired, err := pass.load(ctx.Ctx, key)
	if err != nil {
		return fmt.Errorf("guess_number: load quiz: %w", err)
	}
	prefix := ""
	if existing != nil && !expired {
		if _, running := pass.runtimes[key]; running {
			pass.reply(ctx, fmt.Sprintf("本群已经有一轮编程答题在进行啦！当前是第 %d/%d 题，请直接发送 A/B/C/D 抢答。", existing.Current+1, len(existing.Questions)))
			return nil
		}
		if err := pass.kv.Delete(ctx.Ctx, quizPluginID, key); err != nil {
			return fmt.Errorf("guess_number: clear interrupted quiz: %w", err)
		}
		prefix = "上一轮因服务重启中断，积分已清零。\n"
	}
	if existing != nil && expired {
		if err := pass.kv.Delete(ctx.Ctx, quizPluginID, key); err != nil {
			return fmt.Errorf("guess_number: clear expired quiz: %w", err)
		}
		prefix = "上一轮已超时结束，积分已清零。\n"
	}
	pass.finishRuntimeLocked(key, "")

	questions, err := selectQuizQuestions(languages, quizQuestionCount)
	if err != nil {
		return fmt.Errorf("guess_number: select questions: %w", err)
	}
	now := pass.currentTime()
	game := &quizGame{
		Languages:        languages,
		Questions:        questions,
		Scores:           make(map[string]int),
		Players:          make(map[string]string),
		AnswerAttempts:   make(map[string]int),
		Creator:          ctx.UserID,
		CreatedAt:        now.UnixNano(),
		QuestionDeadline: now.Add(pass.timeout()).UnixNano(),
	}
	if err := pass.save(ctx.Ctx, key, game); err != nil {
		return fmt.Errorf("guess_number: save quiz: %w", err)
	}

	stream := make(chan string, quizStreamBuffer)
	pass.runtimes[key] = &quizRuntime{stream: stream, generation: pass.nextGenerationLocked()}
	conduit.Set(ctx, quizStreamChannelKey, stream)
	stream <- prefix + formatQuizRoundStart(game, pass.timeout())
	pass.armTimeoutLocked(key, game.QuestionDeadline)
	return conduit.ErrPassYielded
}

// answer 提交一个选项。第一个答对者得分并推进题目，每位玩家每题最多作答两次。
func (pass *quizPass) answer(ctx *conduit.MessageContext, choice int) error {
	key := quizKey(ctx.GroupID, ctx.UserID)
	observedGeneration, ok := conduit.Get[uint64](ctx, quizObservedGenerationKey)
	if !ok {
		pass.mu.Lock()
		if runtime := pass.runtimes[key]; runtime != nil {
			observedGeneration = runtime.generation
		}
		pass.mu.Unlock()
	}
	return pass.answerForGeneration(ctx, choice, observedGeneration)
}

// answerForGeneration 只把答案计入玩家开始处理时所看到的题目代次。
func (pass *quizPass) answerForGeneration(ctx *conduit.MessageContext, choice int, observedGeneration uint64) error {
	pass.mu.Lock()
	defer pass.mu.Unlock()

	key := quizKey(ctx.GroupID, ctx.UserID)
	game, expired, err := pass.load(ctx.Ctx, key)
	if err != nil {
		return fmt.Errorf("guess_number: load quiz: %w", err)
	}
	if game == nil {
		pass.finishRuntimeLocked(key, "")
		pass.reply(ctx, "现在没有进行中的编程答题，用 `/答题` 开始一轮吧。")
		return nil
	}
	if expired {
		question := game.Questions[game.Current]
		if err := pass.kv.Delete(ctx.Ctx, quizPluginID, key); err != nil {
			return fmt.Errorf("guess_number: expire quiz: %w", err)
		}
		pass.finishRuntimeLocked(key, "")
		pass.reply(ctx, fmt.Sprintf("⏰ 第 %d 题已超时，%s。\n本轮直接结束，积分已清零。", game.Current+1, formatQuizAnswer(question)))
		return nil
	}
	runtime, running := pass.runtimes[key]
	if !running {
		if err := pass.kv.Delete(ctx.Ctx, quizPluginID, key); err != nil {
			return fmt.Errorf("guess_number: clear interrupted quiz: %w", err)
		}
		pass.reply(ctx, "本轮因服务重启中断，积分已清零。请用 `/答题` 重新开始。")
		return nil
	}
	if runtime.generation != observedGeneration {
		pass.replyUser(ctx, " 上一题已经有人答对啦，请回答最新题目~")
		return nil
	}
	if strings.TrimSpace(ctx.UserID) == "" {
		pass.reply(ctx, "无法识别你的用户 ID，暂时不能参与抢答~")
		return nil
	}

	userID := ctx.UserID
	attempts := game.AnswerAttempts[userID]
	if attempts >= quizAttemptsPerQuestion {
		pass.replyUser(ctx, " 本题的两次作答机会已经用完啦，其他人继续抢答~")
		return nil
	}
	game.Players[userID] = quizPlayerName(ctx)
	game.AnswerAttempts[userID] = attempts + 1
	question := game.Questions[game.Current]
	if choice != question.AnswerIndex {
		if err := pass.save(ctx.Ctx, key, game); err != nil {
			return fmt.Errorf("guess_number: save wrong answer: %w", err)
		}
		remaining := quizAttemptsPerQuestion - game.AnswerAttempts[userID]
		if remaining > 0 {
			pass.replyUser(ctx, fmt.Sprintf(" 答错了，本题还剩 %d 次机会，继续加油~", remaining))
		} else {
			pass.replyUser(ctx, " 答错了，本题的两次机会已经用完，其他人继续抢答~")
		}
		return nil
	}

	game.Scores[userID]++
	currentScore := game.Scores[userID]
	game.Current++
	if game.Current == len(game.Questions) {
		if err := pass.kv.Delete(ctx.Ctx, quizPluginID, key); err != nil {
			return fmt.Errorf("guess_number: finish quiz: %w", err)
		}
		pass.finishRuntimeLocked(key, "")
		pass.replyUser(ctx, fmt.Sprintf(
			" 🎉 答对啦！+1 分，本轮五题全部完成。\n✅ %s\n\n%s\n\n本轮积分现已清零，再用 `/答题` 可以开始新一轮。",
			formatQuizAnswer(question),
			formatQuizLeaderboard(game),
		))
		return nil
	}

	game.AnswerAttempts = make(map[string]int)
	game.QuestionDeadline = pass.currentTime().Add(pass.timeout()).UnixNano()
	if err := pass.save(ctx.Ctx, key, game); err != nil {
		return fmt.Errorf("guess_number: advance quiz: %w", err)
	}
	runtime.generation = pass.nextGenerationLocked()
	pass.armTimeoutLocked(key, game.QuestionDeadline)
	pass.replyUser(ctx, fmt.Sprintf(
		" 🎉 答对啦！+1 分，当前 %d 分。\n✅ %s\n\n%s",
		currentScore,
		formatQuizAnswer(question),
		formatQuizQuestion(game, pass.timeout()),
	))
	return nil
}

// ============================================================
// 持久化与主动超时
// ============================================================

// load 读取当前轮次，并返回当前题是否已到截止时间。
// 历史猜数字状态或损坏状态会被识别并清理。
func (pass *quizPass) load(ctx context.Context, key string) (*quizGame, bool, error) {
	raw, err := pass.kv.Get(ctx, quizPluginID, key)
	if err != nil || raw == "" {
		return nil, false, err
	}
	var game quizGame
	if err := json.Unmarshal([]byte(raw), &game); err != nil {
		if deleteErr := pass.kv.Delete(ctx, quizPluginID, key); deleteErr != nil {
			return nil, false, fmt.Errorf("delete malformed state after decode error %v: %w", err, deleteErr)
		}
		return nil, false, nil
	}
	if !validQuizGame(&game) {
		if err := pass.kv.Delete(ctx, quizPluginID, key); err != nil {
			return nil, false, fmt.Errorf("delete invalid state: %w", err)
		}
		return nil, false, nil
	}
	if game.Scores == nil {
		game.Scores = make(map[string]int)
	}
	if game.Players == nil {
		game.Players = make(map[string]string)
	}
	if game.AnswerAttempts == nil {
		game.AnswerAttempts = make(map[string]int)
	}
	expired := !pass.currentTime().Before(time.Unix(0, game.QuestionDeadline))
	return &game, expired, nil
}

func validQuizGame(game *quizGame) bool {
	if game == nil || len(game.Languages) == 0 || len(game.Questions) != quizQuestionCount {
		return false
	}
	if game.Current < 0 || game.Current >= len(game.Questions) || game.QuestionDeadline <= 0 {
		return false
	}
	for _, question := range game.Questions {
		if !validQuizQuestion(question) {
			return false
		}
	}
	return true
}

func validQuizQuestion(question quizQuestion) bool {
	if question.ID == "" || question.Prompt == "" || question.Explanation == "" || question.AnswerIndex < 0 || question.AnswerIndex > 3 {
		return false
	}
	for _, option := range question.Options {
		if strings.TrimSpace(option) == "" {
			return false
		}
	}
	return true
}

// save 保存当前轮次。
func (pass *quizPass) save(ctx context.Context, key string, game *quizGame) error {
	data, err := json.Marshal(game)
	if err != nil {
		return err
	}
	return pass.kv.Set(ctx, quizPluginID, key, string(data))
}

func (pass *quizPass) currentTime() time.Time {
	if pass.now != nil {
		return pass.now()
	}
	return time.Now()
}

func (pass *quizPass) timeout() time.Duration {
	if pass.questionTimeout > 0 {
		return pass.questionTimeout
	}
	return quizQuestionTimeout
}

// nextGenerationLocked 为每一道新题分配进程内唯一代次。调用方必须持有 pass.mu。
func (pass *quizPass) nextGenerationLocked() uint64 {
	pass.generationSerial++
	if pass.generationSerial == 0 {
		pass.generationSerial++
	}
	return pass.generationSerial
}

// armTimeoutLocked 为当前题目启动或重置主动超时计时器。调用方必须持有 pass.mu。
func (pass *quizPass) armTimeoutLocked(key string, deadline int64) {
	runtime := pass.runtimes[key]
	if runtime == nil {
		return
	}
	if runtime.timer != nil {
		runtime.timer.Stop()
	}
	runtime.deadline = deadline
	delay := time.Unix(0, deadline).Sub(pass.currentTime())
	if delay < 0 {
		delay = 0
	}
	runtime.timer = time.AfterFunc(delay, func() {
		pass.expire(key, deadline, runtime)
	})
}

// expire 在一分钟截止时删除整轮状态，并通过开局消息的异步通道主动通知会话。
func (pass *quizPass) expire(key string, deadline int64, expected *quizRuntime) {
	ctx, cancel := context.WithTimeout(context.Background(), quizBackgroundTimeout)
	defer cancel()

	pass.mu.Lock()
	defer pass.mu.Unlock()

	runtime := pass.runtimes[key]
	if runtime == nil || runtime != expected || runtime.deadline != deadline {
		return
	}
	game, expired, err := pass.load(ctx, key)
	if err != nil {
		pass.finishRuntimeLocked(key, "⏰ 本题计时结束，本轮已结束，积分已清零。")
		return
	}
	if game == nil {
		pass.finishRuntimeLocked(key, "")
		return
	}
	if game.QuestionDeadline != deadline {
		return
	}
	if !expired {
		pass.armTimeoutLocked(key, deadline)
		return
	}

	_ = pass.kv.Delete(ctx, quizPluginID, key)
	question := game.Questions[game.Current]
	pass.finishRuntimeLocked(key, fmt.Sprintf(
		"⏰ 第 %d 题已超过一分钟，%s。\n本轮直接结束，积分已清零。",
		game.Current+1,
		formatQuizAnswer(question),
	))
}

// finishRuntimeLocked 停止计时器，可选发送最后一条主动消息，然后关闭通道。
// 调用方必须持有 pass.mu。
func (pass *quizPass) finishRuntimeLocked(key, finalMessage string) {
	runtime := pass.runtimes[key]
	if runtime == nil {
		return
	}
	if runtime.timer != nil {
		runtime.timer.Stop()
	}
	delete(pass.runtimes, key)
	if finalMessage != "" {
		select {
		case runtime.stream <- finalMessage:
		default:
		}
	}
	close(runtime.stream)
}

// stopAll 在插件停止时释放所有进程内计时器和通道。
func (pass *quizPass) stopAll() {
	pass.mu.Lock()
	defer pass.mu.Unlock()
	for key := range pass.runtimes {
		pass.finishRuntimeLocked(key, "")
	}
}

const (
	// quizStreamChannelKey 复用 bot 层流式段落通道，让一分钟超时可以主动发送消息。
	quizStreamChannelKey = "bot.stream.ch"
	// quizObservedGenerationKey 保存答案消息进入行为树时看到的题目代次。
	quizObservedGenerationKey = "quiz.observed_generation"
	// quizCommandReentryKey 标记由自然语言意图触发的插件命令同步重入。
	quizCommandReentryKey = "bot.command.reentry"
)
