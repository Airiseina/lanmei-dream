package plugin

import (
	"context"
	"fmt"
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

func (s *memStateStore) CompareAndSwap(_ context.Context, key, oldValue, newValue string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.data[key]
	if !ok {
		if oldValue == "" {
			s.data[key] = newValue
			return true, nil
		}
		return false, nil
	}
	if current == oldValue {
		s.data[key] = newValue
		return true, nil
	}
	return false, nil
}

func (s *memStateStore) IncrBy(_ context.Context, key string, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var current int64
	if val, ok := s.data[key]; ok {
		fmt.Sscanf(val, "%d", &current)
	}
	current += delta
	s.data[key] = fmt.Sprintf("%d", current)
	return current, nil
}

func (s *memStateStore) SetIfNotExists(_ context.Context, key, value string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; ok {
		return false, nil
	}
	s.data[key] = value
	return true, nil
}

func (s *memStateStore) Close() error { return nil }

var _ conduit.StateStore = (*memStateStore)(nil)
