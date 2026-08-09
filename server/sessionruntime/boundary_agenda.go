package sessionruntime

import (
	"context"
	"errors"

	"core/server/runtime"
	"core/server/runtimecommand"
	"core/server/session"
	"core/shared/runtimeids"
)

type runtimeBoundExecution struct {
	work    chan func(context.Context, *runtime.Engine) error
	scopeID runtimeids.ExecutionScopeID
}

func (r *agentResource) RegisterRuntimeBoundExecution(
	admission runtimecommand.Admission,
) (runtime.RuntimeBoundExecution, error) {
	if !admission.Owns(r.events) {
		return nil, errors.New("runtime-bound execution requires its Resource Generation admission")
	}
	descriptor, err := session.NewOpenSessionDescriptor(r.ref.SessionID())
	if err != nil {
		return nil, err
	}
	work := make(chan func(context.Context, *runtime.Engine) error, 1)
	request := AgentExecutionRequest{
		Descriptor: descriptor,
		Resource:   CurrentAgentResource{},
		Runner: func(
			executionCtx context.Context,
			_ ExecutionScope,
			bridge AgentRuntimeBridge,
		) error {
			select {
			case run := <-work:
				return bridge.WithEngine(executionCtx, run)
			case <-executionCtx.Done():
				return context.Cause(executionCtx)
			}
		},
	}
	var execution *execution
	if err := admission.StartWork(func(context.Context) {
		if execution == nil {
			return
		}
		launchErr := r.engine.LaunchAgentExecution(
			func(workCtx context.Context) error {
				return r.authority.runAgentExecution(workCtx, execution, request)
			},
			func(runErr error) {
				execution.finish(ExecutionResult{}, runErr, nil)
			},
		)
		if launchErr != nil {
			execution.finish(ExecutionResult{}, launchErr, nil)
		}
	}); err != nil {
		return nil, err
	}
	r.mu.Lock()
	reducerBoundary := r.reducerBoundary
	r.mu.Unlock()
	if reducerBoundary == nil {
		return nil, errors.New("runtime-bound execution requires active idle Boundary ownership")
	}
	execution, err = r.authority.admitAgentExecutionStart(agentExecutionStart{
		request:  request,
		resource: r,
	}, reducerBoundary)
	if errors.Is(err, ErrSessionRunActive) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &runtimeBoundExecution{work: work, scopeID: execution.scope.ID()}, nil
}

func (e *runtimeBoundExecution) Start(
	work func(context.Context, *runtime.Engine) error,
) runtimeids.ExecutionScopeID {
	if e == nil || e.scopeID.IsZero() || e.work == nil || work == nil {
		panic("runtime-bound execution is uninitialized")
	}
	e.work <- work
	return e.scopeID
}
