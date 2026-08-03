package clientui

import (
	"errors"
	"strings"
)

type WorktreeDirtyStateKind string

const (
	WorktreeDirtyStateClean   WorktreeDirtyStateKind = "clean"
	WorktreeDirtyStateDirty   WorktreeDirtyStateKind = "dirty"
	WorktreeDirtyStateUnknown WorktreeDirtyStateKind = "unknown"
)

type WorktreeDirtyState struct {
	Kind           WorktreeDirtyStateKind `json:"kind"`
	DirtyFileCount *int                   `json:"dirty_file_count,omitempty"`
	UnknownCause   *string                `json:"unknown_cause,omitempty"`
}

func (state WorktreeDirtyState) Validate() error {
	switch state.Kind {
	case WorktreeDirtyStateClean:
		if state.DirtyFileCount != nil || state.UnknownCause != nil {
			return errors.New("clean dirty state has no payload")
		}
	case WorktreeDirtyStateDirty:
		if state.DirtyFileCount == nil || *state.DirtyFileCount <= 0 || state.UnknownCause != nil {
			return errors.New("dirty state requires a positive count only")
		}
	case WorktreeDirtyStateUnknown:
		if state.DirtyFileCount != nil || state.UnknownCause == nil || strings.TrimSpace(*state.UnknownCause) == "" {
			return errors.New("unknown dirty state requires an unknown cause only")
		}
	default:
		return errors.New("worktree dirty state kind is invalid")
	}
	return nil
}

func (state WorktreeDirtyState) ValidateDeletePrecondition() error {
	if err := state.Validate(); err != nil {
		return err
	}
	if state.Kind == WorktreeDirtyStateClean {
		return errors.New("clean worktree cannot fail delete precondition")
	}
	return nil
}

func (state WorktreeDirtyState) ValidateDeleteForTransition(transition WorktreeTransitionKind) error {
	if transition != WorktreeTransitionDelete {
		return errors.New("delete precondition is only valid for delete transitions")
	}
	return state.ValidateDeletePrecondition()
}
