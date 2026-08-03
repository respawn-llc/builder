package clientui

import (
	"core/shared/worktreecontract"
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
	return worktreecontract.ValidateDirtyState(state.Kind, state.DirtyFileCount, state.UnknownCause)
}
