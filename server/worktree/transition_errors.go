package worktree

import "errors"

type worktreeUnappliedError struct {
	cause error
}

func (e *worktreeUnappliedError) Error() string {
	return e.cause.Error()
}

func (e *worktreeUnappliedError) Unwrap() error {
	return e.cause
}

type worktreeAppliedError struct {
	cause error
}

func (e *worktreeAppliedError) Error() string {
	return e.cause.Error()
}

func (e *worktreeAppliedError) Unwrap() error {
	return e.cause
}

type worktreeIndeterminateError struct {
	cause error
}

func (e *worktreeIndeterminateError) Error() string {
	return e.cause.Error()
}

func (e *worktreeIndeterminateError) Unwrap() error {
	return e.cause
}

func (e *worktreeIndeterminateError) WorktreeTransitionIndeterminate() {}

type worktreeTechnicalError struct {
	cause error
}

func (e *worktreeTechnicalError) Error() string {
	return e.cause.Error()
}

func (e *worktreeTechnicalError) Unwrap() error {
	return e.cause
}

func (e *worktreeTechnicalError) WorktreeTechnicalFailure() {}

func worktreeUnappliedTechnical(err error) error {
	if err == nil {
		return nil
	}
	return &worktreeUnappliedError{cause: &worktreeTechnicalError{cause: err}}
}

func worktreeUnappliedUserCorrectable(err error) error {
	if err == nil {
		return nil
	}
	return &worktreeUnappliedError{cause: err}
}

func worktreeApplied(err error) error {
	if err == nil {
		return nil
	}
	return &worktreeAppliedError{cause: err}
}

func worktreeIndeterminate(err error) error {
	if err == nil {
		err = errors.New("Worktree target application is indeterminate")
	}
	return &worktreeIndeterminateError{cause: err}
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
