package workflowexecution

import (
	"context"
	"testing"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func TestCurrentNodeOperationReducerAcceptsCompletionAndRetirementInEitherOrder(t *testing.T) {
	for _, test := range []struct {
		name            string
		retirementFirst bool
	}{
		{name: "retirement before finalization", retirementFirst: true},
		{name: "finalization before retirement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reference := currentNodeReferenceForControllerTest(t, "task-operation-order", "node-agent")
			key, err := reference.Key()
			if err != nil {
				t.Fatalf("Current Node key: %v", err)
			}
			sessionID := runtimeids.NewSessionID()
			operationRef := workflow.CurrentNodeOperationRef{
				OperationID: runtimeids.NewCurrentNodeOperationID(),
				CurrentNode: reference,
			}
			lease := &currentNodeAgentCapacityLease{owner: currentNodeAgentCapacityLive}
			completion := workflowstore.CurrentNodeCompletionResult{PostCompletionEligible: true}
			phase := currentNodePostTurnFinalization{
				sessionID: &sessionID, reference: reference,
			}
			controller := &CurrentNodeController{
				permit:              NewMutationPermit(),
				operations:          map[workflow.CurrentNodeReferenceKey]*currentNodeOperation{},
				agentCapacityActive: 1,
			}
			controller.operations[key] = &currentNodeOperation{
				ref: operationRef, agentCapacityLease: lease,
				completion: &completion, postTurnFinalization: &phase,
			}
			retire := func() {
				controller.WorkflowExecutionRetired(sessionruntime.WorkflowRetirementOutcome{
					Operation: operationRef, Kind: sessionruntime.ExecutionScopeAgent,
					Disposition: sessionruntime.WorkflowRetirementCompleted,
				})
			}
			finalize := func() {
				settlement, err := controller.FinalizeCurrentNodePostTurn(
					context.Background(),
					operationRef,
					sessionID,
					workflowruntime.PostCompletionRuntime{CompactionMode: "none"},
				)
				if err != nil {
					t.Fatalf("FinalizeCurrentNodePostTurn: %v", err)
				}
				if settlement.Kind != workflowruntime.PostTurnSettlementSucceeded {
					t.Fatalf("settlement = %+v, want succeeded", settlement)
				}
			}
			if test.retirementFirst {
				retire()
				if controller.agentCapacityActive != 0 || controller.operations[key] == nil {
					t.Fatalf("retirement state = capacity:%d operation:%+v", controller.agentCapacityActive, controller.operations[key])
				}
				finalize()
			} else {
				finalize()
				if controller.agentCapacityActive != 1 || controller.operations[key] == nil {
					t.Fatalf("finalization state = capacity:%d operation:%+v", controller.agentCapacityActive, controller.operations[key])
				}
				retire()
			}
			if controller.agentCapacityActive != 0 || len(controller.operations) != 0 {
				t.Fatalf("terminal reducer state = capacity:%d operations:%d", controller.agentCapacityActive, len(controller.operations))
			}
		})
	}
}

func TestCurrentNodeOperationReducerRetiresOutcomeLessWithoutRetainedState(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-operation-outcome-less", "node-agent")
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("Current Node key: %v", err)
	}
	operationRef := workflow.CurrentNodeOperationRef{
		OperationID: runtimeids.NewCurrentNodeOperationID(),
		CurrentNode: reference,
	}
	lease := &currentNodeAgentCapacityLease{owner: currentNodeAgentCapacityLive}
	controller := &CurrentNodeController{
		operations:          map[workflow.CurrentNodeReferenceKey]*currentNodeOperation{},
		agentCapacityActive: 1,
		interrupts:          newCurrentNodeInterruptState(),
	}
	controller.operations[key] = &currentNodeOperation{
		ref: operationRef, agentCapacityLease: lease,
	}
	fence, err := controller.interrupts.beginTask(reference.TaskID)
	if err != nil {
		t.Fatalf("begin Task interruption: %v", err)
	}
	controller.interrupts.addOperation(fence, operationRef.OperationID)
	controller.WorkflowExecutionRetired(sessionruntime.WorkflowRetirementOutcome{
		Operation: operationRef, Kind: sessionruntime.ExecutionScopeAgent,
		Disposition: sessionruntime.WorkflowRetirementOutcomeLess,
	})
	if controller.agentCapacityActive != 0 ||
		len(controller.operations) != 0 ||
		controller.interrupts.taskActive(reference.TaskID) {
		t.Fatalf(
			"outcome-less reducer state = capacity:%d operations:%d task_fenced:%t",
			controller.agentCapacityActive,
			len(controller.operations),
			controller.interrupts.taskActive(reference.TaskID),
		)
	}
}

func TestCurrentNodeControllerCloseDisposesOperationsAndLateFinalization(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-operation-close", "node-agent")
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("Current Node key: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(
		t,
		&currentNodeControllerStore{},
		&countingCurrentNodeRunner{},
		authority,
		1,
	)
	operationRef := workflow.CurrentNodeOperationRef{
		OperationID: runtimeids.NewCurrentNodeOperationID(),
		CurrentNode: reference,
	}
	sessionID := runtimeids.NewSessionID()
	lease := &currentNodeAgentCapacityLease{owner: currentNodeAgentCapacityLive}
	completion := workflowstore.CurrentNodeCompletionResult{PostCompletionEligible: true}
	phase := currentNodePostTurnFinalization{sessionID: &sessionID, reference: reference}
	controller.mu.Lock()
	controller.operations[key] = &currentNodeOperation{
		ref: operationRef, agentCapacityLease: lease,
		completion: &completion, postTurnFinalization: &phase,
	}
	controller.agentCapacityActive = 1
	controller.mu.Unlock()

	if err := controller.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	controller.mu.Lock()
	operationCount := len(controller.operations)
	capacity := controller.agentCapacityActive
	controller.mu.Unlock()
	if operationCount != 0 || capacity != 0 {
		t.Fatalf("closed controller state = operations:%d capacity:%d", operationCount, capacity)
	}
	settlement, err := controller.FinalizeCurrentNodePostTurn(
		context.Background(),
		operationRef,
		sessionID,
		workflowruntime.PostCompletionRuntime{CompactionMode: "none"},
	)
	if err != nil {
		t.Fatalf("late FinalizeCurrentNodePostTurn: %v", err)
	}
	if settlement.Kind != workflowruntime.PostTurnSettlementShutdownDisposed ||
		settlement.DiagnosticOwner != workflowruntime.DiagnosticOwnerControllerShutdown {
		t.Fatalf("late finalization settlement = %+v, want shutdown_disposed", settlement)
	}
	if err := authority.Close(context.Background()); err != nil {
		t.Fatalf("close Authority: %v", err)
	}
}
