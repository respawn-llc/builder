package runtime

import (
	"strings"
	"sync"

	"core/server/session"
)

type lockedContractState struct {
	mu     sync.Mutex
	locked *session.LockedContract
}

func newLockedContractState() *lockedContractState {
	return &lockedContractState{}
}

func (s *lockedContractState) Snapshot() (session.LockedContract, bool) {
	if s == nil {
		return session.LockedContract{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked == nil {
		return session.LockedContract{}, false
	}
	return *s.locked, true
}

func (s *lockedContractState) Set(locked session.LockedContract) {
	if s == nil {
		return
	}
	copyLocked := locked
	s.mu.Lock()
	s.locked = &copyLocked
	s.mu.Unlock()
}

func (s *lockedContractState) MarkPromptFacingSnapshotsStale() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.locked != nil {
		stale := s.locked.WithPromptFacingSnapshotsStale()
		s.locked = &stale
	}
	s.mu.Unlock()
}

func (s *lockedContractState) ApplyMainPromptSnapshot(locked session.LockedContract) {
	s.mutateFrom(locked, func(current *session.LockedContract) {
		*current = current.WithMainPromptSnapshot(session.LockedMainPromptSnapshot{
			SystemPrompt:    locked.SystemPrompt,
			HasSystemPrompt: locked.HasSystemPrompt,
			ToolPreambles:   locked.ToolPreambles,
			ContextWindow:   locked.ContextWindow,
			ContextPercent:  locked.ContextPercent,
		})
	})
}

func (s *lockedContractState) ApplyReviewerPromptSnapshot(locked session.LockedContract) {
	s.mutateFrom(locked, func(current *session.LockedContract) {
		*current = current.WithReviewerPromptSnapshot(session.LockedReviewerPromptSnapshot{
			ReviewerPrompt:    locked.ReviewerPrompt,
			HasReviewerPrompt: locked.HasReviewerPrompt,
		})
	})
}

func (s *lockedContractState) ApplyRequestShape(locked session.LockedContract) {
	s.mutateFrom(locked, func(current *session.LockedContract) {
		*current = current.WithRequestShape(session.LockedRequestShapeBackfill{
			EnabledTools:    locked.EnabledTools,
			HasEnabledTools: locked.HasEnabledTools,
			WebSearchMode:   locked.WebSearchMode,
		})
	})
}

func (s *lockedContractState) mutateFrom(locked session.LockedContract, mutate func(*session.LockedContract)) {
	if s == nil || mutate == nil {
		return
	}
	s.mu.Lock()
	if s.locked == nil {
		copyLocked := locked
		s.locked = &copyLocked
	} else {
		mutate(s.locked)
	}
	s.mu.Unlock()
}

func (s *lockedContractState) Model() string {
	locked, ok := s.Snapshot()
	if !ok {
		return ""
	}
	return strings.TrimSpace(locked.Model)
}

func (s *lockedContractState) MaxOutputToken() int {
	locked, ok := s.Snapshot()
	if !ok || locked.MaxOutputToken <= 0 {
		return 0
	}
	return locked.MaxOutputToken
}

func (s *lockedContractState) FillSystemPrompt(prompt string) {
	if s == nil {
		return
	}
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return
	}
	s.mu.Lock()
	if s.locked != nil && !s.locked.HasSystemPrompt {
		s.locked.SystemPrompt = trimmed
		s.locked.HasSystemPrompt = true
	}
	s.mu.Unlock()
}

func (s *lockedContractState) FillReviewerPrompt(prompt string) {
	if s == nil {
		return
	}
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return
	}
	s.mu.Lock()
	if s.locked != nil && !s.locked.HasReviewerPrompt {
		s.locked.ReviewerPrompt = trimmed
		s.locked.HasReviewerPrompt = true
	}
	s.mu.Unlock()
}

func (s *lockedContractState) ReviewerPromptSnapshot() (string, bool) {
	locked, ok := s.Snapshot()
	if !ok {
		return "", false
	}
	if locked.HasReviewerPrompt {
		return strings.TrimSpace(locked.ReviewerPrompt), true
	}
	if prompt := strings.TrimSpace(locked.ReviewerPrompt); prompt != "" {
		return prompt, true
	}
	return "", false
}
