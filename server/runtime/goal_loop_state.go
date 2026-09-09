package runtime

import (
	"context"
	"sync"
)

type goalLoopState struct {
	mu               sync.Mutex
	lifecycle        goalLoopLifecycleState
	interruptPending bool
	changed          chan struct{}
}

func newGoalLoopState() *goalLoopState {
	return &goalLoopState{changed: make(chan struct{})}
}

func (s *goalLoopState) Suspend() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.interruptPending = false
	switch s.lifecycle {
	case goalLoopLifecycleRunning, goalLoopLifecycleRestartPending:
		s.lifecycle = goalLoopLifecycleSuspending
	default:
		s.lifecycle = goalLoopLifecycleSuspended
	}
	s.mu.Unlock()
}

func (s *goalLoopState) Resume() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.interruptPending = false
	if s.lifecycle.IsSuspended() {
		s.lifecycle = goalLoopLifecycleIdle
		close(s.changed)
		s.changed = make(chan struct{})
	}
	s.mu.Unlock()
}

func (s *goalLoopState) Start() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruptPending {
		s.interruptPending = false
		switch s.lifecycle {
		case goalLoopLifecycleRunning, goalLoopLifecycleSuspending, goalLoopLifecycleRestartPending:
			s.lifecycle = goalLoopLifecycleRestartPending
			return false
		}
	}
	switch s.lifecycle {
	case goalLoopLifecycleRunning, goalLoopLifecycleRestartPending:
		return false
	case goalLoopLifecycleSuspending:
		s.lifecycle = goalLoopLifecycleRestartPending
		return false
	}
	s.lifecycle = goalLoopLifecycleRunning
	return true
}

func (s *goalLoopState) Finish(active bool) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interruptPending = false
	switch s.lifecycle {
	case goalLoopLifecycleRestartPending:
		if active {
			s.lifecycle = goalLoopLifecycleRunning
			return true
		}
		s.lifecycle = goalLoopLifecycleIdle
	case goalLoopLifecycleSuspending:
		s.lifecycle = goalLoopLifecycleSuspended
	case goalLoopLifecycleRunning:
		s.lifecycle = goalLoopLifecycleIdle
	}
	close(s.changed)
	s.changed = make(chan struct{})
	return false
}

func (s *goalLoopState) Suspended() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifecycle.IsSuspended()
}

func (s *goalLoopState) Running() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifecycle.IsRunning()
}

func (s *goalLoopState) Wait(ctx context.Context) error {
	s.mu.Lock()
	for s.lifecycle.IsRunning() {
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		s.mu.Lock()
	}
	s.mu.Unlock()
	return nil
}

func (s *goalLoopState) ContinuationEnforced() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruptPending {
		return false
	}
	return s.lifecycle == goalLoopLifecycleRunning || s.lifecycle == goalLoopLifecycleRestartPending
}

func (s *goalLoopState) MarkInterruptPending() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.interruptPending = true
	s.mu.Unlock()
}

func (s *goalLoopState) ClearInterruptPending() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.interruptPending = false
	s.mu.Unlock()
}

func (s *goalLoopState) CommitInterrupt() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.interruptPending = false
	switch s.lifecycle {
	case goalLoopLifecycleRestartPending:
	case goalLoopLifecycleRunning:
		s.lifecycle = goalLoopLifecycleSuspending
	default:
		s.lifecycle = goalLoopLifecycleSuspended
	}
	s.mu.Unlock()
}
