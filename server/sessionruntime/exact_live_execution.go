package sessionruntime

import (
	"context"
	"errors"
	"fmt"

	"core/server/runtime"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type exactCallbackAdmissionPhase uint8

const (
	exactCallbackAdmissionOpen exactCallbackAdmissionPhase = iota + 1
	exactCallbackAdmissionClosing
)

type liveExecutionCapture struct {
	execution           *execution
	scopeID             runtimeids.ExecutionScopeID
	executionGeneration ExecutionGeneration
	resource            runtimeids.SessionResourceRef
	resourceGeneration  runtimeids.ResourceGeneration
}

func (a *Authority) captureLiveExecution(sessionID runtimeids.SessionID) (liveExecutionCapture, error) {
	if a == nil {
		return liveExecutionCapture{}, errors.New("session runtime authority is required")
	}
	if sessionID.IsZero() {
		return liveExecutionCapture{}, errors.New("session id is required")
	}
	a.mu.Lock()
	resource := a.resources[sessionID]
	if resource == nil {
		a.mu.Unlock()
		return liveExecutionCapture{}, errors.Join(
			serverapi.ErrRuntimeUnavailable,
			fmt.Errorf("session %s has no active runtime available", sessionID),
		)
	}
	resource.mu.Lock()
	execution := resource.current
	if execution == nil {
		resource.mu.Unlock()
		a.mu.Unlock()
		return liveExecutionCapture{}, serverapi.ErrRuntimeNoActiveRun
	}
	capture, err := a.captureForExecutionLocked(execution, resource)
	resource.mu.Unlock()
	a.mu.Unlock()
	return capture, err
}

func (a *Authority) captureForExecutionLocked(execution *execution, resource *agentResource) (liveExecutionCapture, error) {
	if execution == nil || resource == nil ||
		execution.authority != a ||
		a.byScope[execution.scope.ID()] != execution ||
		execution.scope.Kind() != ExecutionScopeAgent ||
		execution.phase != executionPhaseRunning ||
		resource.current != execution ||
		resource.state != AgentResourceReady ||
		execution.exactCallbackPhase != exactCallbackAdmissionOpen {
		return liveExecutionCapture{}, ErrExecutionNoLongerLive
	}
	resourceRef, ok := execution.scope.Resource()
	if !ok || resourceRef != resource.ref {
		return liveExecutionCapture{}, ErrExecutionNoLongerLive
	}
	return liveExecutionCapture{
		execution:           execution,
		scopeID:             execution.scope.ID(),
		executionGeneration: execution.scope.ExecutionGeneration(),
		resource:            resource.ref,
		resourceGeneration:  resource.ref.Generation(),
	}, nil
}

func (a *Authority) admitLiveExecution(
	ctx context.Context,
	capture liveExecutionCapture,
	callback func(context.Context, *runtime.Engine) error,
) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if callback == nil {
		return errors.New("live execution callback is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	a.mu.Lock()
	execution := capture.execution
	if execution == nil ||
		execution.authority != a ||
		a.byScope[capture.scopeID] != execution ||
		execution.scope.ID() != capture.scopeID ||
		execution.scope.ExecutionGeneration() != capture.executionGeneration ||
		execution.scope.Kind() != ExecutionScopeAgent ||
		execution.phase != executionPhaseRunning {
		a.mu.Unlock()
		return ErrExecutionNoLongerLive
	}
	resource := execution.resource
	if resource == nil {
		a.mu.Unlock()
		return ErrExecutionNoLongerLive
	}
	resource.mu.Lock()
	if resource.current != execution ||
		resource.ref != capture.resource ||
		resource.ref.Generation() != capture.resourceGeneration ||
		resource.state != AgentResourceReady ||
		execution.exactCallbackPhase != exactCallbackAdmissionOpen ||
		context.Cause(execution.ctx) != nil ||
		resource.engine == nil {
		resource.mu.Unlock()
		a.mu.Unlock()
		return ErrExecutionNoLongerLive
	}
	engine := resource.engine
	execution.exactCallbacks++
	resource.callbacks++
	resource.signalLocked()
	resource.mu.Unlock()
	a.mu.Unlock()

	runCtx, stop := MergeContexts(ctx, execution.ctx)
	defer stop()
	defer a.releaseLiveExecution(execution)
	return callback(runCtx, engine)
}

func (a *Authority) releaseLiveExecution(execution *execution) error {
	if execution == nil || execution.resource == nil {
		return nil
	}
	resource := execution.resource
	resource.mu.Lock()
	if execution.exactCallbacks <= 0 {
		resource.mu.Unlock()
		panic(fmt.Sprintf("agent execution scope %s exact callback underflow", execution.scope.ID()))
	}
	if resource.callbacks <= 0 {
		resource.mu.Unlock()
		panic(fmt.Sprintf("agent resource %s generation %d callback underflow", resource.ref.SessionID(), resource.ref.Generation()))
	}
	execution.exactCallbacks--
	resource.callbacks--
	resource.signalLocked()
	resource.mu.Unlock()
	return a.closeRetiringResource(context.Background(), resource)
}
