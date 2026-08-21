package serverapi

import (
	"errors"
)

var ErrWorkspaceDetachConflict = errors.New("workspace detach preparation conflicted")
var ErrWorkspaceMutationFailed = errors.New("workspace mutation failed")

type WorkspaceDetachConflictError struct {
	ProjectID   string
	WorkspaceID string
}

func (e *WorkspaceDetachConflictError) Error() string {
	if e == nil {
		return ErrWorkspaceDetachConflict.Error()
	}
	return "workspace detach preparation was invalidated"
}

func (e *WorkspaceDetachConflictError) Is(target error) bool {
	return target == ErrWorkspaceDetachConflict
}

type WorkspaceMutationError struct {
	ProjectID   string
	WorkspaceID string
	Cause       error
}

func (e *WorkspaceMutationError) Error() string {
	if e == nil {
		return ErrWorkspaceMutationFailed.Error()
	}
	if e.Cause == nil {
		return ErrWorkspaceMutationFailed.Error()
	}
	return e.Cause.Error()
}

func (e *WorkspaceMutationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *WorkspaceMutationError) Is(target error) bool {
	return target == ErrWorkspaceMutationFailed
}
