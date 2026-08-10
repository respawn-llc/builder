package workflowstore

import (
	"context"
	"errors"
	"testing"

	"core/server/workflow"
)

func TestInterruptCurrentNodeSchedulingSetRollsBackEveryBranchOnMismatch(t *testing.T) {
	ctx, store, taskID, branches := readyFanoutBranchesForInterruptionTest(t)
	reason := workflow.CurrentNodeInterruptionReason("workflow_test_interruption")
	_, err := store.InterruptCurrentNodeSchedulingSet(
		ctx,
		taskID,
		[]CurrentNodeSchedulingTarget{
			{Reference: branches[0].Reference, Expected: workflow.CurrentNodeSchedulingReady},
			{Reference: branches[1].Reference, Expected: workflow.CurrentNodeSchedulingAdmitted},
		},
		reason,
		workflow.NewCurrentNodeInterruptionDetail(string(reason), errors.New("test mismatch")),
	)
	if err == nil {
		t.Fatal("scheduling-mismatched interruption unexpectedly succeeded")
	}

	currentNodes, listErr := store.ListCurrentNodes(ctx, taskID)
	if listErr != nil {
		t.Fatalf("ListCurrentNodes: %v", listErr)
	}
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil ||
			currentNode.Scheduling.State != workflow.CurrentNodeSchedulingReady {
			t.Fatalf("Current Node after rolled-back interruption = %+v, want ready", currentNode)
		}
	}
}

func TestInterruptCurrentNodeSchedulingSetCommitsEveryBranchTogether(t *testing.T) {
	ctx, store, taskID, branches := readyFanoutBranchesForInterruptionTest(t)
	reason := workflow.CurrentNodeInterruptionReason("workflow_test_interruption")
	result, err := store.InterruptCurrentNodeSchedulingSet(
		ctx,
		taskID,
		[]CurrentNodeSchedulingTarget{
			{Reference: branches[0].Reference, Expected: workflow.CurrentNodeSchedulingReady},
			{Reference: branches[1].Reference, Expected: workflow.CurrentNodeSchedulingReady},
		},
		reason,
		workflow.NewCurrentNodeInterruptionDetail(string(reason), nil),
	)
	if err != nil {
		t.Fatalf("InterruptCurrentNodeSchedulingSet: %v", err)
	}
	if len(result.Interrupted) != len(branches) {
		t.Fatalf("interrupted references = %+v, want every branch", result.Interrupted)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil ||
			currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
			t.Fatalf("Current Node after committed interruption = %+v, want interrupted", currentNode)
		}
	}
}

func readyFanoutBranchesForInterruptionTest(
	t *testing.T,
) (context.Context, *Store, workflow.TaskID, []workflow.CurrentNode) {
	t.Helper()
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		OutputValues: map[string]string{"summary": "interrupt branches"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode fan-out: %v", err)
	}
	return ctx, store, task.ID, completed.Mutation.Created
}
