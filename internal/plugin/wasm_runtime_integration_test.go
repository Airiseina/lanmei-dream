package plugin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// 真实 Extism runtime 冒烟测试：用 SDK 自带 count_vowels.wasm 验证 CallWithContext 链路可用。
func TestExtismRuntime_SmokeTest(t *testing.T) {
	wasmPath := filepath.Join(
		"/home/hpxx/go/pkg/mod/github.com/extism/go-sdk@v1.7.1",
		"wasm", "count_vowels.wasm",
	)

	hash, err := hashFile(wasmPath)
	if err != nil {
		t.Skipf("count_vowels.wasm not available: %v", err)
	}

	rt := NewRuntime(&DefaultLimits, zap.NewNop())
	ctx := context.Background()

	plugin, err := rt.CreateCheckInstance(ctx, wasmPath, hash)
	if err != nil {
		t.Skipf("cannot create extism instance (no wasm runtime): %v", err)
	}
	defer func() {
		mu := &sync.Mutex{}
		_ = rt.Close(context.Background(), plugin, mu)
	}()

	if !plugin.FunctionExists("count_vowels") {
		t.Fatal("count_vowels export not found")
	}

	exit, output, err := plugin.CallWithContext(ctx, "count_vowels", []byte("Hello World"))
	if err != nil {
		t.Fatalf("CallWithContext failed: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit code = %d", exit)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("unmarshal output: %v (raw: %s)", err, string(output))
	}
	if count, ok := result["count"].(float64); !ok || int(count) != 3 {
		t.Errorf("vowel count = %v, want 3", result["count"])
	}
}
