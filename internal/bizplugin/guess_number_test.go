package bizplugin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zrurf/conduit"
)

// memoryQuizKV 是 quizKVStore 的并发安全内存实现，仅用于单元测试。
type memoryQuizKV struct {
	mu   sync.RWMutex
	data map[string]string
}

func newMemoryQuizKV() *memoryQuizKV {
	return &memoryQuizKV{data: make(map[string]string)}
}

func (kv *memoryQuizKV) Get(_ context.Context, pluginID, key string) (string, error) {
	kv.mu.RLock()
	defer kv.mu.RUnlock()
	return kv.data[pluginID+"\x00"+key], nil
}

func (kv *memoryQuizKV) Set(_ context.Context, pluginID, key, value string) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	kv.data[pluginID+"\x00"+key] = value
	return nil
}

func (kv *memoryQuizKV) Delete(_ context.Context, pluginID, key string) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	delete(kv.data, pluginID+"\x00"+key)
	return nil
}

func newQuizTestContext(t *testing.T, userID, groupID, content, nickname, platform string) *conduit.MessageContext {
	t.Helper()
	ctx := conduit.NewMessageContext(&conduit.InputMessage{
		UserID:  userID,
		GroupID: groupID,
		Content: content,
		IsGroup: groupID != "",
		Extra: map[string]any{
			"nickname": nickname,
			"platform": platform,
		},
	})
	t.Cleanup(ctx.Cancel)
	return ctx
}

func outputContains(ctx *conduit.MessageContext, want string) bool {
	for _, output := range ctx.Output {
		if strings.Contains(output.Content, want) {
			return true
		}
	}
	return false
}

func segmentText(ctx *conduit.MessageContext) string {
	segments, _ := conduit.Get[[]map[string]any](ctx, sendSegmentsKey)
	var builder strings.Builder
	for _, segment := range segments {
		if segment["type"] != "text" {
			continue
		}
		data, _ := segment["data"].(map[string]any)
		text, _ := data["text"].(string)
		builder.WriteString(text)
	}
	return builder.String()
}

func assertMentionedUser(t *testing.T, ctx *conduit.MessageContext, wantUserID string) {
	t.Helper()
	segments, ok := conduit.Get[[]map[string]any](ctx, sendSegmentsKey)
	if !ok || len(segments) < 2 {
		t.Fatalf("expected at/text segments, got %#v", segments)
	}
	if segments[0]["type"] != "at" {
		t.Fatalf("first segment type = %#v, want at", segments[0]["type"])
	}
	data, _ := segments[0]["data"].(map[string]any)
	if got, _ := data["user_id"].(string); got != wantUserID {
		t.Fatalf("mentioned user_id = %q, want %q", got, wantUserID)
	}
}

func TestQuizQuestionBank(t *testing.T) {
	seenIDs := make(map[string]bool)
	for _, language := range allQuizLanguages {
		questions := quizQuestionBank[language]
		if len(questions) < 30 {
			t.Fatalf("%s question count = %d, want at least 30", language, len(questions))
		}
		for _, question := range questions {
			if question.Language != language {
				t.Errorf("question %q language = %q, want %q", question.ID, question.Language, language)
			}
			if !validQuizQuestion(question) {
				t.Errorf("question %q is invalid", question.ID)
			}
			for index, option := range question.Options {
				for other := index + 1; other < len(question.Options); other++ {
					if option == question.Options[other] {
						t.Errorf("question %q has duplicate option %q", question.ID, option)
					}
				}
			}
			if seenIDs[question.ID] {
				t.Errorf("duplicate question ID %q", question.ID)
			}
			seenIDs[question.ID] = true
		}
	}
}

func TestParseQuizLanguages(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []quizLanguage
	}{
		{name: "默认混合", raw: "", want: allQuizLanguages},
		{name: "单语言别名", raw: "golang", want: []quizLanguage{quizLanguageGo}},
		{name: "多语言与去重", raw: "Java、go python,java", want: []quizLanguage{quizLanguageJava, quizLanguageGo, quizLanguagePython}},
		{name: "C++ 别名", raw: "cpp C", want: []quizLanguage{quizLanguageCPP, quizLanguageC}},
		{name: "全部", raw: "全部", want: allQuizLanguages},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseQuizLanguages(test.raw)
			if err != nil {
				t.Fatalf("parseQuizLanguages(%q): %v", test.raw, err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("languages = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("languages = %v, want %v", got, test.want)
				}
			}
		})
	}
	if _, err := parseQuizLanguages("rust"); err == nil {
		t.Fatal("unsupported language should return an error")
	}
	if _, err := parseQuizLanguages("all rust"); err == nil {
		t.Fatal("all combined with another language should return an error")
	}
}

