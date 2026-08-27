package clientui

import (
	"fmt"

	"core/shared/runtimeids"
)

type TranscriptContextUsage struct {
	UsedTokens      int
	WindowTokens    int
	CacheHitPercent *int
}

type TranscriptGoalStatus struct {
	Goal         *TranscriptGoal
	Availability *GoalAvailability
}

type TranscriptGoal struct {
	*Goal
	Suspended bool
}

type CompactionState string

const (
	CompactionStarted   CompactionState = "started"
	CompactionCompleted CompactionState = "completed"
	CompactionFailed    CompactionState = "failed"
)

type CompactionMode string

const (
	CompactionModeAuto                   CompactionMode = "auto"
	CompactionModeHandoff                CompactionMode = "handoff"
	CompactionModeManual                 CompactionMode = "manual"
	CompactionModeWorkflowPostCompletion CompactionMode = "workflow_post_completion"
)

type TranscriptCompactionStatus struct {
	StepID     runtimeids.StepID
	RequestID  *runtimeids.CompactionRequestID
	State      CompactionState
	Mode       CompactionMode
	Count      int
	Diagnostic *TranscriptDiagnostic
}

func (u TranscriptContextUsage) Validate() error {
	if u.UsedTokens < 0 {
		return fmt.Errorf("context used tokens cannot be negative")
	}
	if u.WindowTokens <= 0 {
		return fmt.Errorf("context window tokens must be positive")
	}
	if u.CacheHitPercent != nil && (*u.CacheHitPercent < 0 || *u.CacheHitPercent > 100) {
		return fmt.Errorf("context cache hit percent %d is outside 0..100", *u.CacheHitPercent)
	}
	return nil
}

func (s TranscriptGoalStatus) Validate() error {
	if s.Availability != nil {
		if err := s.Availability.Validate(); err != nil {
			return err
		}
	}
	if s.Goal == nil {
		return nil
	}
	if s.Goal.Goal == nil {
		return fmt.Errorf("transcript goal core is required")
	}
	if err := s.Goal.Goal.Validate(); err != nil {
		return err
	}
	switch s.Goal.Goal.Status {
	case RuntimeGoalStatusActive:
	case RuntimeGoalStatusPaused, RuntimeGoalStatusComplete:
		if s.Goal.Suspended {
			return fmt.Errorf("%s goal cannot be suspended", s.Goal.Goal.Status)
		}
	}
	return nil
}

func (s TranscriptCompactionStatus) Validate() error {
	if s.StepID.IsZero() {
		return fmt.Errorf("compaction status step id is required")
	}
	switch s.Mode {
	case CompactionModeAuto, CompactionModeHandoff, CompactionModeManual, CompactionModeWorkflowPostCompletion:
	default:
		return fmt.Errorf("unknown compaction mode %q", s.Mode)
	}
	if s.RequestID != nil {
		if s.RequestID.IsZero() {
			return fmt.Errorf("compaction request id cannot be zero")
		}
		if s.Mode != CompactionModeManual {
			return fmt.Errorf("%s compaction cannot carry a client request id", s.Mode)
		}
	}
	if s.Count < 0 {
		return fmt.Errorf("compaction count cannot be negative")
	}
	switch s.State {
	case CompactionStarted, CompactionCompleted:
		if s.Diagnostic != nil {
			return fmt.Errorf("%s compaction cannot carry diagnostic", s.State)
		}
		return nil
	case CompactionFailed:
		if s.Diagnostic == nil {
			return fmt.Errorf("failed compaction requires diagnostic")
		}
		return s.Diagnostic.Validate()
	default:
		return fmt.Errorf("unknown compaction state %q", s.State)
	}
}
