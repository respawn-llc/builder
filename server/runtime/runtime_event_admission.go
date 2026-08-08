package runtime

import (
	"errors"

	"core/server/runtimecommand"
)

type runtimeEventAdmission struct {
	engine *Engine
}

func (a runtimeEventAdmission) applySteering(stepID string, intents ...steeringIntent) error {
	return a.engine.applySteeringBatch(stepID, intents...)
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
	admission := runtimeEventAdmission{engine: engine}
	// A persisted steering Engine has no Active Session Runtime generation. Its
	// caller already owns dormant Session admission.
	if engine.runtimeEvents == nil {
		return handle(admission, payload)
	}
	engine.ensureLifecycle()
	deferred, err := runtimecommand.Submit(
		engine.lifecycleCtx,
		engine.runtimeEvents,
		payload,
		func(
			_ runtimecommand.Admission,
			event Payload,
			complete func(Result, error),
		) error {
			result, handleErr := handle(admission, event)
			complete(result, handleErr)
			return nil
		},
	)
	if err != nil {
		return zero, runtimeSteeringError(err)
	}
	result, err := deferred.Await(engine.lifecycleCtx)
	return result, runtimeSteeringError(err)
}
