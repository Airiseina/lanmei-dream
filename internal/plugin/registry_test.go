package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/zrurf/conduit"
)

// fakePlugin 用于测试 Registry 生命周期。
type fakePlugin struct {
	info     PluginInfo
	initErr  error
	initCnt  int
	startErr error
	startCnt int
	stopErr  error
	stopCnt  int
	mu       sync.Mutex
}

func (p *fakePlugin) Info() PluginInfo { return p.info }
func (p *fakePlugin) OnInit(_ *PluginContext) error {
	p.mu.Lock()
	p.initCnt++
	p.mu.Unlock()
	return p.initErr
}

func (p *fakePlugin) OnStart(_ *PluginContext) error {
	p.mu.Lock()
	p.startCnt++
	p.mu.Unlock()
	return p.startErr
}

func (p *fakePlugin) OnStop(_ *PluginContext) error {
	p.mu.Lock()
	p.stopCnt++
	p.mu.Unlock()
	return p.stopErr
}

// noopPass 满足 conduit.Pass 接口。
type noopPass struct{}

func (noopPass) Execute(_ *conduit.MessageContext) error { return nil }

var _ conduit.Pass = noopPass{}

// ── 定向生命周期 ──

func TestRegistryInitPluginSingle(t *testing.T) {
	store := newMemStateStore()
	cmdSys := command.New()
	engine := conduit.New(store)
	reg := NewRegistry(engine, store, nil, cmdSys)

	fp := &fakePlugin{info: PluginInfo{ID: "fp", Name: "Fake", Version: "1.0"}}
	if err := reg.Register(fp); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitPlugin(context.Background(), "fp"); err != nil {
		t.Fatalf("InitPlugin: %v", err)
	}
	if fp.initCnt != 1 {
		t.Errorf("initCnt = %d, want 1", fp.initCnt)
	}
	st, ok := reg.State("fp")
	if !ok || st != stateInitialized {
		t.Fatalf("state = %v, ok=%v", st, ok)
	}
}

func TestRegistryInitPluginNotFound(t *testing.T) {
	reg := NewRegistry(nil, newMemStateStore(), nil, command.New())
	if err := reg.InitPlugin(context.Background(), "missing"); err == nil {
		t.Fatal("expected not found error")
	}
}

func TestRegistryInitPluginOnInitErrorRollsBack(t *testing.T) {
	store := newMemStateStore()
	cmdSys := command.New()
	engine := conduit.New(store)
	reg := NewRegistry(engine, store, nil, cmdSys)

	fp := &fakePlugin{
		info:    PluginInfo{ID: "fail", Name: "F", Version: "1.0", Commands: []CommandDef{{Name: "fail", Description: "x"}}},
		initErr: errors.New("init boom"),
	}
	_ = reg.Register(fp)

	if err := reg.InitPlugin(context.Background(), "fail"); err == nil {
		t.Fatal("expected init error")
	}
	// 状态应仍为 stateRegistered（回滚后）
	st, _ := reg.State("fail")
	if st != stateRegistered {
		t.Errorf("state after failed init = %v, want %v", st, stateRegistered)
	}
}

func TestRegistryStartStopPluginSingle(t *testing.T) {
	store := newMemStateStore()
	cmdSys := command.New()
	engine := conduit.New(store)
	reg := NewRegistry(engine, store, nil, cmdSys)

	fp := &fakePlugin{info: PluginInfo{ID: "ss", Name: "SS", Version: "1.0"}}
	_ = reg.Register(fp)
	_ = reg.InitPlugin(context.Background(), "ss")
	_ = reg.StartPlugin(context.Background(), "ss")

	if st, _ := reg.State("ss"); st != stateStarted {
		t.Fatalf("state = %v, want started", st)
	}
	_ = reg.StopPlugin(context.Background(), "ss")
	if st, _ := reg.State("ss"); st != stateStopped {
		t.Fatalf("state = %v, want stopped", st)
	}
}

func TestRegistryStartPluginNotInitializedFails(t *testing.T) {
	reg := NewRegistry(nil, newMemStateStore(), nil, command.New())
	fp := &fakePlugin{info: PluginInfo{ID: "n", Name: "N", Version: "1"}}
	_ = reg.Register(fp)
	if err := reg.StartPlugin(context.Background(), "n"); err == nil {
		t.Fatal("expected not-initialized error")
	}
}

func TestRegistryStopPluginNotStartedNoOps(t *testing.T) {
	reg := NewRegistry(nil, newMemStateStore(), nil, command.New())
	fp := &fakePlugin{info: PluginInfo{ID: "n2", Name: "N2", Version: "1"}}
	_ = reg.Register(fp)
	_ = reg.InitPlugin(context.Background(), "n2")
	if err := reg.StopPlugin(context.Background(), "n2"); err != nil {
		t.Fatalf("StopPlugin on initialized but not started should no-op: %v", err)
	}
}

// ── 批量复用定向 ──

func TestRegistryBatchCallsUsesSingle(t *testing.T) {
	store := newMemStateStore()
	engine := conduit.New(store)
	cmdSys := command.New()
	reg := NewRegistry(engine, store, nil, cmdSys)

	for i := range 3 {
		_ = reg.Register(&fakePlugin{info: PluginInfo{
			ID: fmt.Sprintf("b%d", i), Name: "B", Version: "1.0",
		}})
	}
	if err := reg.InitPlugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartPlugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg.StopPlugins(context.Background())

	for i := range 3 {
		st, _ := reg.State(fmt.Sprintf("b%d", i))
		if st != stateStopped {
			t.Errorf("b%d state = %v, want stopped", i, st)
		}
	}
}

// ── 命令注册到并发安全 System ──

func TestRegistryInitPluginRegistersCommand(t *testing.T) {
	store := newMemStateStore()
	engine := conduit.New(store)
	cmdSys := command.New()
	reg := NewRegistry(engine, store, nil, cmdSys)

	fp := &fakePlugin{info: PluginInfo{
		ID: "cmdp", Name: "C", Version: "1.0",
		Commands: []CommandDef{{Name: "cmdp_test", Description: "test"}},
	}}
	_ = reg.Register(fp)
	_ = reg.InitPlugin(context.Background(), "cmdp")

	cmds := cmdSys.List()
	found := false
	for _, c := range cmds {
		if c.Name == "cmdp_test" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("command not registered")
	}
}
