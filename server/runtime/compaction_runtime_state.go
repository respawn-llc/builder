package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/llm"
	"core/server/session"
)

type workflowPostCompletionActivity uint8

const (
	workflowPostCompletionNoActivity workflowPostCompletionActivity = iota
	workflowPostCompletionDurableActivity
)

func workflowPostCompletionMessageActivity(message llm.Message) workflowPostCompletionActivity {
	if message.MessageType == nil {
		return workflowPostCompletionDurableActivity
	}
	if *message.MessageType == llm.MessageTypeCompactionSoonReminder {
		return workflowPostCompletionNoActivity
	}
	if _, ok := classifyMetaContextMessage(message); ok {
		return workflowPostCompletionNoActivity
	}
	return workflowPostCompletionDurableActivity
}

type compactionRuntimeState struct {
	mu                             sync.Mutex
	count                          int
	soonReminderIssued             bool
	manualEligible                 bool
	historyReplacementMode         *session.CompactionMode
	workflowPostCompletionBoundary bool
	active                         *TranscriptCompactionState
	contextFacts                   session.SessionContextFacts
}

type liveChatContextCompactionSnapshot struct {
	completedCompactionCount int
	compactionRunning        bool
	manualCompactEligible    bool
}

func (s *compactionRuntimeState) LiveChatContextSnapshot() liveChatContextCompactionSnapshot {
	if s == nil {
		return liveChatContextCompactionSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := liveChatContextCompactionSnapshot{
		compactionRunning: s.active != nil,
	}
	if s.contextFacts.CompletedCompactionCount != nil {
		snapshot.completedCompactionCount = s.count
	}
	if s.contextFacts.ManualCompactEligible != nil {
		snapshot.manualCompactEligible = s.manualEligible
	}
	return snapshot
}

func (s *compactionRuntimeState) SetContextFacts(facts session.SessionContextFacts) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.contextFacts = facts.Clone()
	s.mu.Unlock()
}

func (s *compactionRuntimeState) SetPresentedManualCompactEligibility(eligible bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.contextFacts.ManualCompactEligible = &eligible
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

func newCompactionRuntimeState() *compactionRuntimeState {
	return &compactionRuntimeState{}
}

func (s *compactionRuntimeState) SetActive(
	stepID string,
	mode string,
	count int,
) {
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

func (s *compactionRuntimeState) SetHistoryReplacementMode(mode *session.CompactionMode) error {
	if s == nil {
		return errors.New("compaction runtime state is required")
	}
	if mode == nil {
		s.mu.Lock()
		s.historyReplacementMode = nil
		s.workflowPostCompletionBoundary = false
		s.mu.Unlock()
		return nil
	}
	normalized := session.CompactionMode(strings.TrimSpace(string(*mode)))
	switch normalized {
	case session.CompactionModeAuto,
		session.CompactionModeHandoff,
		session.CompactionModeManual,
		session.CompactionModeWorkflowPostCompletion:
	default:
		return fmt.Errorf("invalid history replacement mode %q", normalized)
	}
	s.mu.Lock()
	s.historyReplacementMode = &normalized
	s.workflowPostCompletionBoundary = normalized == session.CompactionModeWorkflowPostCompletion
	s.mu.Unlock()
	return nil
}

func (s *compactionRuntimeState) HistoryReplacementMode() (*session.CompactionMode, bool) {
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

func (s *compactionRuntimeState) ApplyWorkflowPostCompletionActivity(activity workflowPostCompletionActivity) bool {
	if s == nil {
		return false
	}
	if activity != workflowPostCompletionDurableActivity {
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
