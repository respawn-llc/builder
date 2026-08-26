package clientui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"core/shared/runtimeids"
	"core/shared/worktreecontract"
)

type TranscriptWorktreeTransitionOutcome struct {
	OperationID        worktreecontract.OperationID
	Transition         WorktreeTransitionKind
	State              WorktreeTransitionState
	Failure            *TranscriptDiagnostic
	DeletePrecondition *worktreecontract.DirtyState
}

type transcriptWorktreeTransitionOutcomeJSON struct {
	OperationID        string
	Transition         WorktreeTransitionKind
	State              WorktreeTransitionState
	Failure            *TranscriptDiagnostic
	DeletePrecondition *transcriptWorktreeDirtyStateJSON
}

type transcriptWorktreeDirtyStateJSON struct {
	Kind           worktreecontract.DirtyStateKind `json:"kind"`
	DirtyFileCount *int                            `json:"dirty_file_count,omitempty"`
	UnknownCause   *string                         `json:"unknown_cause,omitempty"`
}

func (o TranscriptWorktreeTransitionOutcome) MarshalJSON() ([]byte, error) {
	if err := o.OperationID.Validate(); err != nil {
		return nil, err
	}
	var precondition *transcriptWorktreeDirtyStateJSON
	if o.DeletePrecondition != nil {
		precondition = &transcriptWorktreeDirtyStateJSON{
			Kind:           o.DeletePrecondition.Kind,
			DirtyFileCount: o.DeletePrecondition.DirtyFileCount,
			UnknownCause:   o.DeletePrecondition.UnknownCause,
		}
	}
	return json.Marshal(transcriptWorktreeTransitionOutcomeJSON{
		OperationID:        o.OperationID.String(),
		Transition:         o.Transition,
		State:              o.State,
		Failure:            o.Failure,
		DeletePrecondition: precondition,
	})
}

func (o *TranscriptWorktreeTransitionOutcome) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*o = TranscriptWorktreeTransitionOutcome{}
		return nil
	}
	var wire transcriptWorktreeTransitionOutcomeJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	operationID, err := worktreecontract.ParseOperationID(wire.OperationID)
	if err != nil {
		return err
	}
	var precondition *worktreecontract.DirtyState
	if wire.DeletePrecondition != nil {
		precondition = &worktreecontract.DirtyState{
			Kind:           wire.DeletePrecondition.Kind,
			DirtyFileCount: wire.DeletePrecondition.DirtyFileCount,
			UnknownCause:   wire.DeletePrecondition.UnknownCause,
		}
	}
	*o = TranscriptWorktreeTransitionOutcome{
		OperationID:        operationID,
		Transition:         wire.Transition,
		State:              wire.State,
		Failure:            wire.Failure,
		DeletePrecondition: precondition,
	}
	return nil
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
