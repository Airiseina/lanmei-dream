package random_beauty

import (
	"context"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/media"
	"github.com/DaWesen/lanmei-dream/internal/model"
	pluginpkg "github.com/DaWesen/lanmei-dream/internal/plugin"
	"github.com/glebarez/sqlite"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
)

// newTestDB 创建独立的 SQLite 测试库（文件模式，避免内存库多连接丢 schema）。
func newTestDB(t *testing.T) *database.DB {
	t.Helper()
	orm, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "random_beauty.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	if err := orm.AutoMigrate(&model.RandomBeautyPool{}); err != nil {
		t.Fatalf("AutoMigrate() error = %v", err)
	}
	return &database.DB{Orm: orm}
}

// newTestPlugin 组装一个指向 httptest 假上游的插件（生产 New 的测试替身路径）。
// 生产路径 db 由 OnInit 注入，这里直接赋值以覆盖 OnInit 之前的补图/取图流程。
func newTestPlugin(t *testing.T, cfg config.RandomBeautyConfig, upstream http.HandlerFunc, store *media.ObjectStore, db *database.DB, moderator ImageModerator) *Plugin {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	normalized := normalizedConfig(cfg)
	client := &http.Client{Timeout: time.Duration(normalized.TimeoutSeconds) * time.Second}
	provider, err := newRandomMageClient(srv.URL, client, normalized.MinWidth, normalized.MinHeight, normalized.MinBookmarks, true)
	if err != nil {
		t.Fatalf("newRandomMageClient() error = %v", err)
	}
	provider.logger = zap.NewNop()
	downloader, err := newImageDownloader(srv.URL, client, normalized.MaxImageBytes, normalized.MinWidth, normalized.MinHeight, true)
	if err != nil {
		t.Fatalf("newImageDownloader() error = %v", err)
	}
	plugin := newPluginWithDeps(cfg, provider, downloader, moderator, store, zap.NewNop())
	plugin.db = db
	return plugin
}

// endlessUpstream 无限供应互不重复的合法候选与图片，附带图片请求处理。
func endlessUpstream(t *testing.T) http.HandlerFunc {
	t.Helper()
	var mu sync.Mutex
	seq := 0
	png := testImageBytes(t, "png", 800, 800)
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/i/") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
			return
		}
		mu.Lock()
		seq++
		n := seq
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ok":true,"code":"OK","data":{"image":{"illust_id":"%d","x_restrict":0,"ai_type":0,"width":800,"height":800,"title":"标题%d","user":{"name":"画师"}},"tags":["safe"],"urls":{"local":"/i/pool-%d.png"}}}`, n, n, n)
	}
}

func TestRandomBeautyCommandMatchesExactly(t *testing.T) {
	for _, tt := range []struct {
		message string
		want    bool
	}{
		{message: "/随机美图", want: true},
		{message: "  /随机美图  ", want: true},
		{message: "/随机美图xxx", want: false},
		{message: "/随机美图 风景", want: false},
		{message: "给我来一张随机美图", want: false},
	} {
		ctx := testMessageContext(tt.message)
		if got := isRandomBeautyCommand(ctx); got != tt.want {
			t.Errorf("isRandomBeautyCommand(%q) = %v, want %v", tt.message, got, tt.want)
		}
	}
}

