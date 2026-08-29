package worktree

import "errors"

type worktreeError struct{ cause error }

func (e worktreeError) Error() string { return e.cause.Error() }
func (e worktreeError) Unwrap() error { return e.cause }

type worktreeUnappliedError struct{ worktreeError }
type worktreeAppliedError struct{ worktreeError }
type worktreeIndeterminateError struct{ worktreeError }
type worktreeTechnicalError struct{ worktreeError }

func (*worktreeAppliedError) WorktreeTransitionApplied()             {}
func (*worktreeIndeterminateError) WorktreeTransitionIndeterminate() {}
func (e *worktreeTechnicalError) WorktreeTechnicalFailure()          {}

func worktreeUnappliedTechnical(err error) error {
	if err == nil {
		return nil
	}
	return &worktreeUnappliedError{worktreeError{cause: &worktreeTechnicalError{worktreeError{cause: err}}}}
}

func worktreeUnappliedUserCorrectable(err error) error {
	if err == nil {
		return nil
	}
	return &worktreeUnappliedError{worktreeError{cause: err}}
}

func worktreeApplied(err error) error {
	if err == nil {
		return nil
	}
	return &worktreeAppliedError{worktreeError{cause: err}}
}

func worktreeIndeterminate(err error) error {
	if err == nil {
		err = errors.New("Worktree target application is indeterminate")
	}
	return &worktreeIndeterminateError{worktreeError{cause: err}}
}

func isWorktreeUnapplied(err error) bool {
	var target *worktreeUnappliedError
	return errors.As(err, &target)
}

func isWorktreeApplied(err error) bool {
	var target *worktreeAppliedError
	return errors.As(err, &target)
}

func isWorktreeIndeterminate(err error) bool {
	var target *worktreeIndeterminateError
	return errors.As(err, &target)
}

func worktreeUnappliedTechnicalUnlessClassified(err error) error {
	if err == nil ||
		isWorktreeUnapplied(err) ||
		isWorktreeApplied(err) ||
		isWorktreeIndeterminate(err) {
		return err
	}
	return worktreeUnappliedTechnical(err)
}

func worktreeUnappliedWithDiagnostic(unapplied error, diagnostic error) error {
	var technical *worktreeTechnicalError
	if errors.As(unapplied, &technical) {
		return worktreeUnappliedTechnical(diagnostic)
	}
	return worktreeUnappliedUserCorrectable(diagnostic)
}

func worktreeAppliedDiagnostic(err error) error {
	var applied *worktreeAppliedError
	if errors.As(err, &applied) {
		return applied.cause
	}
	return err
}
