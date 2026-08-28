package runtime

import (
	"errors"
	"fmt"
)

type WorktreeApplicationCertainty uint8
type WorktreeApplicationFailureKind uint8

const (
	WorktreeApplicationUnapplied WorktreeApplicationCertainty = iota + 1
	WorktreeApplicationCommitted
	WorktreeApplicationIndeterminate
)

const (
	WorktreeApplicationFailureTechnical WorktreeApplicationFailureKind = iota + 1
	WorktreeApplicationFailureUserCorrectable
)

type WorktreeApplicationResult struct {
	Certainty WorktreeApplicationCertainty
	Failure   WorktreeApplicationFailureKind
	Err       error
}

func UnappliedWorktreeApplication(err error) WorktreeApplicationResult {
	return WorktreeApplicationResult{
		Certainty: WorktreeApplicationUnapplied,
		Failure:   WorktreeApplicationFailureTechnical,
		Err:       err,
	}
}

func UnappliedUserCorrectableWorktreeApplication(err error) WorktreeApplicationResult {
	return WorktreeApplicationResult{
		Certainty: WorktreeApplicationUnapplied,
		Failure:   WorktreeApplicationFailureUserCorrectable,
		Err:       err,
	}
}

func CommittedWorktreeApplication(err error) WorktreeApplicationResult {
	return WorktreeApplicationResult{
		Certainty: WorktreeApplicationCommitted,
		Err:       err,
	}
}

func IndeterminateWorktreeApplication(err error) WorktreeApplicationResult {
	return WorktreeApplicationResult{
		Certainty: WorktreeApplicationIndeterminate,
		Err:       err,
	}
}

func (r WorktreeApplicationResult) Validate() error {
	switch r.Certainty {
	case WorktreeApplicationUnapplied:
		if r.Err == nil {
			return errors.New("failed Worktree application requires an error")
		}
		switch r.Failure {
		case WorktreeApplicationFailureTechnical, WorktreeApplicationFailureUserCorrectable:
		default:
			return errors.New("unapplied Worktree application failure kind is required")
		}
	case WorktreeApplicationIndeterminate:
		if r.Err == nil {
			return errors.New("failed Worktree application requires an error")
		}
		if r.Failure != 0 {
			return errors.New("indeterminate Worktree application cannot have a failure kind")
		}
	case WorktreeApplicationCommitted:
		if r.Failure != 0 {
			return errors.New("committed Worktree application cannot have a failure kind")
		}
	default:
		return errors.New("Worktree application certainty is required")
	}
	return nil
}

func (r WorktreeApplicationResult) WithError(err error) WorktreeApplicationResult {
	r.Err = err
	return r
}

func (r WorktreeApplicationResult) RequiresTechnicalRestoration() bool {
	return r.Validate() == nil &&
		r.Certainty == WorktreeApplicationUnapplied &&
		r.Failure == WorktreeApplicationFailureTechnical
}

func worktreeApplicationError(result WorktreeApplicationResult) error {
	if err := result.Validate(); err != nil {
		return &resultGroupFatal{
			Committed: false,
			Cause:     fmt.Errorf("invalid Worktree application result: %w", err),
		}
	}
	if result.Certainty == WorktreeApplicationIndeterminate {
		return &resultGroupFatal{Committed: false, Cause: result.Err}
	}
	return result.Err
}
