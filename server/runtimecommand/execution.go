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

type orderedExecutionStart struct {
	handle    sessionruntime.ExecutionHandle
	submitted <-chan sessionruntime.SubmittedTurnOutcome
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

func (a *ExecutionAdapter) RunAgentExecutionOrdered(
	ctx context.Context,
	commands *Authority,
	sessionID string,
	run func(context.Context, *runtime.Engine) error,
) error {
	if a == nil || a.authority == nil {
		return errors.New("session runtime authority is required")
	}
	if commands == nil {
		return a.RunAgentExecution(ctx, sessionID, run)
	}
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	descriptor, err := session.NewOpenSessionDescriptor(id)
	if err != nil {
		return err
	}
	ref, err := a.authority.CurrentResourceRef(ctx, id)
	if err != nil {
		return err
	}
	future, err := Enqueue(ctx, commands, SessionTarget(ref), func(turn Turn) (orderedExecutionStart, error) {
		lease, retainErr := turn.RetainExecutionLease()
		if retainErr != nil {
			return orderedExecutionStart{}, retainErr
		}
		submitted := make(chan sessionruntime.SubmittedTurnOutcome, 1)
		handle, startErr := a.authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
			Descriptor:   descriptor,
			Resource:     sessionruntime.CurrentAgentResource{},
			CommandLease: lease,
			Runner: func(executionCtx context.Context, _ sessionruntime.ExecutionScope, bridge sessionruntime.AgentRuntimeBridge) error {
				return sessionruntime.RunSubmittedAgentExecution(executionCtx, bridge, run, func(outcome sessionruntime.SubmittedTurnOutcome) {
					submitted <- outcome
				})
			},
		})
		if startErr != nil {
			_ = lease.Abort(startErr)
			_ = lease.Release()
			return orderedExecutionStart{}, startErr
		}
		return orderedExecutionStart{handle: handle, submitted: submitted}, nil
	})
	if err != nil {
		return mapExecutionStartError(err)
	}
	start, err := future.Await(ctx)
	if err != nil {
		return mapExecutionStartError(err)
	}
	select {
	case outcome := <-start.submitted:
		if outcome.Err != nil || !outcome.Continues {
			_, waitErr := start.handle.Wait(context.Background())
			return errors.Join(mapExecutionStartError(outcome.Err), mapExecutionStartError(waitErr))
		}
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func mapExecutionStartError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sessionruntime.ErrSessionStartsBlocked) {
		return errors.Join(serverapi.ErrSessionWorktreeDeleting, err)
	}
	if errors.Is(err, sessionruntime.ErrSessionRunActive) {
		return errors.Join(serverapi.ErrSessionRunStarting, err)
	}
	return err
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

func (a *ExecutionAdapter) WithLiveExecutionMutation(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	callback func(runtime.OrderedMutationTurn) error,
) error {
	if a == nil || a.authority == nil {
		return errors.New("session runtime authority is required")
	}
	handle, ok := a.authority.SessionExecution(sessionID)
	if !ok || handle.Scope().Kind() != sessionruntime.ExecutionScopeAgent {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	return a.authority.WithExecutionMutation(ctx, handle.Scope().ID(), callback)
}
