package workflowrunner

import (
	"context"
	"errors"
	"testing"

	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

type scriptCompletionFailureController struct {
	completionErr error
	failedScope   runtimeids.ExecutionScopeID
	failedReason  workflow.CurrentNodeInterruptionReason
	failedCause   error
}

func (c *scriptCompletionFailureController) CompleteCurrentNode(
	context.Context,
	workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	return workflowruntime.CompletionResult{}, c.completionErr
}

func (c *scriptCompletionFailureController) RecordProtocolViolation(
	context.Context,
	workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	return workflowruntime.ViolationResult{}, nil
}

func (c *scriptCompletionFailureController) ResetProtocolViolationBudget(
	context.Context,
	workflowruntime.ViolationResetRequest,
) error {
	return nil
}

func (c *scriptCompletionFailureController) ObserveCurrentNodeCompletion(
	context.Context,
	workflowruntime.CompletionObservationRequest,
) (workflowruntime.CompletionObservationResult, error) {
	return workflowruntime.CompletionObservationResult{}, nil
}

func (c *scriptCompletionFailureController) FailCurrentNodeScope(
	_ context.Context,
	scopeID runtimeids.ExecutionScopeID,
	reason workflow.CurrentNodeInterruptionReason,
	cause error,
) error {
	c.failedScope = scopeID
	c.failedReason = reason
	c.failedCause = cause
	return nil
}

func TestScriptCompletionFailureInterruptsExactCurrentNodeScope(t *testing.T) {
	completionErr := errors.New("complete current node")
	controller := &scriptCompletionFailureController{completionErr: completionErr}
	scopeID := runtimeids.NewExecutionScopeID()

	err := (&Starter{}).completeCurrentNodeScript(
		context.Background(),
		controller,
		workflowruntime.CompletionRequest{ScopeID: scopeID},
	)

	if !errors.Is(err, completionErr) {
		t.Fatalf("completion error = %v, want %v", err, completionErr)
	}
	if controller.failedScope != scopeID {
		t.Fatalf("failed scope = %s, want %s", controller.failedScope, scopeID)
	}
	if controller.failedReason != workflow.CurrentNodeInterruptionReason(ReasonScriptCompletionFailed) {
		t.Fatalf("failed reason = %q, want %q", controller.failedReason, ReasonScriptCompletionFailed)
	}
	if !errors.Is(controller.failedCause, completionErr) {
		t.Fatalf("failed cause = %v, want %v", controller.failedCause, completionErr)
	}
}
