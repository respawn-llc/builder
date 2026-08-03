package worktreecontract

import (
	"errors"
	"strings"
)

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
