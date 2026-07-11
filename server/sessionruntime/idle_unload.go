package sessionruntime

import (
	"context"
	"strings"
	"time"

	"core/server/registry"
)

type runtimeIdleTimer struct {
	generation uint64
	timer      *time.Timer
}

const (
	defaultRuntimeIdleUnloadDelay     = 5 * time.Second
	defaultRunFinishedIdleUnloadDelay = 3 * time.Minute
)

func (s *Service) runtimeInterestChanged(sessionID string, reason registry.RuntimeInterestReason) {
	delay := s.defaultIdleUnloadDelay()
	if reason == registry.RuntimeInterestRunFinished {
		delay = s.runFinishedIdleUnloadDelay()
	}
	s.scheduleIdleUnload(sessionID, delay)
}

func (s *Service) cancelScheduledIdleUnload(sessionID string) {
	if s == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return
	}
	s.mu.Lock()
	state := s.idleTimers[trimmedSessionID]
	if state != nil {
		state.generation++
		if state.timer != nil {
			state.timer.Stop()
		}
	}
	s.mu.Unlock()
}

func (s *Service) clearScheduledIdleUnload(sessionID string) {
	if s == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return
	}
	s.mu.Lock()
	state := s.idleTimers[trimmedSessionID]
	if state != nil && state.timer != nil {
		state.timer.Stop()
	}
	delete(s.idleTimers, trimmedSessionID)
	s.mu.Unlock()
}

func (s *Service) defaultIdleUnloadDelay() time.Duration {
	if s == nil || s.idleUnloadDelay <= 0 {
		return defaultRuntimeIdleUnloadDelay
	}
	return s.idleUnloadDelay
}

func (s *Service) runFinishedIdleUnloadDelay() time.Duration {
	if s == nil || s.runFinishedUnloadDelay <= 0 {
		return defaultRunFinishedIdleUnloadDelay
	}
	return s.runFinishedUnloadDelay
}

func (s *Service) scheduleIdleUnload(sessionID string, delay time.Duration) {
	if s == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" || delay <= 0 {
		return
	}
	s.mu.Lock()
	if s.idleTimers == nil {
		s.idleTimers = make(map[string]*runtimeIdleTimer)
	}
	state := s.idleTimers[trimmedSessionID]
	if state == nil {
		state = &runtimeIdleTimer{}
		s.idleTimers[trimmedSessionID] = state
	}
	state.generation++
	generation := state.generation
	if state.timer != nil {
		state.timer.Stop()
	}
	state.timer = time.AfterFunc(delay, func() {
		s.runScheduledIdleUnload(trimmedSessionID, generation)
	})
	s.mu.Unlock()
}

func (s *Service) runScheduledIdleUnload(sessionID string, generation uint64) {
	if s == nil {
		return
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return
	}
	s.mu.Lock()
	state := s.idleTimers[trimmedSessionID]
	if state == nil || state.generation != generation {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	claim := s.runtimes.RuntimeClaimFor(trimmedSessionID)
	if claim == nil || claim.Closing() || claim.OwnerCount() > 0 {
		return
	}
	if s.runtimeHasSubscribers(trimmedSessionID) {
		return
	}
	if active, err := s.runtimeHasBlockingActivity(context.Background(), trimmedSessionID); err != nil || active {
		return
	}
	closed, err := claim.CloseIfIdle(context.Background(), 0, s.drainBeforeClose(claim))
	if err == nil && closed {
		s.clearScheduledIdleUnload(trimmedSessionID)
	}
}

func (s *Service) runtimeHasSubscribers(sessionID string) bool {
	if s == nil || s.runtimes == nil {
		return false
	}
	return s.runtimes.HasRuntimeSubscribers(strings.TrimSpace(sessionID))
}