func TestParseQuizChoice(t *testing.T) {
	tests := map[string]int{
		"A":    0,
		" b ":  1,
		"答案 C": 2,
		"Ｄ":    3,
	}
	for raw, want := range tests {
		got, ok := parseQuizChoice(raw)
		if !ok || got != want {
			t.Errorf("parseQuizChoice(%q) = (%d, %v), want (%d, true)", raw, got, ok, want)
		}
	}
	if _, ok := parseQuizChoice("E"); ok {
		t.Fatal("choice E should be rejected")
	}
}

func TestSelectQuizQuestions(t *testing.T) {
	selected, err := selectQuizQuestions(allQuizLanguages, quizQuestionCount)
	if err != nil {
		t.Fatalf("select mixed questions: %v", err)
	}
	seenLanguages := make(map[quizLanguage]bool)
	seenIDs := make(map[string]bool)
	for _, question := range selected {
		seenLanguages[question.Language] = true
		if seenIDs[question.ID] {
			t.Fatalf("duplicate selected question %q", question.ID)
		}
		seenIDs[question.ID] = true
	}
	if len(seenLanguages) != len(allQuizLanguages) {
		t.Fatalf("mixed round languages = %v, want all %v", seenLanguages, allQuizLanguages)
	}

	selected, err = selectQuizQuestions([]quizLanguage{quizLanguageGo}, quizQuestionCount)
	if err != nil {
		t.Fatalf("select Go questions: %v", err)
	}
	for _, question := range selected {
		if question.Language != quizLanguageGo {
			t.Fatalf("selected language = %s, want Go", question.Language)
		}
	}
}

