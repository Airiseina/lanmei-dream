package plugin

import (
	"context"
	"sync"
	"testing"
	"time"

	extism "github.com/extism/go-sdk"

	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/zrurf/conduit"
)

// fakeWasmRuntime 实现 WasmRuntime 接口，用于 Conduit 集成测试。
type fakeWasmRuntime struct {
	handleResp HandleResponse
	handleCnt  int
	muHandle   sync.Mutex
}

func (r *fakeWasmRuntime) CallHandle(_ context.Context, _ *extism.Plugin, _ *sync.Mutex, _ HandleRequest) (*HandleResponse, error) {
	r.muHandle.Lock()
	r.handleCnt++
	r.muHandle.Unlock()
	return &r.handleResp, nil
}

func (r *fakeWasmRuntime) CallStart(_ context.Context, _ *extism.Plugin, _ *sync.Mutex) error {
	return nil
}

func (r *fakeWasmRuntime) CallStop(_ context.Context, _ *extism.Plugin, _ *sync.Mutex, _ StopReason) {
}

func (r *fakeWasmRuntime) Close(_ context.Context, _ *extism.Plugin, _ *sync.Mutex) error { return nil }

// 用真实 Conduit 引擎验证 command.System → Conduit → WasmCommandPass → 回复输出链路。
func TestWasmCommandPass_ConduitIntegration(t *testing.T) {
	store := newMemStateStore()
	engine := conduit.New(store, conduit.WithTimeout(10*time.Second))
	cmdSys := command.New()
	if err := cmdSys.Register(command.Command{
		Name:        "帮助",
		Description: "help",
		Handler:     cmdSys.HelpHandler,
	}); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(engine, store, nil, cmdSys)

	pluginInfo := PluginInfoResponse{
		ABIVersion:  ABIVersion,
		ID:          "fakecmd",
		Name:        "Fake",
		Description: "测试命令插件",
		Version:     "1.0.0",
		Commands:    []CommandDecl{{Name: "fakecmd", Description: "测试"}},
	}

	fakeRt := &fakeWasmRuntime{handleResp: HandleResponse{
		Handled: true,
		Outputs: []OutputItem{{Type: "text", Content: "签到成功！"}},
	}}

	auth := &mockAuthorizer{allowed: true}

	wasmPl := NewWasmPlugin(pluginInfo, "test-install-001", fakeRt, nil, auth)
	if err := reg.Register(wasmPl); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitPlugin(context.Background(), "fakecmd"); err != nil {
		t.Fatal(err)
	}

	// 构建行为树：插件子树 + 兜底动作
	branches := []conduit.BTNode{}
	for _, ref := range reg.SubtreeRefs() {
		branches = append(branches, ref)
	}
	branches = append(branches, conduit.NewAction("pipeline.fallback"))
	engine.SetBehaviorTree(conduit.NewBehaviorTree(branches...))

	engine.Start()
	defer engine.Stop()

	var replies []string
	cmdCtx := &command.Context{
		Platform:       "qq",
		PlatformUserID: "10001",
		GroupID:        "g1",
		IsGroup:        true,
		Reply:          func(s string) { replies = append(replies, s) },
	}
	if err := cmdSys.Process("/fakecmd", cmdCtx); err != nil {
		t.Fatalf("Process error: %v", err)
	}

	if len(replies) == 0 {
		t.Fatal("expected reply output")
	}
	if replies[0] != "签到成功！" {
		t.Errorf("reply = %q, want %q", replies[0], "签到成功！")
	}
	if fakeRt.handleCnt != 1 {
		t.Errorf("handle called %d times, want 1", fakeRt.handleCnt)
	}
}

// mockAuthorizer 用于测试，直接返回固定鉴权结果。
type mockAuthorizer struct {
	allowed bool
}

func (m *mockAuthorizer) Require(_, _ string) error {
	if !m.allowed {
		return ErrPermissionDenied
	}
	return nil
}
func (m *mockAuthorizer) BindRole(_, _, _ string) error     { return nil }
func (m *mockAuthorizer) UnbindRole(_, _, _ string) error   { return nil }
func (m *mockAuthorizer) GrantAction(_, _, _ string) error  { return nil }
func (m *mockAuthorizer) RevokeAction(_, _, _ string) error { return nil }
func (m *mockAuthorizer) RolesFor(_ string) ([]string, error) {
	return []string{RolePluginCommandBasic}, nil
}

func (m *mockAuthorizer) ActionsFor(_ string) ([][]string, error) {
	return [][]string{{RolePluginCommandBasic, ActionCommandHandle}}, nil
}
func (m *mockAuthorizer) ListActions() []string                { return allActions() }
func (m *mockAuthorizer) ListRoles() []string                  { return []string{RolePluginCommandBasic} }
func (m *mockAuthorizer) InitBuiltinPolicies(_ []string) error { return nil }
func (m *mockAuthorizer) IsKnownRole(role string) bool         { return role == RolePluginCommandBasic }
func (m *mockAuthorizer) IsKnownAction(_ string) bool          { return true }

var _ Authorizer = (*mockAuthorizer)(nil)
