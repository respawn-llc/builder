package runtime

import "sync"

type compactionRuntimeState struct {
	mu                 sync.Mutex
	count              int
	soonReminderIssued bool
}

func newCompactionRuntimeState() *compactionRuntimeState {
	return &compactionRuntimeState{}
}

func (s *compactionRuntimeState) Count() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func (s *compactionRuntimeState) IncrementCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	return s.count
}

func (s *compactionRuntimeState) SetCount(count int) {
	if s == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	s.mu.Lock()
	s.count = count
	s.mu.Unlock()
}

func (s *compactionRuntimeState) SoonReminderIssued() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.soonReminderIssued
}

func (s *compactionRuntimeState) SetSoonReminderIssued(issued bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.soonReminderIssued = issued
	s.mu.Unlock()
}
