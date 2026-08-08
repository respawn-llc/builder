package sessionruntime

import (
	"context"
	"errors"

	"core/server/runtime"
	"core/server/session"
	"core/shared/runtimeids"
)

type runtimeBoundHumanExecution struct {
	start  chan struct{}
	handle ExecutionHandle
}

type runtimeBoundLongExecution struct {
	start  chan struct{}
	work   chan func(context.Context, *runtime.Engine) error
	handle ExecutionHandle
}

func (r *agentResource) RegisterRuntimeBoundHumanExecution(
	ctx context.Context,
) (runtime.RuntimeBoundHumanExecution, error) {
	start := make(chan struct{})
	handle, err := r.startRuntimeBoundExecution(
		ctx,
		func(executionCtx context.Context, bridge AgentRuntimeBridge) error {
			select {
			case <-start:
			case <-executionCtx.Done():
				return context.Cause(executionCtx)
			}
			return bridge.WithEngine(
				executionCtx,
				func(engineCtx context.Context, engine *runtime.Engine) error {
					_, runErr := engine.SubmitQueuedUserMessages(engineCtx)
					return runErr
				},
			)
		},
	)
	if errors.Is(err, ErrSessionRunActive) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &runtimeBoundHumanExecution{start: start, handle: handle}, nil
}

func (r *agentResource) RegisterRuntimeBoundLongExecution(
	ctx context.Context,
) (runtime.RuntimeBoundLongExecution, error) {
	start := make(chan struct{})
	work := make(chan func(context.Context, *runtime.Engine) error, 1)
	handle, err := r.startRuntimeBoundExecution(
		ctx,
		func(executionCtx context.Context, bridge AgentRuntimeBridge) error {
			select {
			case <-start:
			case <-executionCtx.Done():
				return context.Cause(executionCtx)
			}
			run := <-work
			return bridge.WithEngine(
				executionCtx,
				func(engineCtx context.Context, engine *runtime.Engine) error {
					return run(engineCtx, engine)
				},
			)
		},
	)
	if errors.Is(err, ErrSessionRunActive) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &runtimeBoundLongExecution{
		start:  start,
		work:   work,
		handle: handle,
	}, nil
}

func (r *agentResource) startRuntimeBoundExecution(
	ctx context.Context,
	run func(context.Context, AgentRuntimeBridge) error,
) (ExecutionHandle, error) {
	descriptor, err := session.NewOpenSessionDescriptor(r.ref.SessionID())
	if err != nil {
		return nil, err
	}
	return r.authority.StartAgentExecution(ctx, AgentExecutionRequest{
		Descriptor: descriptor,
		Resource:   CurrentAgentResource{},
		Runner: func(
			executionCtx context.Context,
			_ ExecutionScope,
			bridge AgentRuntimeBridge,
		) error {
			return run(executionCtx, bridge)
		},
	})
}

func (e *runtimeBoundHumanExecution) Launch(ctx context.Context) error {
	if e == nil || e.handle == nil || e.start == nil {
		return errors.New("runtime-bound human execution is uninitialized")
	}
	close(e.start)
	_, err := e.handle.Wait(ctx)
	return err
}

func (e *runtimeBoundLongExecution) Launch(
	ctx context.Context,
	work func(context.Context, *runtime.Engine) error,
) (runtimeids.ExecutionScopeID, error) {
	if e == nil || e.handle == nil || e.start == nil || e.work == nil || work == nil {
		return runtimeids.ExecutionScopeID{}, errors.New("runtime-bound long execution is uninitialized")
	}
	scopeID := e.handle.Scope().ID()
	e.work <- work
	close(e.start)
	_, err := e.handle.Wait(ctx)
	return scopeID, err
}

func (e *runtimeBoundLongExecution) Cancel(ctx context.Context) error {
	if e == nil || e.handle == nil {
		return errors.New("runtime-bound long execution is uninitialized")
	}
	e.handle.RequestStop()
	_, err := e.handle.Wait(ctx)
	return err
}