func TestRandomBeautyConfigClamps(t *testing.T) {
	tests := []struct {
		name     string
		in       config.RandomBeautyConfig
		timeout  int
		poolSize int
		refillMo int
	}{
		{name: "defaults", in: config.RandomBeautyConfig{}, timeout: 18, poolSize: 100, refillMo: 25},
		{name: "timeout upper bound widened to 30", in: config.RandomBeautyConfig{TimeoutSeconds: 30}, timeout: 30, poolSize: 100, refillMo: 25},
		{name: "timeout beyond 30 resets", in: config.RandomBeautyConfig{TimeoutSeconds: 31}, timeout: 18, poolSize: 100, refillMo: 25},
		{name: "pool below floor resets", in: config.RandomBeautyConfig{PoolInitSize: 5}, timeout: 18, poolSize: 100, refillMo: 25},
		{name: "pool above ceiling resets", in: config.RandomBeautyConfig{PoolInitSize: 501}, timeout: 18, poolSize: 100, refillMo: 25},
		{name: "pool in range kept", in: config.RandomBeautyConfig{PoolInitSize: 250}, timeout: 18, poolSize: 250, refillMo: 25},
		{name: "refill moderation below floor resets", in: config.RandomBeautyConfig{RefillModerationTimeoutSeconds: 3}, timeout: 18, poolSize: 100, refillMo: 25},
		{name: "refill moderation above ceiling resets", in: config.RandomBeautyConfig{RefillModerationTimeoutSeconds: 31}, timeout: 18, poolSize: 100, refillMo: 25},
		{name: "refill moderation in range kept", in: config.RandomBeautyConfig{RefillModerationTimeoutSeconds: 10}, timeout: 18, poolSize: 100, refillMo: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizedConfig(tt.in)
			if got.TimeoutSeconds != tt.timeout || got.PoolInitSize != tt.poolSize || got.RefillModerationTimeoutSeconds != tt.refillMo {
				t.Fatalf("normalizedConfig() = timeout=%d pool=%d refillMo=%d, want timeout=%d pool=%d refillMo=%d",
					got.TimeoutSeconds, got.PoolInitSize, got.RefillModerationTimeoutSeconds, tt.timeout, tt.poolSize, tt.refillMo)
			}
		})
	}
}

func TestRandomBeautyPassSendsPoolHitWithAttribution(t *testing.T) {
	imageBytes := []byte("pool stored image")
	fakeStore, _ := newFakeObjectServer(t)
	fakeStore.put("stored-key", imageBytes)
	db := newTestDB(t)
	if err := db.InsertRandomBeautyImage(context.Background(), &model.RandomBeautyPool{
		ImageKey: "/i/pool-1.png", IllustID: 123, Title: "晚霞", Author: "画师",
		ObjectKey: "stored-key", Mime: "image/png", SizeBytes: int64(len(imageBytes)),
	}); err != nil {
		t.Fatal(err)
	}
	plugin := newTestPlugin(t, config.RandomBeautyConfig{}, endlessUpstream(t), fakeStore.store, db, nil)
	pass := &randomBeautyPass{plugin: plugin, cooldown: time.Minute}
	ctx := testMessageContext("/随机美图")

	if err := pass.Execute(ctx); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	segments, ok := conduit.Get[[]map[string]any](ctx, sendSegmentsKey)
	if !ok || len(segments) != 2 {
		t.Fatalf("segments = %#v", segments)
	}
	imageData := segments[0]["data"].(map[string]any)
	wantFile := "base64://" + base64.StdEncoding.EncodeToString(imageBytes)
	if segments[0]["type"] != "image" || imageData["file"] != wantFile {
		t.Fatalf("image segment = %#v", segments[0])
	}
	textData := segments[1]["data"].(map[string]any)
	wantText := "\n《晚霞》\n作者：画师\nPixiv：https://www.pixiv.net/artworks/123"
	if segments[1]["type"] != "text" || textData["text"] != wantText {
		t.Fatalf("text segment = %#v", segments[1])
	}
	// 用户路径直接命中池，不应有失败回复。
	if len(ctx.Output) != 0 {
		t.Fatalf("pool hit must not reply failure, output = %#v", ctx.Output)
	}
	if err := plugin.OnStop(nil); err != nil {
		t.Fatal(err)
	}
}

func TestRandomBeautyPassRepliesFailureWhenObjectMissing(t *testing.T) {
	fakeStore, _ := newFakeObjectServer(t)
	db := newTestDB(t)
	if err := db.InsertRandomBeautyImage(context.Background(), &model.RandomBeautyPool{
		ImageKey: "/i/pool-1.png", IllustID: 1, Title: "图", Author: "作者",
		ObjectKey: "missing-key", Mime: "image/png",
	}); err != nil {
		t.Fatal(err)
	}
	plugin := newTestPlugin(t, config.RandomBeautyConfig{}, endlessUpstream(t), fakeStore.store, db, nil)
	pass := &randomBeautyPass{plugin: plugin, cooldown: time.Minute}
	ctx := testMessageContext("/随机美图")

	if err := pass.Execute(ctx); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(ctx.Output) != 1 || ctx.Output[0].Content != messageFailure {
		t.Fatalf("output = %#v", ctx.Output)
	}
	if _, ok := conduit.Get[[]map[string]any](ctx, sendSegmentsKey); ok {
		t.Fatal("object missing must not fall back to realtime pipeline")
	}
	if err := plugin.OnStop(nil); err != nil {
		t.Fatal(err)
	}
}

