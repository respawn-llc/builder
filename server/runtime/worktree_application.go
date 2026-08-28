package runtime

import (
	"errors"
	"fmt"
)

type WorktreeApplicationCertainty uint8

const (
	WorktreeApplicationUnapplied WorktreeApplicationCertainty = iota + 1
	WorktreeApplicationCommitted
	WorktreeApplicationIndeterminate
)

type WorktreeApplicationResult struct {
	Certainty WorktreeApplicationCertainty
	Err       error
}

func UnappliedWorktreeApplication(err error) WorktreeApplicationResult {
	return WorktreeApplicationResult{
		Certainty: WorktreeApplicationUnapplied,
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
	case WorktreeApplicationUnapplied, WorktreeApplicationIndeterminate:
		if r.Err == nil {
			return errors.New("failed Worktree application requires an error")
		}
	case WorktreeApplicationCommitted:
	default:
		return errors.New("Worktree application certainty is required")
	}
	return nil
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
