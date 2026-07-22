package runtimecommand

import (
	"context"
	"errors"
	"strings"
	"sync"

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
	operationContinues := make(chan bool, 1)
	handle, err := a.authority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Resource:   sessionruntime.CurrentAgentResource{},
		Runner: func(executionCtx context.Context, _ sessionruntime.ExecutionScope, bridge sessionruntime.AgentRuntimeBridge) error {
			callbackRan := false
			runErr := bridge.WithEngine(executionCtx, func(_ context.Context, engine *runtime.Engine) error {
				callbackRan = true
				runCtx, stop := MergeContexts(executionCtx, ctx)
				err := run(runCtx, engine)
				stop()
				goalLoopActive := err == nil && engine.GoalLoopRunning()
				operationContinues <- goalLoopActive
				if err != nil || !goalLoopActive {
					return err
				}
				return engine.WaitForGoalLoop(executionCtx)
			})
			if !callbackRan {
				operationContinues <- false
			}
			return runErr
		},
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionStartsBlocked) {
			return errors.Join(serverapi.ErrSessionWorktreeDeleting, err)
		}
		if errors.Is(err, sessionruntime.ErrSessionRunActive) {
			return errors.Join(serverapi.ErrSessionRunStarting, err)
		}
		return err
	}
	if <-operationContinues {
		return nil
	}
	_, err = handle.Wait(context.Background())
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
	execution, ok := a.authority.SessionExecution(sessionID)
	if !ok {
		err := a.authority.WithCurrentRuntime(ctx, sessionID, func(context.Context, *runtime.Engine) error {
			return nil
		})
		if err != nil {
			return err
		}
		return serverapi.ErrRuntimeNoActiveRun
	}
	resource, ok := execution.Scope().Resource()
	if !ok {
		return errors.New("agent execution scope has no runtime resource")
	}
	return a.authority.WithRuntime(ctx, resource, callback)
}

func MergeContexts(contexts ...context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	stop := func() { once.Do(cancel) }
	for _, source := range contexts {
		if source == nil {
			continue
		}
		if err := source.Err(); err != nil {
			stop()
			continue
		}
		done := source.Done()
		if done == nil {
			continue
		}
		go func() {
			select {
			case <-done:
				stop()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, stop
}