func TestRandomBeautyPassDegradedWithoutStore(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	plugin := newTestPlugin(t, config.RandomBeautyConfig{}, endlessUpstream(t), nil, newTestDB(t), nil)
	plugin.logger = zap.New(core)
	pass := &randomBeautyPass{plugin: plugin, cooldown: time.Minute}

	for range 2 {
		ctx := testMessageContext("/随机美图")
		if err := pass.Execute(ctx); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if len(ctx.Output) != 1 || ctx.Output[0].Content != messageFailure {
			t.Fatalf("output = %#v", ctx.Output)
		}
	}
	entries := logs.FilterMessage("random_beauty: 对象存储未配置，图池不可用").All()
	if len(entries) != 1 {
		t.Fatalf("store warning count = %d, want 1", len(entries))
	}
	if err := plugin.OnStop(nil); err != nil {
		t.Fatal(err)
	}
}

func TestRandomBeautyPassAppliesCooldown(t *testing.T) {
	fakeStore, _ := newFakeObjectServer(t)
	fakeStore.put("stored-key", []byte("image"))
	db := newTestDB(t)
	if err := db.InsertRandomBeautyImage(context.Background(), &model.RandomBeautyPool{
		ImageKey: "/i/pool-1.png", IllustID: 1, Title: "图", Author: "作者",
		ObjectKey: "stored-key", Mime: "image/png",
	}); err != nil {
		t.Fatal(err)
	}
	plugin := newTestPlugin(t, config.RandomBeautyConfig{}, endlessUpstream(t), fakeStore.store, db, nil)
	pass := &randomBeautyPass{plugin: plugin, stateStore: newTestStateStore(), cooldown: time.Minute}

	first := testMessageContext("/随机美图")
	if err := pass.Execute(first); err != nil {
		t.Fatal(err)
	}
	if len(first.Output) != 0 {
		t.Fatalf("first output = %#v", first.Output)
	}
	second := testMessageContext("/随机美图")
	if err := pass.Execute(second); err != nil {
		t.Fatal(err)
	}
	if len(second.Output) != 1 || second.Output[0].Content != messageRateLimited {
		t.Fatalf("second output = %#v", second.Output)
	}
	if err := plugin.OnStop(nil); err != nil {
		t.Fatal(err)
	}
}

func TestRandomBeautyOnStopIsIdempotent(t *testing.T) {
	plugin := newTestPlugin(t, config.RandomBeautyConfig{}, endlessUpstream(t), nil, newTestDB(t), nil)
	if err := plugin.OnStop(nil); err != nil {
		t.Fatalf("first OnStop() error = %v", err)
	}
	if err := plugin.OnStop(nil); err != nil {
		t.Fatalf("second OnStop() error = %v", err)
	}
}

func TestRandomBeautyPluginRegistersTrackedResources(t *testing.T) {
	store := newTestStateStore()
	engine := conduit.New(store)
	registry := pluginpkg.NewRegistry(engine, store, nil, command.New(), nil, zap.NewNop())
	plugin := newTestPlugin(t, config.RandomBeautyConfig{}, endlessUpstream(t), nil, nil, nil)
	if err := registry.Register(plugin); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := registry.InitPlugins(context.Background()); err != nil {
		t.Fatalf("InitPlugins() error = %v", err)
	}
	if _, ok := engine.GetPass(pluginpkg.PassID(pluginID, "fetch")); !ok {
		t.Fatal("plugin pass was not registered")
	}
	if _, ok := engine.GetPipeline(pluginpkg.PipelineID(pluginID, "main")); !ok {
		t.Fatal("plugin pipeline was not registered")
	}
	if _, ok := engine.GetSubtree(pluginpkg.SubtreeID(pluginID)); !ok {
		t.Fatal("plugin subtree was not registered")
	}
	if err := registry.Unregister(pluginID); err != nil {
		t.Fatalf("Unregister() error = %v", err)
	}
	if _, ok := engine.GetPass(pluginpkg.PassID(pluginID, "fetch")); ok {
		t.Fatal("tracked pass leaked after unregister")
	}
}

