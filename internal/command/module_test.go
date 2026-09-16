package command

import (
	"sync"
	"testing"
)

// TestSystemConcurrentRegister 验证并发注册同名命令时仅一次成功，其余均返回重复错误。
func TestSystemConcurrentRegister(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error

	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Register(Command{
				Name:    "cmd",
				Handler: func(ctx *Context) error { return nil },
			}); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// 只应有一次成功注册
	if len(failures) < 99 {
		t.Errorf("expected 99 duplicate failures, got %d", len(failures))
	}
}

// TestSystemConcurrentProcess 验证多 goroutine 并发分发同一命令时的读写安全。
func TestSystemConcurrentProcess(t *testing.T) {
	s := New()
	if err := s.Register(Command{
		Name:        "ping",
		Description: "ping",
		Handler:     func(ctx *Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			ctx := &Context{Platform: "qq", PlatformUserID: "1", Reply: func(string) {}}
			if err := s.Process("/ping", ctx); err != nil {
				t.Errorf("process error: %v", err)
			}
		})
	}
	wg.Wait()
}

// TestSystemDuplicateRegister 验证重复注册同名命令返回错误且不覆盖已有命令。
func TestSystemDuplicateRegister(t *testing.T) {
	s := New()
	if err := s.Register(Command{Name: "x", Handler: func(ctx *Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(Command{Name: "x", Handler: func(ctx *Context) error { return nil }}); err == nil {
		t.Fatal("duplicate register should fail")
	}
}

// TestSystemUnregisterIdempotent 验证注销不存在的命令不 panic（幂等语义）。
func TestSystemUnregisterIdempotent(t *testing.T) {
	s := New()
	s.Unregister("nonexistent") // 不应 panic
}

// TestSystemProcessArgs 验证带参数命令经 Process 后 Message 归一化为 "/命令 参数"。
func TestSystemProcessArgs(t *testing.T) {
	s := New()
	captured := ""
	if err := s.Register(Command{
		Name: "echo",
		Handler: func(ctx *Context) error {
			captured = ctx.Message
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Process("/echo hello world", &Context{Reply: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if captured != "/echo hello world" {
		t.Errorf("Message = %q, want %q", captured, "/echo hello world")
	}
}

// TestSystemProcessNoArgs 验证无参数命令经 Process 后 Message 为 "/命令"。
func TestSystemProcessNoArgs(t *testing.T) {
	s := New()
	captured := ""
	if err := s.Register(Command{
		Name: "ping",
		Handler: func(ctx *Context) error {
			captured = ctx.Message
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Process("/ping", &Context{Reply: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if captured != "/ping" {
		t.Errorf("Message = %q, want %q", captured, "/ping")
	}
}

// TestSystemProcessUnknownCommand 验证未知命令返回错误并经 Reply 回复提示。
func TestSystemProcessUnknownCommand(t *testing.T) {
	s := New()
	replied := ""
	ctx := &Context{Reply: func(s string) { replied = s }}
	if err := s.Process("/nonexistent", ctx); err == nil {
		t.Fatal("expected error")
	}
	if replied == "" {
		t.Error("expected reply for unknown command")
	}
}

// TestSystemListSorted 验证 List 返回的命令按命令名升序排列。
func TestSystemListSorted(t *testing.T) {
	s := New()
	for _, name := range []string{"zebra", "alpha", "mango"} {
		if err := s.Register(Command{Name: name, Handler: func(ctx *Context) error { return nil }}); err != nil {
			t.Fatal(err)
		}
	}
	cmds := s.List()
	if len(cmds) != 3 || cmds[0].Name != "alpha" || cmds[1].Name != "mango" || cmds[2].Name != "zebra" {
		t.Fatalf("List not sorted: %+v", cmds)
	}
}
