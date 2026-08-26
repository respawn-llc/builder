package clientui

import (
	"errors"
	"strings"

	"core/shared/worktreecontract"
)

type WorktreeTransitionKind string

const (
	WorktreeTransitionEnter  WorktreeTransitionKind = "enter"
	WorktreeTransitionLeave  WorktreeTransitionKind = "leave"
	WorktreeTransitionDelete WorktreeTransitionKind = "delete"
)

type WorktreeTransitionState string

const (
	WorktreeTransitionCompleted WorktreeTransitionState = "completed"
	WorktreeTransitionFailed    WorktreeTransitionState = "failed"
)

type WorktreeTransitionFailure struct {
	Diagnostic         string
	DeletePrecondition *worktreecontract.DirtyState
}

type WorktreeTransitionOutcome struct {
	OperationID worktreecontract.OperationID
	Transition  WorktreeTransitionKind
	State       WorktreeTransitionState
	Failure     *WorktreeTransitionFailure
}

func (outcome WorktreeTransitionOutcome) Validate() error {
	if err := outcome.OperationID.Validate(); err != nil {
		return err
	}
	switch outcome.Transition {
	case WorktreeTransitionEnter, WorktreeTransitionLeave, WorktreeTransitionDelete:
	default:
		return errors.New("worktree transition kind is invalid")
	}
	switch outcome.State {
	case WorktreeTransitionCompleted:
		if outcome.Failure != nil {
			return errors.New("completed worktree transition cannot contain failure facts")
		}
	case WorktreeTransitionFailed:
		if outcome.Failure == nil {
			return errors.New("failed worktree transition requires failure facts")
		}
		if strings.TrimSpace(outcome.Failure.Diagnostic) == "" {
			return errors.New("worktree transition failure diagnostic is required")
		}
		if outcome.Failure.DeletePrecondition != nil {
			precondition := outcome.Failure.DeletePrecondition
			if err := worktreecontract.ValidateDeleteTransitionPrecondition(
				worktreecontract.TransitionKind(outcome.Transition),
				precondition.Kind,
				precondition.DirtyFileCount,
				precondition.UnknownCause,
			); err != nil {
				return err
			}
		}
	default:
		return errors.New("worktree transition state is invalid")
	}
	return nil
}
