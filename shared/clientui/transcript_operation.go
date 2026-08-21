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

type TranscriptSessionRetargetOutcomeKind string

const (
	TranscriptSessionRetargetSucceeded TranscriptSessionRetargetOutcomeKind = "succeeded"
	TranscriptSessionRetargetFailed    TranscriptSessionRetargetOutcomeKind = "failed"
)

type TranscriptSessionRetargetSuccess struct {
	ProjectID               string
	ProjectKey              string
	ProjectName             string
	WorkspaceID             string
	CanonicalRoot           string
	WorkspaceName           string
	WorkspaceStatus         string
	WorkspaceBindingCreated bool
}

type TranscriptSessionRetargetFailure struct {
	Diagnostic                string
	UnchangedProjectID        string
	UnchangedProjectName      string
	UnchangedWorkingDirectory string
}

type TranscriptSessionRetargetOutcome struct {
	OperationID WorktreeTransitionID
	Kind        TranscriptSessionRetargetOutcomeKind
	Success     *TranscriptSessionRetargetSuccess
	Failure     *TranscriptSessionRetargetFailure
}

func (o TranscriptSessionRetargetOutcome) Validate() error {
	if err := o.OperationID.Validate(); err != nil {
		return err
	}
	switch o.Kind {
	case TranscriptSessionRetargetSucceeded:
		if o.Success == nil || o.Failure != nil {
			return fmt.Errorf("succeeded session retarget outcome must contain only success")
		}
		if strings.TrimSpace(o.Success.ProjectID) == "" ||
			strings.TrimSpace(o.Success.ProjectName) == "" ||
			strings.TrimSpace(o.Success.WorkspaceID) == "" ||
			strings.TrimSpace(o.Success.CanonicalRoot) == "" {
			return fmt.Errorf("session retarget success binding is incomplete")
		}
	case TranscriptSessionRetargetFailed:
		if o.Failure == nil || o.Success != nil {
			return fmt.Errorf("failed session retarget outcome must contain only failure")
		}
		if strings.TrimSpace(o.Failure.Diagnostic) == "" ||
			strings.TrimSpace(o.Failure.UnchangedProjectID) == "" ||
			strings.TrimSpace(o.Failure.UnchangedProjectName) == "" ||
			strings.TrimSpace(o.Failure.UnchangedWorkingDirectory) == "" {
			return fmt.Errorf("session retarget failure facts are incomplete")
		}
	default:
		return fmt.Errorf("unknown session retarget outcome kind %q", o.Kind)
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