func TestQuizRoundFlowAndScoreReset(t *testing.T) {
	kv := newMemoryQuizKV()
	pass := newQuizPass(kv)
	t.Cleanup(pass.stopAll)

	startCtx := newQuizTestContext(t, "creator", "group-1", "/答题 go", "开局者", "qq")
	if err := pass.Execute(startCtx); !errors.Is(err, conduit.ErrPassYielded) {
		t.Fatalf("start error = %v, want ErrPassYielded", err)
	}
	stream, ok := conduit.Get[chan string](startCtx, quizStreamChannelKey)
	if !ok {
		t.Fatal("start should register a stream channel")
	}
	firstQuestion := <-stream
	if !strings.Contains(firstQuestion, "第 1/5 题 [Go]") {
		t.Fatalf("first question = %q", firstQuestion)
	}

	duplicateStart := newQuizTestContext(t, "another", "group-1", "/答题 python", "另一位", "qq")
	if err := pass.Execute(duplicateStart); err != nil {
		t.Fatalf("duplicate start: %v", err)
	}
	if !outputContains(duplicateStart, "已经有一轮") {
		t.Fatalf("duplicate start output = %#v", duplicateStart.Output)
	}

	key := quizKey("group-1", "creator")
	game, expired, err := pass.load(context.Background(), key)
	if err != nil || expired || game == nil {
		t.Fatalf("load started game = (%#v, %v, %v)", game, expired, err)
	}
	wrongChoice := (game.Questions[0].AnswerIndex + 1) % 4
	wrongCtx := newQuizTestContext(t, "wrong-user", "group-1", quizChoiceLetter(wrongChoice), "小错", "qq")
	if err := pass.Execute(wrongCtx); err != nil {
		t.Fatalf("wrong answer: %v", err)
	}
	assertMentionedUser(t, wrongCtx, "wrong-user")
	if !strings.Contains(segmentText(wrongCtx), "答错了") {
		t.Fatalf("wrong answer text = %q", segmentText(wrongCtx))
	}
	if !strings.Contains(segmentText(wrongCtx), "还剩 1 次机会") {
		t.Fatalf("first wrong answer should keep one chance, text = %q", segmentText(wrongCtx))
	}

	game, _, _ = pass.load(context.Background(), key)
	secondWrongCtx := newQuizTestContext(t, "wrong-user", "group-1", quizChoiceLetter(wrongChoice), "小错", "qq")
	if err := pass.Execute(secondWrongCtx); err != nil {
		t.Fatalf("second wrong answer: %v", err)
	}
	assertMentionedUser(t, secondWrongCtx, "wrong-user")
	if !strings.Contains(segmentText(secondWrongCtx), "两次机会已经用完") {
		t.Fatalf("second wrong answer text = %q", segmentText(secondWrongCtx))
	}

	game, _, _ = pass.load(context.Background(), key)
	exhaustedCtx := newQuizTestContext(t, "wrong-user", "group-1", quizChoiceLetter(game.Questions[0].AnswerIndex), "小错", "qq")
	if err := pass.Execute(exhaustedCtx); err != nil {
		t.Fatalf("answer after two attempts: %v", err)
	}
	assertMentionedUser(t, exhaustedCtx, "wrong-user")
	if !strings.Contains(segmentText(exhaustedCtx), "两次作答机会已经用完") {
		t.Fatalf("exhausted answer text = %q", segmentText(exhaustedCtx))
	}

	correctCtx := newQuizTestContext(t, "winner", "group-1", quizChoiceLetter(game.Questions[0].AnswerIndex), "小蓝", "qq")
	if err := pass.Execute(correctCtx); err != nil {
		t.Fatalf("correct answer: %v", err)
	}
	assertMentionedUser(t, correctCtx, "winner")
	if text := segmentText(correctCtx); !strings.Contains(text, "答对啦") || !strings.Contains(text, "第 2/5 题") {
		t.Fatalf("correct answer text = %q", text)
	}

	game, expired, err = pass.load(context.Background(), key)
	if err != nil || expired || game == nil {
		t.Fatalf("load advanced game = (%#v, %v, %v)", game, expired, err)
	}
	if game.Current != 1 || game.Scores["winner"] != 1 || len(game.AnswerAttempts) != 0 {
		t.Fatalf("advanced game state = %#v", game)
	}

	var finalCtx *conduit.MessageContext
	for questionIndex := 1; questionIndex < quizQuestionCount; questionIndex++ {
		game, _, err = pass.load(context.Background(), key)
		if err != nil || game == nil {
			t.Fatalf("load question %d: game=%#v err=%v", questionIndex+1, game, err)
		}
		answerCtx := newQuizTestContext(t, "winner", "group-1", quizChoiceLetter(game.Questions[game.Current].AnswerIndex), "小蓝", "qq")
		if err := pass.Execute(answerCtx); err != nil {
			t.Fatalf("answer question %d: %v", questionIndex+1, err)
		}
		finalCtx = answerCtx
	}

	assertMentionedUser(t, finalCtx, "winner")
	finalText := segmentText(finalCtx)
	if !strings.Contains(finalText, "本轮积分排行榜") || !strings.Contains(finalText, "小蓝 — 5 分") || !strings.Contains(finalText, "小错 — 0 分") {
		t.Fatalf("final leaderboard = %q", finalText)
	}
	if raw, _ := kv.Get(context.Background(), quizPluginID, key); raw != "" {
		t.Fatalf("finished round state should be deleted, got %q", raw)
	}
	if _, open := <-stream; open {
		t.Fatal("round stream should close after the fifth question")
	}

	newRoundCtx := newQuizTestContext(t, "creator", "group-1", "/答题 python c++", "开局者", "qq")
	if err := pass.Execute(newRoundCtx); !errors.Is(err, conduit.ErrPassYielded) {
		t.Fatalf("start new round error = %v", err)
	}
	newStream, _ := conduit.Get[chan string](newRoundCtx, quizStreamChannelKey)
	<-newStream
	game, expired, err = pass.load(context.Background(), key)
	if err != nil || expired || game == nil {
		t.Fatalf("load new round = (%#v, %v, %v)", game, expired, err)
	}
	if len(game.Scores) != 0 || len(game.Players) != 0 {
		t.Fatalf("new round should reset scores and players: %#v", game)
	}
}

