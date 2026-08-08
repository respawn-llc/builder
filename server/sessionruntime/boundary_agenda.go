package sessionruntime

import (
	"context"
	"errors"

	"core/server/runtime"
	"core/server/session"
)

type runtimeBoundHumanExecution struct {
	start  chan struct{}
	handle ExecutionHandle
}

func (r *agentResource) RegisterRuntimeBoundHumanExecution(
	ctx context.Context,
) (runtime.RuntimeBoundHumanExecution, error) {
	r.mu.Lock()
	blocked := r.worktreeBoundary != nil || r.reducerBoundary != nil
	r.mu.Unlock()
	if blocked {
		return nil, nil
	}
	descriptor, err := session.NewOpenSessionDescriptor(r.ref.SessionID())
	if err != nil {
		return nil, err
	}
	start := make(chan struct{})
	handle, err := r.authority.StartAgentExecution(ctx, AgentExecutionRequest{
		Descriptor: descriptor,
		Resource:   CurrentAgentResource{},
		Runner: func(executionCtx context.Context, _ ExecutionScope, bridge AgentRuntimeBridge) error {
			select {
			case <-start:
			case <-executionCtx.Done():
				return context.Cause(executionCtx)
			}
			return bridge.WithEngine(executionCtx, func(engineCtx context.Context, engine *runtime.Engine) error {
				_, runErr := engine.SubmitQueuedUserMessages(engineCtx)
				return runErr
			})
		},
	})
	if errors.Is(err, ErrSessionRunActive) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &runtimeBoundHumanExecution{start: start, handle: handle}, nil
}

func (e *runtimeBoundHumanExecution) Launch(ctx context.Context) error {
	if e == nil || e.handle == nil || e.start == nil {
		return errors.New("runtime-bound human execution is uninitialized")
	}
	close(e.start)
	_, err := e.handle.Wait(ctx)
	return err
}
