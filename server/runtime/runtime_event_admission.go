package runtime

import (
	"context"
	"errors"

	"core/server/runtimecommand"
)

type runtimeEventAdmission struct {
	engine  *Engine
	command runtimecommand.Admission
}

func (a runtimeEventAdmission) applySteering(stepID string, intents ...steeringIntent) error {
	return a.engine.applySteeringBatch(stepID, intents...)
}

func (a runtimeEventAdmission) applySteeringOptional(stepID *string, intents ...steeringIntent) error {
	if stepID == nil {
		return a.applySteering("", intents...)
	}
	return a.applySteering(*stepID, intents...)
}

func (a runtimeEventAdmission) Context() context.Context {
	if a.command.Context() != nil {
		return a.command.Context()
	}
	if a.engine != nil && a.engine.lifecycleCtx != nil {
		return a.engine.lifecycleCtx
	}
	return context.Background()
}

func (a runtimeEventAdmission) startWork(work func(context.Context)) error {
	return a.command.StartWork(work)
}

func submitRuntimeEvent[Payload, Result any](
	engine *Engine,
	payload Payload,
	handle func(runtimeEventAdmission, Payload) (Result, error),
) (Result, error) {
	var zero Result
	if engine == nil {
		return zero, errors.New("runtime engine is required")
	}
	if engine.closed.Load() {
		return zero, ErrEngineClosed
	}
	engine.ensureLifecycle()
	return submitRuntimeEventWithContext(
		engine.lifecycleCtx,
		engine.lifecycleCtx,
		engine,
		payload,
		handle,
	)
}

func submitRuntimeEventWithContext[Payload, Result any](
	admissionContext context.Context,
	waitContext context.Context,
	engine *Engine,
	payload Payload,
	handle func(runtimeEventAdmission, Payload) (Result, error),
) (Result, error) {
	var zero Result
	if engine == nil {
		return zero, errors.New("runtime engine is required")
	}
	admission := runtimeEventAdmission{engine: engine}
	// A persisted steering Engine has no Active Session Runtime generation. Its
	// caller already owns dormant Session admission.
	if engine.runtimeEvents == nil {
		return handle(admission, payload)
	}
	deferred, err := runtimecommand.Submit(
		admissionContext,
		engine.runtimeEvents,
		payload,
		func(
			command runtimecommand.Admission,
			event Payload,
			complete func(Result, error),
		) error {
			admission.command = command
			result, handleErr := handle(admission, event)
			complete(result, handleErr)
			return nil
		},
	)
	if err != nil {
		return zero, runtimeSteeringError(err)
	}
	result, err := deferred.Await(waitContext)
	return result, runtimeSteeringError(err)
}

func submitRuntimeEventWork[Request, WorkResult, Application, Result any](
	admissionContext context.Context,
	workContext context.Context,
	waitContext context.Context,
	engine *Engine,
	request Request,
	start func(runtimeEventAdmission, Request) error,
	run func(context.Context, Request) WorkResult,
	apply func(runtimeEventAdmission, Request, WorkResult) (Application, error),
	settle func(WorkResult, Application, error) (Result, error),
) (Result, error) {
	var zero Result
	if engine == nil {
		return zero, errors.New("runtime engine is required")
	}
	deferred, err := runtimecommand.Submit(
		admissionContext,
		engine.runtimeEvents,
		request,
		func(
			command runtimecommand.Admission,
			accepted Request,
			complete func(Result, error),
		) error {
			admission := runtimeEventAdmission{engine: engine, command: command}
			if start != nil {
				if startErr := start(admission, accepted); startErr != nil {
					return startErr
				}
			}
			return admission.startWork(func(runtimeContext context.Context) {
				runContext, cancelRun := context.WithCancelCause(runtimeContext)
				stopWorkCancellation := context.AfterFunc(workContext, func() {
					cancelRun(context.Cause(workContext))
				})
				if cause := context.Cause(workContext); cause != nil {
					cancelRun(cause)
				}
				defer stopWorkCancellation()
				defer cancelRun(nil)

				workResult := run(runContext, accepted)
				application, resultErr := submitRuntimeEventWithContext(
					engine.lifecycleCtx,
					engine.lifecycleCtx,
					engine,
					workResult,
					func(
						resultAdmission runtimeEventAdmission,
						completed WorkResult,
					) (Application, error) {
						return apply(resultAdmission, accepted, completed)
					},
				)
				result, settleErr := settle(
					workResult,
					application,
					resultErr,
				)
				complete(result, settleErr)
			})
		},
	)
	if err != nil {
		return zero, runtimeSteeringError(err)
	}
	result, err := deferred.Await(waitContext)
	return result, runtimeSteeringError(err)
}
