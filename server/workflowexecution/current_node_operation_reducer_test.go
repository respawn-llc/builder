package workflowexecution

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func TestCurrentNodeOperationReducerSettlesEveryPostTurnOutcomeInEitherOrder(t *testing.T) {
	diagnostic := errors.New("post-turn optimization failed")
	for _, outcome := range []struct {
		name           string
		classification workflow.SessionReuseClassification
		runtime        func(context.Context) workflowruntime.PostCompletionRuntime
		cancel         bool
		want           workflowruntime.PostTurnSettlementKind
	}{
		{
			name: "success",
			runtime: func(context.Context) workflowruntime.PostCompletionRuntime {
				return workflowruntime.PostCompletionRuntime{CompactionMode: "none"}
			},
			want: workflowruntime.PostTurnSettlementSucceeded,
		},
		{
			name:           "non-cancellation diagnostic",
			classification: workflow.SessionReuseGuaranteedCACReuse,
			runtime: func(context.Context) workflowruntime.PostCompletionRuntime {
				return workflowruntime.PostCompletionRuntime{
					CompactionMode: "local",
					Compact: func(context.Context) workflowruntime.PostCompletionCompactionResult {
						return workflowruntime.PostCompletionCompactionResult{Diagnostic: diagnostic}
					},
				}
			},
			want: workflowruntime.PostTurnSettlementCompletedWithDiagnostic,
		},
		{
			name:           "cancellation before compaction commit",
			classification: workflow.SessionReuseGuaranteedCACReuse,
			cancel:         true,
			runtime: func(ctx context.Context) workflowruntime.PostCompletionRuntime {
				return workflowruntime.PostCompletionRuntime{
					CompactionMode: "local",
					Compact: func(context.Context) workflowruntime.PostCompletionCompactionResult {
						return workflowruntime.PostCompletionCompactionResult{Diagnostic: context.Cause(ctx)}
					},
				}
			},
			want: workflowruntime.PostTurnSettlementAborted,
		},
		{
			name:           "cancellation after compaction commit",
			classification: workflow.SessionReuseGuaranteedCACReuse,
			cancel:         true,
			runtime: func(context.Context) workflowruntime.PostCompletionRuntime {
				return workflowruntime.PostCompletionRuntime{
					CompactionMode: "local",
					Compact: func(context.Context) workflowruntime.PostCompletionCompactionResult {
						return workflowruntime.PostCompletionCompactionResult{
							CommitReceipt: session.CommitReceipt{Committed: true},
						}
					},
				}
			},
			want: workflowruntime.PostTurnSettlementCompletedWithDiagnostic,
		},
		{
			name:           "invalid configuration",
			classification: workflow.SessionReuseThresholdPossibleReuse,
			runtime: func(context.Context) workflowruntime.PostCompletionRuntime {
				return workflowruntime.PostCompletionRuntime{CompactionMode: "local"}
			},
			want: workflowruntime.PostTurnSettlementAborted,
		},
	} {
		for _, retirementFirst := range []bool{true, false} {
			order := "finalization_before_retirement"
			if retirementFirst {
				order = "retirement_before_finalization"
			}
			t.Run(outcome.name+"/"+order, func(t *testing.T) {
				controller, operationRef, sessionID, key := newReducerPostTurnOperationForTest(
					t,
					outcome.classification,
				)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if outcome.cancel {
					cancel()
				}
				retire := func() {
					controller.WorkflowExecutionRetired(sessionruntime.WorkflowRetirementOutcome{
						Operation:   operationRef,
						Kind:        sessionruntime.ExecutionScopeAgent,
						Disposition: sessionruntime.WorkflowRetirementCompleted,
					})
				}
				finalize := func() {
					settlement, err := controller.FinalizeCurrentNodePostTurn(
						ctx,
						operationRef,
						sessionID,
						outcome.runtime(ctx),
					)
					if err != nil {
						t.Fatalf("FinalizeCurrentNodePostTurn: %v", err)
					}
					if settlement.Kind != outcome.want {
						t.Fatalf("settlement = %+v, want %s", settlement, outcome.want)
					}
				}
				if retirementFirst {
					retire()
					finalize()
				} else {
					finalize()
					retire()
				}
				if controller.agentCapacityActive != 0 || controller.operations[key] != nil {
					t.Fatalf(
						"terminal reducer state = capacity:%d operation:%+v",
						controller.agentCapacityActive,
						controller.operations[key],
					)
				}
			})
		}
	}
}

