package clientui

import (
	"fmt"
	"strings"

	"buf.build/go/protovalidate"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeids"
	"core/shared/worktreecontract"
)

type TranscriptWorktreeTransitionOutcome struct {
	OperationID        WorktreeTransitionID
	Transition         WorktreeTransitionKind
	State              WorktreeTransitionState
	Failure            *TranscriptDiagnostic
	SelectorError      *worktreepb.SelectorErrorDetails
	DeletePrecondition *WorktreeDirtyState
}

type OperationalDiagnosticCode string

const (
	OperationalDiagnosticSleepGuardFailed           OperationalDiagnosticCode = "sleep_guard_failed"
	OperationalDiagnosticPromptHistoryPersistFailed OperationalDiagnosticCode = "prompt_history_persist_failed"
	OperationalDiagnosticContextFactsPersistFailed  OperationalDiagnosticCode = "context_facts_persist_failed"
	OperationalDiagnosticInFlightClearFailed        OperationalDiagnosticCode = "in_flight_clear_failed"
	OperationalDiagnosticProviderTurnStateInvalid   OperationalDiagnosticCode = "provider_turn_state_invalid"
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
		if o.Failure != nil || o.SelectorError != nil || o.DeletePrecondition != nil {
			return fmt.Errorf("completed worktree transition cannot carry failure")
		}
		return nil
	case WorktreeTransitionFailed:
		if (o.Failure == nil) == (o.SelectorError == nil) {
			return fmt.Errorf("failed worktree transition requires exactly one failure detail")
		}
		if o.Failure != nil {
			if err := o.Failure.Validate(); err != nil {
				return err
			}
		} else if err := protovalidate.Validate(o.SelectorError); err != nil {
			return err
		}
		if o.DeletePrecondition != nil {
			if o.SelectorError != nil {
				return fmt.Errorf("selector failure cannot carry delete precondition")
			}
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
		OperationalDiagnosticPromptHistoryPersistFailed,
		OperationalDiagnosticContextFactsPersistFailed,
		OperationalDiagnosticInFlightClearFailed:
		if strings.TrimSpace(d.Detail) == "" {
			return fmt.Errorf("operational diagnostic detail is required")
		}
	case OperationalDiagnosticProviderTurnStateInvalid:
		if d.Detail != "" {
			return fmt.Errorf("provider turn-state diagnostic cannot carry detail")
		}
	default:
		return fmt.Errorf("unknown operational diagnostic code %q", d.Code)
	}
	if d.StepID != nil && d.StepID.IsZero() {
		return fmt.Errorf("operational diagnostic step id is invalid")
	}
	return nil
}
