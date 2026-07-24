package clientui

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
)

type TranscriptSessionStatus struct {
	ReviewerFrequency         string
	ReviewerEnabled           bool
	AutoCompactionEnabled     bool
	QuestionsEnabled          bool
	FastModeAvailable         bool
	FastModeEnabled           bool
	ThinkingLevel             string
	CompactionMode            string
	PreviousSessionID         *runtimeids.SessionID
	ParentAgentSessionID      *runtimeids.SessionID
	NavigationTargetSessionID *runtimeids.SessionID
	Workflow                  *TranscriptWorkflowSession
}

type TranscriptWorkflowSession struct {
	Active     bool
	TaskID     string
	WorkflowID string
}

func (s TranscriptSessionStatus) Validate() error {
	if strings.TrimSpace(s.ReviewerFrequency) == "" {
		return fmt.Errorf("session status reviewer frequency is required")
	}
	if strings.TrimSpace(s.ThinkingLevel) == "" {
		return fmt.Errorf("session status thinking level is required")
	}
	if strings.TrimSpace(s.CompactionMode) == "" {
		return fmt.Errorf("session status compaction mode is required")
	}
	if s.FastModeEnabled && !s.FastModeAvailable {
		return fmt.Errorf("session status cannot enable unavailable fast mode")
	}
	if s.PreviousSessionID != nil && s.PreviousSessionID.IsZero() {
		return fmt.Errorf("session status previous session id is invalid")
	}
	if s.ParentAgentSessionID != nil && s.ParentAgentSessionID.IsZero() {
		return fmt.Errorf("session status parent agent session id is invalid")
	}
	if s.NavigationTargetSessionID != nil && s.NavigationTargetSessionID.IsZero() {
		return fmt.Errorf("session status navigation target session id is invalid")
	}
	if s.Workflow != nil {
		return s.Workflow.Validate()
	}
	return nil
}

func (s TranscriptWorkflowSession) Validate() error {
	if strings.TrimSpace(s.TaskID) == "" {
		return fmt.Errorf("workflow session task id is required")
	}
	if strings.TrimSpace(s.WorkflowID) == "" {
		return fmt.Errorf("workflow session workflow id is required")
	}
	return nil
}
