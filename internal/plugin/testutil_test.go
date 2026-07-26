package plugin

import (
	"context"
	"sync"
	"time"

	"github.com/zrurf/conduit"
)

// memStateStore 是 conduit.StateStore 的内存实现，仅用于单元测试。
type memStateStore struct {
	mu   sync.RWMutex
	data map[string]string
}

func newMemStateStore() *memStateStore {
	return &memStateStore{data: make(map[string]string)}
}

func (s *memStateStore) Get(_ context.Context, key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[key], nil
}

func (s *memStateStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *memStateStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *memStateStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[key]
	return ok, nil
}

func (s *memStateStore) Close() error { return nil }

var _ conduit.StateStore = (*memStateStore)(nil)
