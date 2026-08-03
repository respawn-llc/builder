package worktreecontract

import (
	"errors"
	"strings"
)

func ValidateDirtyState[K ~string](kind K, dirtyFileCount *int, unknownCause *string) error {
	switch string(kind) {
	case "clean":
		if dirtyFileCount != nil || unknownCause != nil {
			return errors.New("clean dirty state has no payload")
		}
	case "dirty":
		if dirtyFileCount == nil || *dirtyFileCount <= 0 || unknownCause != nil {
			return errors.New("dirty state requires a positive count only")
		}
	case "unknown":
		if dirtyFileCount != nil || unknownCause == nil || strings.TrimSpace(*unknownCause) == "" {
			return errors.New("unknown dirty state requires an unknown cause only")
		}
	default:
		return errors.New("worktree dirty state kind is invalid")
	}
	return nil
}

func ValidateDeletePrecondition[K ~string](kind K, dirtyFileCount *int, unknownCause *string) error {
	if err := ValidateDirtyState(kind, dirtyFileCount, unknownCause); err != nil {
		return err
	}
	if string(kind) == "clean" {
		return errors.New("clean worktree cannot fail delete precondition")
	}
	return nil
}

func ValidateDeleteTransitionPrecondition[T ~string, K ~string](
	transition T,
	kind K,
	dirtyFileCount *int,
	unknownCause *string,
) error {
	if string(transition) != "delete" {
		return errors.New("delete precondition is only valid for delete transitions")
	}
	return ValidateDeletePrecondition(kind, dirtyFileCount, unknownCause)
}