func TestCurrentNodeOperationReducerCloseWinsRunningPostTurnOptimization(t *testing.T) {
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(
		t,
		&currentNodeControllerStore{},
		&countingCurrentNodeRunner{},
		authority,
		1,
	)
	operationRef, sessionID, key := installReducerPostTurnOperationForTest(
		t,
		controller,
		workflow.SessionReuseGuaranteedCACReuse,
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	type finalizationResult struct {
		settlement workflowruntime.PostTurnSettlement
		err        error
	}
	done := make(chan finalizationResult, 1)
	go func() {
		settlement, err := controller.FinalizeCurrentNodePostTurn(
			context.Background(),
			operationRef,
			sessionID,
			workflowruntime.PostCompletionRuntime{
				CompactionMode: "local",
				Compact: func(context.Context) workflowruntime.PostCompletionCompactionResult {
					close(entered)
					<-release
					return workflowruntime.PostCompletionCompactionResult{
						CommitReceipt: session.CommitReceipt{Committed: true},
					}
				},
			},
		)
		done <- finalizationResult{settlement: settlement, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("post-turn optimization did not start")
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(release)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("late finalization: %v", result.err)
		}
		if result.settlement.Kind != workflowruntime.PostTurnSettlementShutdownDisposed ||
			result.settlement.DiagnosticOwner != workflowruntime.DiagnosticOwnerControllerShutdown {
			t.Fatalf("late settlement = %+v, want shutdown_disposed", result.settlement)
		}
	case <-time.After(time.Second):
		t.Fatal("post-Close finalization did not return")
	}
	if controller.agentCapacityActive != 0 || controller.operations[key] != nil {
		t.Fatalf(
			"closed reducer state = capacity:%d operation:%+v",
			controller.agentCapacityActive,
			controller.operations[key],
		)
	}
	if err := authority.Close(context.Background()); err != nil {
		t.Fatalf("close Authority: %v", err)
	}
}

func newReducerPostTurnOperationForTest(
	t *testing.T,
	classification workflow.SessionReuseClassification,
) (*CurrentNodeController, workflow.CurrentNodeOperationRef, runtimeids.SessionID, workflow.CurrentNodeReferenceKey) {
	t.Helper()
	controller := &CurrentNodeController{
		permit:              NewMutationPermit(),
		operations:          make(map[workflow.CurrentNodeReferenceKey]*currentNodeOperation),
		agentCapacityActive: 1,
	}
	operationRef, sessionID, key := installReducerPostTurnOperationForTest(t, controller, classification)
	return controller, operationRef, sessionID, key
}

func installReducerPostTurnOperationForTest(
	t *testing.T,
	controller *CurrentNodeController,
	classification workflow.SessionReuseClassification,
) (workflow.CurrentNodeOperationRef, runtimeids.SessionID, workflow.CurrentNodeReferenceKey) {
	t.Helper()
	reference := currentNodeReferenceForControllerTest(t, "task-post-turn-outcome", "node-agent")
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("Current Node key: %v", err)
	}
	sessionID := runtimeids.NewSessionID()
	operationRef := workflow.CurrentNodeOperationRef{
		OperationID: runtimeids.NewCurrentNodeOperationID(),
		CurrentNode: reference,
	}
	completion := workflowstore.CurrentNodeCompletionResult{PostCompletionEligible: true}
	phase := currentNodePostTurnFinalization{
		sessionID:      &sessionID,
		classification: classification,
		reference:      reference,
	}
	controller.mu.Lock()
	controller.operations[key] = &currentNodeOperation{
		ref:                  operationRef,
		agentCapacityLease:   &currentNodeAgentCapacityLease{owner: currentNodeAgentCapacityLive},
		completion:           &completion,
		postTurnFinalization: &phase,
	}
	controller.agentCapacityActive = 1
	controller.mu.Unlock()
	return operationRef, sessionID, key
}

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
