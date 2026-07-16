package clientui

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
)

type TranscriptContextUsage struct {
	UsedTokens      int
	WindowTokens    int
	CacheHitPercent *int
}

type TranscriptGoalStatus struct {
	Goal *TranscriptGoal
}

type TranscriptGoal struct {
	ID        string
	Objective string
	Status    RuntimeGoalStatus
	Suspended bool
}

type CompactionState string

const (
	CompactionStarted   CompactionState = "started"
	CompactionCompleted CompactionState = "completed"
	CompactionFailed    CompactionState = "failed"
)

type TranscriptCompactionStatus struct {
	StepID     runtimeids.StepID
	State      CompactionState
	Mode       string
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
	if s.Goal == nil {
		return nil
	}
	return s.Goal.Validate()
}

func (g TranscriptGoal) Validate() error {
	if strings.TrimSpace(g.ID) == "" {
		return fmt.Errorf("goal id is required")
	}
	if strings.TrimSpace(g.Objective) == "" {
		return fmt.Errorf("goal objective is required")
	}
	switch g.Status {
	case RuntimeGoalStatusActive:
	case RuntimeGoalStatusPaused, RuntimeGoalStatusComplete:
		if g.Suspended {
			return fmt.Errorf("%s goal cannot be suspended", g.Status)
		}
	default:
		return fmt.Errorf("unknown goal status %q", g.Status)
	}
	return nil
}

func (s TranscriptCompactionStatus) Validate() error {
	if s.StepID.IsZero() {
		return fmt.Errorf("compaction status step id is required")
	}
	if strings.TrimSpace(s.Mode) == "" {
		return fmt.Errorf("compaction mode is required")
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
