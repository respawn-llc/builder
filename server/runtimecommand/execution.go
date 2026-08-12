package runtimecommand

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type ExecutionAdapter struct {
	authority *sessionruntime.Authority
}

func NewExecutionAdapter(authority *sessionruntime.Authority) *ExecutionAdapter {
	return &ExecutionAdapter{authority: authority}
}

func (a *ExecutionAdapter) RunAgentExecution(
	ctx context.Context,
	sessionID string,
	run func(context.Context, *runtime.Engine) error,
) error {
	if a == nil || a.authority == nil {
		return errors.New("session runtime authority is required")
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	descriptor, err := session.NewOpenSessionDescriptor(id)
	if err != nil {
		return err
	}
	err = a.authority.RunCurrentAgentExecution(ctx, descriptor, run)
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionStartsBlocked) {
			return errors.Join(serverapi.ErrSessionWorktreeDeleting, err)
		}
		if errors.Is(err, sessionruntime.ErrSessionRunActive) {
			return errors.Join(serverapi.ErrSessionRunStarting, err)
		}
		return err
	}
	return nil
}

func (a *ExecutionAdapter) WithLiveExecutionRuntime(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	callback func(context.Context, *runtime.Engine) error,
) error {
	if a == nil || a.authority == nil {
		return errors.New("session runtime authority is required")
	}
	return a.authority.WithLiveExecutionRuntime(ctx, sessionID, callback)
}

func (a *ExecutionAdapter) WithRetainedWorkflowRuntime(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	callback func(context.Context, *runtime.Engine) error,
) error {
	if a == nil || a.authority == nil {
		return errors.New("session runtime authority is required")
	}
	return a.authority.WithRetainedWorkflowRuntime(ctx, sessionID, callback)
}
