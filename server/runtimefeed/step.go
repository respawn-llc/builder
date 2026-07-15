package runtimefeed

import (
	"fmt"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

type StepLifecycleState string

const (
	StepLifecycleStarted  StepLifecycleState = "started"
	StepLifecycleFinished StepLifecycleState = "finished"
)

type TranscriptStepState struct {
	RunID      runtimeids.RunID
	StepID     runtimeids.StepID
	Lifecycle  StepLifecycleState
	ActiveKind clientui.RuntimeActivityActiveKind
	Status     clientui.RunStatus
}

type ReviewerState string

const (
	ReviewerStateRunning   ReviewerState = "running"
	ReviewerStateCompleted ReviewerState = "completed"
)

type TranscriptReviewerState struct {
	StepID runtimeids.StepID
	State  ReviewerState
}

func (s TranscriptStepState) Validate() error {
	if s.RunID.IsZero() {
		return fmt.Errorf("step state run id is required")
	}
	if s.StepID.IsZero() {
		return fmt.Errorf("step state step id is required")
	}
	if err := s.ActiveKind.Validate(); err != nil {
		return err
	}
	switch s.Lifecycle {
	case StepLifecycleStarted:
		if s.Status != clientui.RunStatusRunning {
			return fmt.Errorf("started step state requires running status")
		}
	case StepLifecycleFinished:
		switch s.Status {
		case clientui.RunStatusCompleted, clientui.RunStatusInterrupted, clientui.RunStatusFailed:
		default:
			return fmt.Errorf("finished step state has invalid status %q", s.Status)
		}
	default:
		return fmt.Errorf("unknown step lifecycle state %q", s.Lifecycle)
	}
	return nil
}

func (s TranscriptReviewerState) Validate() error {
	if s.StepID.IsZero() {
		return fmt.Errorf("reviewer state step id is required")
	}
	switch s.State {
	case ReviewerStateRunning, ReviewerStateCompleted:
		return nil
	default:
		return fmt.Errorf("unknown reviewer state %q", s.State)
	}
}
