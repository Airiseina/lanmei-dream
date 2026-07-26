package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

type fakeWasmInstaller struct {
	actor     string
	sourceURL string
	result    *model.PluginInstallation
	err       error
	calls     int
}

func (installer *fakeWasmInstaller) Install(_ context.Context, actor, sourceURL string) (*model.PluginInstallation, error) {
	installer.actor = actor
	installer.sourceURL = sourceURL
	installer.calls++
	return installer.result, installer.err
}

func TestWasmInstallCommandRequiresDirectURL(t *testing.T) {
	installer := &fakeWasmInstaller{}
	cmd := NewWasmInstallCommand(context.Background(), installer)
	var reply string

	if err := cmd.Handler(&command.Context{
		UserID:  10001,
		Message: "/插件 安装",
		Reply:   func(message string) { reply = message },
	}); err != nil {
		t.Fatalf("handle usage: %v", err)
	}
	if installer.calls != 0 {
		t.Fatalf("Install called %d times without URL", installer.calls)
	}
	if !strings.Contains(reply, "HTTPS Wasm 直链") || !strings.Contains(reply, "GitHub Release") {
		t.Fatalf("usage reply = %q", reply)
	}
}

func TestWasmInstallCommandInstallsRemoteBinary(t *testing.T) {
	installer := &fakeWasmInstaller{result: &model.PluginInstallation{
		ID:      "installation-001",
		Name:    "签到",
		Version: "1.0.0",
	}}
	cmd := NewWasmInstallCommand(context.Background(), installer)
	var reply string

	if err := cmd.Handler(&command.Context{
		UserID:  10001,
		Message: "/插件 安装 https://github.com/example/project/releases/download/v1/signin.wasm",
		Reply:   func(message string) { reply = message },
	}); err != nil {
		t.Fatalf("install remote Wasm: %v", err)
	}
	if installer.calls != 1 {
		t.Fatalf("Install called %d times, want 1", installer.calls)
	}
	if installer.actor != UserPrincipal(10001) {
		t.Fatalf("actor = %q", installer.actor)
	}
	if installer.sourceURL != "https://github.com/example/project/releases/download/v1/signin.wasm" {
		t.Fatalf("source URL = %q", installer.sourceURL)
	}
	if !strings.Contains(reply, "installation-001") || !strings.Contains(reply, "尚未加载") {
		t.Fatalf("success reply = %q", reply)
	}
}

func TestWasmInstallCommandReportsInstallFailure(t *testing.T) {
	installer := &fakeWasmInstaller{err: errors.New("permission denied")}
	cmd := NewWasmInstallCommand(context.Background(), installer)
	var reply string

	if err := cmd.Handler(&command.Context{
		UserID:  10002,
		Message: "/插件 安装 https://plugins.example/plugin.wasm",
		Reply:   func(message string) { reply = message },
	}); err != nil {
		t.Fatalf("handle failed installation: %v", err)
	}
	if !strings.Contains(reply, "安装失败") || !strings.Contains(reply, "permission denied") {
		t.Fatalf("failure reply = %q", reply)
	}
}
