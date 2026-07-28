package command

import (
	"sync"
	"testing"
)

// ── 并发安全 ──

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

// ── 重复注册不被覆盖 ──

func TestSystemDuplicateRegister(t *testing.T) {
	s := New()
	if err := s.Register(Command{Name: "x", Handler: func(ctx *Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(Command{Name: "x", Handler: func(ctx *Context) error { return nil }}); err == nil {
		t.Fatal("duplicate register should fail")
	}
}

func TestSystemUnregisterIdempotent(t *testing.T) {
	s := New()
	s.Unregister("nonexistent") // should not panic
}

// ── Process 参数传递 ──

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
