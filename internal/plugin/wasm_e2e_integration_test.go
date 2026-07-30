package plugin

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/infra"
	"github.com/zrurf/conduit"
	"go.uber.org/zap"
)

// TestSigninWasm_EndToEnd 覆盖示例 Wasm 的构建产物经数据库安装、加载和启动后，
// 通过真实 Extism、PostgreSQL、Redis 与 Conduit 路由签到命令的完整链路。
// 消息回复边界使用闭包收集，以避免依赖具体 IM 平台。
func TestSigninWasm_EndToEnd(t *testing.T) {
	if os.Getenv("LANMEI_E2E") != "1" {
		t.Skip("set LANMEI_E2E=1 to run Docker-backed Wasm E2E test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wasmPath := os.Getenv("LANMEI_SIGNIN_WASM")
	if wasmPath == "" {
		wasmPath = filepath.Join(t.TempDir(), "signin.wasm")
		build := exec.CommandContext(ctx, "tinygo", "build", "-o", wasmPath, "-target=wasi", ".")
		build.Dir = filepath.Join("..", "..", "examples", "plugin", "signin")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build signin Wasm: %v\n%s", err, output)
		}
	}
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read built signin wasm %q: %v", wasmPath, err)
	}
	inf, err := infra.Setup(ctx, &config.DatabaseConfig{
		URL: envOr("LANMEI_DATABASE_URL", "postgres://lanmei:lanmei@localhost:55432/lanmei?sslmode=disable"),
	}, &config.RedisConfig{
		Addr: envOr("LANMEI_REDIS_ADDR", "localhost:56379"),
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("setup Docker integration infrastructure: %v", err)
	}
	defer inf.Close()
	if err := inf.Redis.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("clear isolated Redis database: %v", err)
	}

	const ownerID int64 = 900001
	actor := UserPrincipal("qq", strconv.FormatInt(ownerID, 10))
	authorizer, err := NewService(inf.DB.Orm)
	if err != nil {
		t.Fatalf("create authorizer: %v", err)
	}
	if err := authorizer.InitBuiltinPolicies([]string{actor}); err != nil {
		t.Fatalf("initialize authorization policies: %v", err)
	}

	engine := conduit.New(inf.StateStore, conduit.WithTimeout(10*time.Second))
	cmdSys := command.New()
	registry := NewRegistry(engine, inf.StateStore, inf.DB, cmdSys, nil, zap.NewNop())
	manager, err := NewWasmManager(&config.PluginConfig{RootDir: t.TempDir()}, inf.DB, registry, authorizer, zap.NewNop(), nil)
	if err != nil {
		t.Fatalf("create Wasm manager: %v", err)
	}
	manager.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader(wasm)),
			ContentLength: int64(len(wasm)),
			Header:        make(http.Header),
			Request:       request,
		}, nil
	})}

	installation, err := manager.Install(ctx, actor, "https://plugins.example/signin.wasm")
	if err != nil {
		t.Fatalf("install signin Wasm: %v", err)
	}
	defer func() {
		if err := manager.Unload(context.Background(), actor, installation.ID); err != nil {
			t.Errorf("unload signin Wasm: %v", err)
		}
		if err := manager.Delete(context.Background(), actor, installation.ID); err != nil {
			t.Errorf("delete signin installation: %v", err)
		}
	}()

	persisted, err := manager.store.FindByID(ctx, installation.ID)
	if err != nil {
		t.Fatalf("read installed record from PostgreSQL: %v", err)
	}
	if persisted.PluginID != "signin" || persisted.Enabled {
		t.Fatalf("installed record = plugin_id=%q enabled=%t, want signin and disabled", persisted.PluginID, persisted.Enabled)
	}
	if err := authorizer.BindRole(actor, PluginPrincipal(installation.PluginID, installation.ID), RolePluginCommandBasic); err != nil {
		t.Fatalf("grant signin runtime role: %v", err)
	}
	if err := manager.Load(ctx, actor, installation.ID); err != nil {
		t.Fatalf("load installed signin Wasm: %v", err)
	}
	if err := manager.Start(ctx, actor, installation.ID); err != nil {
		t.Fatalf("start loaded signin Wasm: %v", err)
	}

	persisted, err = manager.store.FindByID(ctx, installation.ID)
	if err != nil {
		t.Fatalf("read started record from PostgreSQL: %v", err)
	}
	if !persisted.Enabled {
		t.Fatal("started installation is not enabled in PostgreSQL")
	}

	branches := make([]conduit.BTNode, 0, len(registry.SubtreeRefs())+1)
	for _, ref := range registry.SubtreeRefs() {
		branches = append(branches, ref)
	}
	branches = append(branches, conduit.NewAction("pipeline.fallback"))
	engine.SetBehaviorTree(conduit.NewBehaviorTree(branches...))
	engine.Start()
	defer engine.Stop()

	replies := make([]string, 0, 2)
	commandContext := &command.Context{
		Platform:       "qq",
		PlatformUserID: "10001",
		GroupID:        "e2e-group",
		IsGroup:        true,
		Reply: func(message string) {
			replies = append(replies, message)
		},
	}
	if err := cmdSys.Process("/签到", commandContext); err != nil {
		t.Fatalf("process first sign-in command: %v", err)
	}
	if len(replies) != 1 || replies[0] != "签到成功！\n本次积分: +10" {
		t.Fatalf("first sign-in reply = %#v", replies)
	}
	if err := cmdSys.Process("/签到", commandContext); err != nil {
		t.Fatalf("process repeated sign-in command: %v", err)
	}
	if len(replies) != 2 || replies[1] != "今日已签到，明天再来吧！" {
		t.Fatalf("repeated sign-in reply = %#v", replies)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
