package sessionruntime

import (
	"context"
	"errors"
	"sync"

	"core/server/runtime"
	"core/server/runtimecommand"
	"core/server/session"
)

type runtimeBoundExecutionHandoff struct {
	run   func(context.Context, *runtime.Engine) error
	abort func(error)

	once     sync.Once
	terminal error
}

func (r *agentResource) LaunchRuntimeBoundExecution(
	admission runtimecommand.Admission,
	work func(context.Context, *runtime.Engine) error,
	abort func(error),
) error {
	if !admission.Owns(r.events) {
		return errors.New(
			"runtime-bound execution requires its Resource Generation admission",
		)
	}
	if work == nil || abort == nil {
		return errors.New(
			"runtime-bound execution requires work and abort callbacks",
		)
	}
	descriptor, err := session.NewOpenSessionDescriptor(r.ref.SessionID())
	if err != nil {
		return err
	}
	handoff := &runtimeBoundExecutionHandoff{
		run:   work,
		abort: abort,
	}
	request := AgentExecutionRequest{
		Descriptor: descriptor,
		Resource:   CurrentAgentResource{},
		Runner: func(
			executionCtx context.Context,
			_ ExecutionScope,
			bridge AgentRuntimeBridge,
		) error {
			return handoff.execute(executionCtx, bridge)
		},
	}
	var execution *execution
	r.mu.Lock()
	reducerBoundary := r.reducerBoundary
	reducerBoundaryActive := reducerBoundary != nil &&
		reducerBoundary.phase == reducerBoundaryActive
	r.mu.Unlock()
	if !reducerBoundaryActive {
		return errors.New(
			"runtime-bound execution requires active idle Boundary ownership",
		)
	}
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
			handoff.fail(launchErr)
			execution.finish(ExecutionResult{}, launchErr, nil)
		}
	}); err != nil {
		return err
	}
	execution, err = r.authority.admitAgentExecutionStart(agentExecutionStart{
		request:  request,
		resource: r,
	}, reducerBoundary)
	if err != nil {
		return err
	}
	return nil
}

func (h *runtimeBoundExecutionHandoff) execute(
	executionCtx context.Context,
	bridge AgentRuntimeBridge,
) error {
	if cause := context.Cause(executionCtx); cause != nil {
		h.fail(cause)
		return cause
	}
	invoked := false
	runErr := bridge.WithEngine(executionCtx, func(
		workCtx context.Context,
		engine *runtime.Engine,
	) error {
		invoked = true
		h.once.Do(func() {
			if cause := context.Cause(workCtx); cause != nil {
				h.terminal = cause
				h.abort(cause)
				return
			}
			h.terminal = h.run(workCtx, engine)
		})
		return h.terminal
	})
	if !invoked {
		h.fail(runErr)
	}
	return runErr
}

func (h *runtimeBoundExecutionHandoff) fail(cause error) {
	if cause == nil {
		cause = errors.New("runtime-bound execution handoff failed")
	}
	h.once.Do(func() {
		h.terminal = cause
		h.abort(cause)
	})
}
