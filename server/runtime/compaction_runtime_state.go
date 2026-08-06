package runtime

import (
	"strings"
	"sync"
)

type compactionRuntimeState struct {
	mu                             sync.Mutex
	count                          int
	soonReminderIssued             bool
	manualEligible                 bool
	historyReplacementMode         *string
	workflowPostCompletionBoundary bool
	active                         *TranscriptCompactionState
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

func newCompactionRuntimeState() *compactionRuntimeState {
	return &compactionRuntimeState{}
}

func (s *compactionRuntimeState) SetActive(stepID, mode string, count int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.active = &TranscriptCompactionState{
		StepID: strings.TrimSpace(stepID),
		Mode:   strings.TrimSpace(mode),
		Count:  count,
	}
	s.mu.Unlock()
}

func (s *compactionRuntimeState) ClearActive(stepID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.active != nil && s.active.StepID == strings.TrimSpace(stepID) {
		s.active = nil
	}
	s.mu.Unlock()
}

func (s *compactionRuntimeState) ActiveSnapshot() *TranscriptCompactionState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return nil
	}
	active := *s.active
	return &active
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

func (s *compactionRuntimeState) SetHistoryReplacementMode(mode string) {
	if s == nil {
		return
	}
	normalized := strings.TrimSpace(mode)
	s.mu.Lock()
	if normalized == "" {
		s.historyReplacementMode = nil
	} else {
		s.historyReplacementMode = &normalized
	}
	s.workflowPostCompletionBoundary = normalized == string(compactionModeWorkflowPostCompletion)
	s.mu.Unlock()
}

func (s *compactionRuntimeState) HistoryReplacementMode() (*string, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.historyReplacementMode == nil {
		return nil, false
	}
	value := *s.historyReplacementMode
	return &value, true
}

func (s *compactionRuntimeState) WorkflowPostCompletionBoundary() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workflowPostCompletionBoundary
}

func (s *compactionRuntimeState) ConsumeWorkflowPostCompletionBoundary() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.workflowPostCompletionBoundary {
		return false
	}
	s.workflowPostCompletionBoundary = false
	return true
}
