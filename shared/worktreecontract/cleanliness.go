package worktreecontract

import (
	"errors"
	"strings"
)

type DirtyStateKind string

const (
	DirtyStateClean   DirtyStateKind = "clean"
	DirtyStateDirty   DirtyStateKind = "dirty"
	DirtyStateUnknown DirtyStateKind = "unknown"
)

type TransitionKind string

const TransitionDelete TransitionKind = "delete"

func ValidateDirtyState(kind DirtyStateKind, dirtyFileCount *int, unknownCause *string) error {
	switch kind {
	case DirtyStateClean:
		if dirtyFileCount != nil || unknownCause != nil {
			return errors.New("clean dirty state has no payload")
		}
	case DirtyStateDirty:
		if dirtyFileCount == nil || *dirtyFileCount <= 0 || unknownCause != nil {
			return errors.New("dirty state requires a positive count only")
		}
	case DirtyStateUnknown:
		if dirtyFileCount != nil || unknownCause == nil || strings.TrimSpace(*unknownCause) == "" {
			return errors.New("unknown dirty state requires an unknown cause only")
		}
	default:
		return errors.New("worktree dirty state kind is invalid")
	}
	return nil
}

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