func testMessageContext(content string) *conduit.MessageContext {
	return conduit.NewMessageContext(&conduit.InputMessage{
		UserID:  "u1",
		GroupID: "g1",
		IsGroup: true,
		Content: content,
		Extra: map[string]any{
			"platform": "qq",
		},
	})
}

type testStateStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newTestStateStore() *testStateStore { return &testStateStore{data: make(map[string]string)} }

func (s *testStateStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key], nil
}
func (s *testStateStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}
func (s *testStateStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}
func (s *testStateStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[key]
	return ok, nil
}
func (s *testStateStore) CompareAndSwap(_ context.Context, key, oldValue, newValue string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[key] != oldValue {
		return false, nil
	}
	s.data[key] = newValue
	return true, nil
}
func (s *testStateStore) IncrBy(_ context.Context, key string, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var value int64
	_, _ = fmt.Sscan(s.data[key], &value)
	value += delta
	s.data[key] = fmt.Sprint(value)
	return value, nil
}
func (s *testStateStore) SetIfNotExists(_ context.Context, key, value string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; ok {
		return false, nil
	}
	s.data[key] = value
	return true, nil
}
func (s *testStateStore) Close() error { return nil }
// fakeObjectServer 是 S3 兼容假服务器（path-style），驱动真实 media.ObjectStore。
type fakeObjectServer struct {
	store   *media.ObjectStore
	server  *httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
	deleted []string
	puts    int
}

func newFakeObjectServer(t *testing.T) (*fakeObjectServer, *media.ObjectStore) {
	t.Helper()
	f := &fakeObjectServer{objects: make(map[string][]byte)}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		if key == "" || key == r.URL.Path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") ||
				strings.HasPrefix(r.Header.Get("x-amz-content-sha256"), "STREAMING-") {
				body = decodeAWSChunked(body)
			}
			f.mu.Lock()
			f.objects[key] = body
			f.puts++
			f.mu.Unlock()
			w.Header().Set("ETag", `"fake-etag"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			f.mu.Lock()
			data, ok := f.objects[key]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(data)
		case http.MethodHead:
			f.mu.Lock()
			_, ok := f.objects[key]
			f.mu.Unlock()
			if ok {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodDelete:
			f.mu.Lock()
			delete(f.objects, key)
			f.deleted = append(f.deleted, key)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	store, err := media.NewObjectStore(f.server.URL, "test-access", "test-secret", "bucket", "us-east-1")
	if err != nil {
		t.Fatalf("NewObjectStore() error = %v", err)
	}
	f.store = store
	return f, store
}

func (f *fakeObjectServer) put(key string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = data
}

func (f *fakeObjectServer) putsN() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts
}

func (f *fakeObjectServer) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

func (f *fakeObjectServer) deletedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// decodeAWSChunked 解码 AWS SDK trailing checksum 模式的 aws-chunked 传输编码。
func decodeAWSChunked(body []byte) []byte {
	var out []byte
	for len(body) > 0 {
		idx := bytes.IndexByte(body, '\n')
		if idx < 0 {
			return body // 结构不完整，原样返回便于排查
		}
		sizePart := string(body[:idx])
		if i := strings.IndexByte(sizePart, ';'); i >= 0 {
			sizePart = sizePart[:i]
		}
		size, err := strconv.ParseInt(strings.TrimSpace(sizePart), 16, 64)
		if err != nil {
			return body // 非 aws-chunked，原样返回
		}
		body = body[idx+1:]
		if size == 0 || size > int64(len(body)) {
			break
		}
		out = append(out, body[:size]...)
		if int(size)+2 > len(body) {
			body = nil
		} else {
			body = body[size+2:]
		}
	}
	return out
}
