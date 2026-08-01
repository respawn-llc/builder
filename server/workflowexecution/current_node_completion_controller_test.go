package workflowexecution

import (
	"context"
	"errors"
	"testing"

	"core/server/runtimecommand"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

type completionAttemptControllerStub struct {
	completeCalls int
}

func (c *completionAttemptControllerStub) CompleteCurrentNode(context.Context, workflowruntime.CompletionRequest) (workflowruntime.CompletionResult, error) {
	c.completeCalls++
	return workflowruntime.CompletionResult{State: "applied"}, nil
}

func (*completionAttemptControllerStub) RecordProtocolViolation(context.Context, workflowruntime.ViolationRequest) (workflowruntime.ViolationResult, error) {
	return workflowruntime.ViolationResult{}, nil
}

func (*completionAttemptControllerStub) ResetProtocolViolationBudget(context.Context, workflowruntime.ViolationResetRequest) error {
	return nil
}

func (*completionAttemptControllerStub) ObserveCurrentNodeCompletion(context.Context, workflowruntime.CompletionObservationRequest) (workflowruntime.CompletionObservationResult, error) {
	return workflowruntime.CompletionObservationResult{}, nil
}

func TestCompletionAttemptControllerLeavesInputOpenUntilCompletionReenters(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	ref, err := runtimeids.NewSessionResourceRef(sessionID, 1)
	if err != nil {
		t.Fatalf("new resource ref: %v", err)
	}
	fence := runtimecommand.NewCompletionFence(runtimecommand.SessionTarget(ref))
	attempt, err := fence.Begin()
	if err != nil {
		t.Fatalf("begin completion attempt: %v", err)
	}
	scopeID := runtimeids.NewExecutionScopeID()
	base := &completionAttemptControllerStub{}
	controller := &completionAttemptWorkflowController{
		Controller: base,
		attempts: map[runtimeids.ExecutionScopeID]completionAttemptState{
			scopeID: {attempt: attempt},
		},
	}

	if _, err := fence.AcceptInput(); err != nil {
		t.Fatalf("accept input before completion re-entry: %v", err)
	}
	_, err = controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{ScopeID: scopeID})
	if !errors.Is(err, runtimecommand.ErrCompletionSuperseded) {
		t.Fatalf("completion after accepted input = %v, want ErrCompletionSuperseded", err)
	}
	if base.completeCalls != 0 {
		t.Fatalf("underlying completion calls = %d, want 0", base.completeCalls)
	}
}

func TestCompletionAttemptControllerFencesInputAfterDurableCompletion(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	ref, err := runtimeids.NewSessionResourceRef(sessionID, 1)
	if err != nil {
		t.Fatalf("new resource ref: %v", err)
	}
	fence := runtimecommand.NewCompletionFence(runtimecommand.SessionTarget(ref))
	attempt, err := fence.Begin()
	if err != nil {
		t.Fatalf("begin completion attempt: %v", err)
	}
	scopeID := runtimeids.NewExecutionScopeID()
	controller := &completionAttemptWorkflowController{
		Controller: &completionAttemptControllerStub{},
		attempts: map[runtimeids.ExecutionScopeID]completionAttemptState{
			scopeID: {attempt: attempt},
		},
	}

	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{ScopeID: scopeID}); err != nil {
		t.Fatalf("complete current node: %v", err)
	}
	if _, err := fence.BeginInput(); !errors.Is(err, runtimecommand.ErrCompletionFenced) {
		t.Fatalf("input after completion = %v, want ErrCompletionFenced", err)
	}
}
