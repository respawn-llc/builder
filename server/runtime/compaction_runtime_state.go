package runtime

import "sync"

type compactionRuntimeState struct {
	mu                 sync.Mutex
	count              int
	soonReminderIssued bool
	manualEligible     bool
	compactionActive   bool
	manualBoundary     *manualBoundaryCoordinator
}

func newCompactionRuntimeState() *compactionRuntimeState {
	return &compactionRuntimeState{manualBoundary: newManualBoundaryCoordinator()}
}

func (s *compactionRuntimeState) manualBoundaryCoordinator() *manualBoundaryCoordinator {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.manualBoundary == nil {
		s.manualBoundary = newManualBoundaryCoordinator()
	}
	return s.manualBoundary
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

func (s *compactionRuntimeState) ManualCompactionEligible() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manualEligible
}

func (s *compactionRuntimeState) SetManualCompactionEligible(eligible bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.manualEligible = eligible
	s.mu.Unlock()
}