func TestQuizSecondAttemptCanScore(t *testing.T) {
	kv := newMemoryQuizKV()
	pass := newQuizPass(kv)
	t.Cleanup(pass.stopAll)

	startCtx := newQuizTestContext(t, "creator", "group-second-chance", "/答题 c", "开局者", "qq")
	if err := pass.Execute(startCtx); !errors.Is(err, conduit.ErrPassYielded) {
		t.Fatalf("start error = %v, want ErrPassYielded", err)
	}
	stream, _ := conduit.Get[chan string](startCtx, quizStreamChannelKey)
	<-stream

	key := quizKey("group-second-chance", "creator")
	game, _, err := pass.load(context.Background(), key)
	if err != nil || game == nil {
		t.Fatalf("load started game: game=%#v err=%v", game, err)
	}
	question := game.Questions[game.Current]
	wrongChoice := (question.AnswerIndex + 1) % len(question.Options)

	firstCtx := newQuizTestContext(t, "player", "group-second-chance", quizChoiceLetter(wrongChoice), "小蓝", "napcat")
	if err := pass.Execute(firstCtx); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	assertMentionedUser(t, firstCtx, "player")
	if !strings.Contains(segmentText(firstCtx), "还剩 1 次机会") {
		t.Fatalf("first answer text = %q", segmentText(firstCtx))
	}

	secondCtx := newQuizTestContext(t, "player", "group-second-chance", quizChoiceLetter(question.AnswerIndex), "小蓝", "napcat")
	if err := pass.Execute(secondCtx); err != nil {
		t.Fatalf("second answer: %v", err)
	}
	assertMentionedUser(t, secondCtx, "player")
	if !strings.Contains(segmentText(secondCtx), "答对啦") {
		t.Fatalf("second answer text = %q", segmentText(secondCtx))
	}

	game, expired, err := pass.load(context.Background(), key)
	if err != nil || expired || game == nil {
		t.Fatalf("load advanced game = (%#v, %v, %v)", game, expired, err)
	}
	if game.Current != 1 || game.Scores["player"] != 1 || len(game.AnswerAttempts) != 0 {
		t.Fatalf("second-chance score state = %#v", game)
	}
}

func TestQuizReplyUserUsesRealMentionSegments(t *testing.T) {
	pass := newQuizPass(newMemoryQuizKV())
	for _, platform := range []string{"qq", "napcat"} {
		t.Run(platform, func(t *testing.T) {
			ctx := newQuizTestContext(t, "player-1", "group-mention", "A", "小蓝", platform)
			pass.replyUser(ctx, " 答对啦")
			assertMentionedUser(t, ctx, "player-1")
			if text := segmentText(ctx); text != " 答对啦" {
				t.Fatalf("mention text = %q", text)
			}
		})
	}

	fallbackCtx := newQuizTestContext(t, "player-2", "group-mention", "A", "小绿", "wechat")
	pass.replyUser(fallbackCtx, " 答错了")
	if conduit.Has(fallbackCtx, sendSegmentsKey) {
		t.Fatal("unsupported platform should not receive OneBot segments")
	}
	if !outputContains(fallbackCtx, "@小绿 答错了") {
		t.Fatalf("fallback output = %#v", fallbackCtx.Output)
	}
}

