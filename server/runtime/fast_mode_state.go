package runtime

import (
	"sync"
	"sync/atomic"
)

type FastModeState struct {
	mu      sync.Mutex
	enabled atomic.Bool
}

func NewFastModeState(enabled bool) *FastModeState {
	state := &FastModeState{}
	state.enabled.Store(enabled)
	return state
}

func (s *FastModeState) Enabled() bool {
	if s == nil {
		return false
	}
	return s.enabled.Load()
}

func (s *FastModeState) SetEnabled(enabled bool) bool {
	if s == nil {
		return false
	}
	changed, _ := s.SetEnabledWithTransaction(enabled, nil)
	return changed
}

// SetEnabledWithTransaction serializes the changed-state decision with a
// caller-supplied fallible step that must succeed before the shared state flips.
func (s *FastModeState) SetEnabledWithTransaction(enabled bool, beforeApply func(changed bool) error) (bool, error) {
	if s == nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.enabled.Load() != enabled
	if beforeApply != nil {
		if err := beforeApply(changed); err != nil {
			return false, err
		}
	}
	if changed {
		s.enabled.Store(enabled)
	}
	return changed, nil
}
