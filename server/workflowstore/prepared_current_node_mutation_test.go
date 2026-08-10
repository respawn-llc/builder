package workflowstore

import (
	"context"
	"errors"
	"testing"

	"core/server/workflow"
)

func TestPreparedTaskStartCommitReplacesBacklogOnce(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	prepared, err := store.PrepareTaskStart(ctx, task.ID)
	if err != nil {
		t.Fatalf("PrepareTaskStart: %v", err)
	}
	result := prepared.Result()
	result.Mutation.Created = nil
	result.CreatedExecutableCurrentNodes[0].Scheduling.State = workflow.CurrentNodeSchedulingAdmitted
	immutable := prepared.Result()
	if len(immutable.Mutation.Created) != 1 ||
		immutable.CreatedExecutableCurrentNodes[0].Scheduling == nil ||
		immutable.CreatedExecutableCurrentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("prepared result changed through caller-owned copy: %+v", immutable)
	}
	if err := prepared.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := prepared.Rollback(); !errors.Is(err, ErrPreparedCurrentNodeMutationConsumed) {
		t.Fatalf("Rollback after Commit = %v, want consumed error", err)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		len(result.CreatedExecutableCurrentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(result.CreatedExecutableCurrentNodes[0].Reference) ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("Current Nodes after Commit = %+v, prepared result = %+v", currentNodes, result)
	}
}

func TestPreparedTaskStartRollbackLeavesBacklogUnchanged(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	prepared, err := store.PrepareTaskStart(ctx, task.ID)
	if err != nil {
		t.Fatalf("PrepareTaskStart: %v", err)
	}
	result := prepared.Result()
	if len(result.Mutation.Created) != 1 ||
		result.Mutation.Created[0].Scheduling == nil ||
		result.Mutation.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("prepared Start result = %+v, want one ready executable Current Node", result)
	}
	if err := prepared.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].Scheduling != nil ||
		currentNodes[0].SessionID != nil {
		t.Fatalf("Current Nodes after rollback = %+v, want unchanged backlog", currentNodes)
	}
}

func TestPreparedTaskStartCancellationBeforeCommitLeavesBacklogUnchanged(t *testing.T) {
	parent, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, parent, store, binding.ProjectID)
	task := createDefaultTask(t, parent, store, binding.ProjectID)
	ctx, cancel := context.WithCancel(parent)

	prepared, err := store.PrepareTaskStart(ctx, task.ID)
	if err != nil {
		t.Fatalf("PrepareTaskStart: %v", err)
	}
	cancel()
	if err := prepared.Commit(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit after cancellation = %v, want context cancellation", err)
	}

	currentNodes, err := store.ListCurrentNodes(parent, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].Scheduling != nil {
		t.Fatalf("Current Nodes after canceled Commit = %+v, want unchanged backlog", currentNodes)
	}
}

func TestPreparedTaskResumeRollbackLeavesEveryBranchInterrupted(t *testing.T) {
	ctx, store, taskID, interrupted := preparedResumeBranchFixture(t)

	prepared, err := store.PrepareTaskResume(ctx, taskID)
	if err != nil {
		t.Fatalf("PrepareTaskResume: %v", err)
	}
	result := prepared.Result()
	if len(result.CreatedExecutableCurrentNodes) != len(interrupted) {
		t.Fatalf("prepared resumed nodes = %+v, want %d branches", result.CreatedExecutableCurrentNodes, len(interrupted))
	}
	if err := prepared.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != len(interrupted) {
		t.Fatalf("Current Nodes after rollback = %+v, want %d branches", currentNodes, len(interrupted))
	}
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil ||
			currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
			t.Fatalf("Current Node after rollback = %+v, want interrupted", currentNode)
		}
	}
}

func TestPreparedTaskResumeCommitsEveryBranchAndAttentionTogether(t *testing.T) {
	ctx, store, taskID, interrupted := preparedResumeBranchFixture(t)

	prepared, err := store.PrepareTaskResume(ctx, taskID)
	if err != nil {
		t.Fatalf("PrepareTaskResume: %v", err)
	}
	result := prepared.Result()
	if len(result.TaskAttentionResolution.InterruptedCurrentNodes) != len(interrupted) {
		t.Fatalf("prepared attention = %+v, want every interrupted branch", result.TaskAttentionResolution)
	}
	if err := prepared.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != len(interrupted) {
		t.Fatalf("Current Nodes after Commit = %+v, want %d branches", currentNodes, len(interrupted))
	}
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil ||
			currentNode.Scheduling.State != workflow.CurrentNodeSchedulingReady {
			t.Fatalf("Current Node after Commit = %+v, want ready", currentNode)
		}
	}
}

func TestPreparedTaskResumeCancellationBeforeCommitLeavesEveryBranchInterrupted(t *testing.T) {
	parent, store, taskID, interrupted := preparedResumeBranchFixture(t)
	ctx, cancel := context.WithCancel(parent)

	prepared, err := store.PrepareTaskResume(ctx, taskID)
	if err != nil {
		t.Fatalf("PrepareTaskResume: %v", err)
	}
	cancel()
	if err := prepared.Commit(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit after cancellation = %v, want context cancellation", err)
	}

	currentNodes, err := store.ListCurrentNodes(parent, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != len(interrupted) {
		t.Fatalf("Current Nodes after canceled Commit = %+v, want %d branches", currentNodes, len(interrupted))
	}
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil ||
			currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
			t.Fatalf("Current Node after canceled Commit = %+v, want interrupted", currentNode)
		}
	}
}

func preparedResumeBranchFixture(t *testing.T) (context.Context, *Store, workflow.TaskID, []workflow.CurrentNode) {
	t.Helper()
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		OutputValues: map[string]string{"summary": "prepared resume"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode fan-out: %v", err)
	}
	if len(completed.Mutation.Created) != 2 {
		t.Fatalf("fan-out Current Nodes = %+v, want two branches", completed.Mutation.Created)
	}
	for _, currentNode := range completed.Mutation.Created {
		reason := workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed")
		if err := store.InterruptCurrentNode(
			ctx,
			currentNode.Reference,
			reason,
			workflow.CurrentNodeInterruptionDetail{Code: string(reason)},
		); err != nil {
			t.Fatalf("InterruptCurrentNode(%v): %v", currentNode.Reference, err)
		}
	}
	return ctx, store, task.ID, completed.Mutation.Created
}
