package runtime

import "sync"

type pendingToolCallStartStore struct {
	mu     sync.Mutex
	starts map[string]int
}

func newPendingToolCallStartStore() *pendingToolCallStartStore {
	return &pendingToolCallStartStore{starts: make(map[string]int)}
}

func (s *pendingToolCallStartStore) Remember(starts map[string]int) {
	if s == nil || len(starts) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starts == nil {
		s.starts = make(map[string]int, len(starts))
	}
	for callID, start := range starts {
		if callID == "" {
			continue
		}
		s.starts[callID] = start
	}
}

func (s *pendingToolCallStartStore) Lookup(callID string) (int, bool) {
	if s == nil || callID == "" {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	start, ok := s.starts[callID]
	return start, ok
}

func (s *pendingToolCallStartStore) Forget(callID string) {
	if s == nil || callID == "" {
		return
	}
	s.mu.Lock()
	delete(s.starts, callID)
	s.mu.Unlock()
}

func (s *pendingToolCallStartStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.starts)
}
