package clientui

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
	"core/shared/worktreecontract"
)

type TranscriptWorktreeTransitionOutcome struct {
	OperationID        WorktreeTransitionID
	Transition         WorktreeTransitionKind
	State              WorktreeTransitionState
	Failure            *TranscriptDiagnostic
	DeletePrecondition *WorktreeDirtyState
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
	case WorktreeTransitionEnter,
		WorktreeTransitionLeave,
		WorktreeTransitionDelete:
	default:
		return fmt.Errorf("unknown worktree transition kind %q", o.Transition)
	}
	switch o.State {
	case WorktreeTransitionCompleted:
		if o.Failure != nil || o.DeletePrecondition != nil {
			return fmt.Errorf("completed worktree transition cannot carry failure")
		}
		return nil
	case WorktreeTransitionFailed:
		if o.Failure == nil {
			return fmt.Errorf("failed worktree transition requires failure diagnostic")
		}
		if err := o.Failure.Validate(); err != nil {
			return err
		}
		if o.DeletePrecondition != nil {
			precondition := o.DeletePrecondition
			if err := worktreecontract.ValidateDeleteTransitionPrecondition(
				worktreecontract.TransitionKind(o.Transition),
				precondition.Kind,
				precondition.DirtyFileCount,
				precondition.UnknownCause,
			); err != nil {
				return err
			}
		}
		return nil
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
