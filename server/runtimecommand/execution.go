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

type AgentExecutionAdmission struct {
	CallbackEntered bool
	Err             error
}

func NewExecutionAdapter(authority *sessionruntime.Authority) *ExecutionAdapter {
	return &ExecutionAdapter{authority: authority}
}

func (a *ExecutionAdapter) RunAgentExecution(
	ctx context.Context,
	sessionID string,
	run func(context.Context, *runtime.Engine) error,
) error {
	return a.RunAgentExecutionAdmission(ctx, sessionID, run).Err
}

func (a *ExecutionAdapter) RunAgentExecutionAdmission(
	ctx context.Context,
	sessionID string,
	run func(context.Context, *runtime.Engine) error,
) AgentExecutionAdmission {
	if a == nil || a.authority == nil {
		return AgentExecutionAdmission{Err: errors.New("session runtime authority is required")}
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return AgentExecutionAdmission{Err: err}
	}
	descriptor, err := session.NewOpenSessionDescriptor(id)
	if err != nil {
		return AgentExecutionAdmission{Err: err}
	}
	entered := false
	err = a.authority.RunCurrentAgentExecution(ctx, descriptor, func(ctx context.Context, engine *runtime.Engine) error {
		entered = true
		return run(ctx, engine)
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionStartsBlocked) {
			err = errors.Join(serverapi.ErrSessionWorktreeDeleting, err)
		}
		if errors.Is(err, sessionruntime.ErrSessionRunActive) {
			err = errors.Join(serverapi.ErrSessionRunStarting, err)
		}
	}
	return AgentExecutionAdmission{CallbackEntered: entered, Err: err}
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
