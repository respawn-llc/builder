package runtimefeed

import (
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

type TranscriptWorktreeTransitionOutcome struct {
	OperationID clientui.WorktreeTransitionID
	Transition  clientui.WorktreeTransitionKind
	State       clientui.WorktreeTransitionState
	Failure     *TranscriptDiagnostic
}

type OperationalDiagnosticCode string

const (
	OperationalDiagnosticSleepGuardFailed           OperationalDiagnosticCode = "sleep_guard_failed"
	OperationalDiagnosticPromptHistoryPersistFailed OperationalDiagnosticCode = "prompt_history_persist_failed"
)

type TranscriptOperationalDiagnostic struct {
	Code   OperationalDiagnosticCode
	StepID *runtimeids.StepID
	Detail string
}

func (o TranscriptWorktreeTransitionOutcome) Validate() error {
	if err := o.OperationID.Validate(); err != nil {
		return err
	}
	switch o.Transition {
	case clientui.WorktreeTransitionEnter,
		clientui.WorktreeTransitionLeave,
		clientui.WorktreeTransitionDelete:
	default:
		return fmt.Errorf("unknown worktree transition kind %q", o.Transition)
	}
	switch o.State {
	case clientui.WorktreeTransitionCompleted:
		if o.Failure != nil {
			return fmt.Errorf("completed worktree transition cannot carry failure")
		}
		return nil
	case clientui.WorktreeTransitionFailed:
		if o.Failure == nil {
			return fmt.Errorf("failed worktree transition requires failure diagnostic")
		}
		return o.Failure.Validate()
	default:
		return fmt.Errorf("unknown worktree transition state %q", o.State)
	}
}

func (d TranscriptOperationalDiagnostic) Validate() error {
	switch d.Code {
	case OperationalDiagnosticSleepGuardFailed,
		OperationalDiagnosticPromptHistoryPersistFailed:
	default:
		return fmt.Errorf("unknown operational diagnostic code %q", d.Code)
	}
	if d.StepID != nil && d.StepID.IsZero() {
		return fmt.Errorf("operational diagnostic step id is invalid")
	}
	if strings.TrimSpace(d.Detail) == "" {
		return fmt.Errorf("operational diagnostic detail is required")
	}
	return nil
}
