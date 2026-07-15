// Package recordstore provides synchronized, clone-on-boundary storage for
// test-owned records without depending on a production domain package.
package recordstore

import "sync"

type Store[T any] struct {
	mu      sync.RWMutex
	records map[string]T
	clone   func(T) T
}

func New[T any](clone func(T) T) *Store[T] {
	return NewWithRecords(make(map[string]T), clone)
}

func NewWithRecords[T any](records map[string]T, clone func(T) T) *Store[T] {
	if records == nil {
		records = make(map[string]T)
	}
	if clone == nil {
		clone = func(value T) T { return value }
	}
	return &Store[T]{records: records, clone: clone}
}

func (s *Store[T]) Put(key string, value T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[key] = s.clone(value)
}

func (s *Store[T]) Get(key string) (T, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.records[key]
	if !ok {
		var zero T
		return zero, false
	}
	return s.clone(value), true
}
