package worktreecontract

import "errors"

func ValidateDeletePrecondition(kind DirtyStateKind, dirtyFileCount *int, unknownCause *string) error {
	if err := ValidateDirtyState(kind, dirtyFileCount, unknownCause); err != nil {
		return err
	}
	if kind == DirtyStateClean {
		return errors.New("clean worktree cannot fail delete precondition")
	}
	return nil
}

func ValidateDeleteTransitionPrecondition(
	transition TransitionKind,
	kind DirtyStateKind,
	dirtyFileCount *int,
	unknownCause *string,
) error {
	if transition != TransitionDelete {
		return errors.New("delete precondition is only valid for delete transitions")
	}
	return ValidateDeletePrecondition(kind, dirtyFileCount, unknownCause)
}