func TestQuizTimeoutEndsRound(t *testing.T) {
	kv := newMemoryQuizKV()
	pass := newQuizPass(kv)
	pass.questionTimeout = 25 * time.Millisecond
	t.Cleanup(pass.stopAll)

	startCtx := newQuizTestContext(t, "creator", "group-timeout", "/答题 java", "开局者", "qq")
	if err := pass.Execute(startCtx); !errors.Is(err, conduit.ErrPassYielded) {
		t.Fatalf("start error = %v, want ErrPassYielded", err)
	}
	stream, _ := conduit.Get[chan string](startCtx, quizStreamChannelKey)
	<-stream // 首题

	key := quizKey("group-timeout", "creator")
	game, _, err := pass.load(context.Background(), key)
	if err != nil || game == nil {
		t.Fatalf("load started game: game=%#v err=%v", game, err)
	}
	wantAnswer := "答案：" + quizChoiceLetter(game.Questions[0].AnswerIndex)

	select {
	case timeoutMessage, open := <-stream:
		if !open {
			t.Fatal("stream closed without a timeout message")
		}
		if !strings.Contains(timeoutMessage, "本轮直接结束") || !strings.Contains(timeoutMessage, wantAnswer) {
			t.Fatalf("timeout message = %q", timeoutMessage)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for proactive quiz timeout")
	}
	if _, open := <-stream; open {
		t.Fatal("stream should close after timeout notification")
	}
	if raw, _ := kv.Get(context.Background(), quizPluginID, key); raw != "" {
		t.Fatalf("timed out round state should be deleted, got %q", raw)
	}
}

func TestConcurrentCorrectAnswersOnlyAdvanceOnce(t *testing.T) {
	kv := newMemoryQuizKV()
	pass := newQuizPass(kv)
	t.Cleanup(pass.stopAll)

	startCtx := newQuizTestContext(t, "creator", "group-race", "/答题 python", "开局者", "qq")
	if err := pass.Execute(startCtx); !errors.Is(err, conduit.ErrPassYielded) {
		t.Fatalf("start error = %v, want ErrPassYielded", err)
	}
	stream, _ := conduit.Get[chan string](startCtx, quizStreamChannelKey)
	<-stream

	key := quizKey("group-race", "creator")
	game, _, err := pass.load(context.Background(), key)
	if err != nil || game == nil {
		t.Fatalf("load started game: game=%#v err=%v", game, err)
	}
	choice := game.Questions[0].AnswerIndex

	const players = 16
	contexts := make([]*conduit.MessageContext, players)
	for i := range contexts {
		userID := "player-" + quizChoiceLetter(i%4) + string(rune('a'+i))
		contexts[i] = newQuizTestContext(t, userID, "group-race", quizChoiceLetter(choice), userID, "qq")
		if !pass.isActiveQuizAnswer(contexts[i]) {
			t.Fatalf("answer condition should match player %q", userID)
		}
	}

	var waitGroup sync.WaitGroup
	errorsCh := make(chan error, players)
	for _, answerCtx := range contexts {
		waitGroup.Add(1)
		go func(ctx *conduit.MessageContext) {
			defer waitGroup.Done()
			errorsCh <- pass.Execute(ctx)
		}(answerCtx)
	}
	waitGroup.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent answer: %v", err)
		}
	}

	game, expired, err := pass.load(context.Background(), key)
	if err != nil || expired || game == nil {
		t.Fatalf("load advanced game = (%#v, %v, %v)", game, expired, err)
	}
	if game.Current != 1 {
		t.Fatalf("current question = %d, want 1", game.Current)
	}
	totalScore := 0
	for _, score := range game.Scores {
		totalScore += score
	}
	if totalScore != 1 {
		t.Fatalf("total score = %d, want exactly 1", totalScore)
	}
	staleAnswers := 0
	for _, answerCtx := range contexts {
		if strings.Contains(segmentText(answerCtx), "上一题已经有人答对") {
			staleAnswers++
		}
	}
	if staleAnswers != players-1 {
		t.Fatalf("stale answer replies = %d, want %d", staleAnswers, players-1)
	}
}

func TestQuizCommandReentryRequiresDirectSlashCommand(t *testing.T) {
	kv := newMemoryQuizKV()
	pass := newQuizPass(kv)
	t.Cleanup(pass.stopAll)

	ctx := newQuizTestContext(t, "creator", "group-reentry", "/答题 go", "开局者", "qq")
	ctx.Extra[quizCommandReentryKey] = true
	if err := pass.Execute(ctx); err != nil {
		t.Fatalf("command reentry: %v", err)
	}
	if !outputContains(ctx, "请直接发送 `/答题 go`") {
		t.Fatalf("command reentry output = %#v", ctx.Output)
	}
	if conduit.Has(ctx, quizStreamChannelKey) {
		t.Fatal("command reentry must not create an unconsumed stream")
	}
	key := quizKey("group-reentry", "creator")
	if raw, _ := kv.Get(context.Background(), quizPluginID, key); raw != "" {
		t.Fatalf("command reentry must not create quiz state, got %q", raw)
	}
}

func TestQuizCleansLegacyGuessNumberState(t *testing.T) {
	kv := newMemoryQuizKV()
	key := quizKey("group-legacy", "creator")
	if err := kv.Set(context.Background(), quizPluginID, key, `{"answer":42,"attempts":3,"created_at":1}`); err != nil {
		t.Fatal(err)
	}
	pass := newQuizPass(kv)
	t.Cleanup(pass.stopAll)

	startCtx := newQuizTestContext(t, "creator", "group-legacy", "/答题 c", "开局者", "qq")
	if err := pass.Execute(startCtx); !errors.Is(err, conduit.ErrPassYielded) {
		t.Fatalf("start with legacy state error = %v", err)
	}
	stream, _ := conduit.Get[chan string](startCtx, quizStreamChannelKey)
	if firstQuestion := <-stream; !strings.Contains(firstQuestion, "[C]") {
		t.Fatalf("first C question = %q", firstQuestion)
	}
}
