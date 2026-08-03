package clientui

import (
	"core/shared/worktreecontract"
)

type WorktreeDirtyStateKind = worktreecontract.DirtyStateKind

const (
	WorktreeDirtyStateClean   = worktreecontract.DirtyStateClean
	WorktreeDirtyStateDirty   = worktreecontract.DirtyStateDirty
	WorktreeDirtyStateUnknown = worktreecontract.DirtyStateUnknown
)

type WorktreeDirtyState struct {
	Kind           WorktreeDirtyStateKind `json:"kind"`
	DirtyFileCount *int                   `json:"dirty_file_count,omitempty"`
	UnknownCause   *string                `json:"unknown_cause,omitempty"`
}

func (state WorktreeDirtyState) Validate() error {
	return worktreecontract.ValidateDirtyState(state.Kind, state.DirtyFileCount, state.UnknownCause)
}
