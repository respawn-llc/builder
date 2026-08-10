package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

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

func TestPreparedCurrentNodeCompletionRollbackLeavesSourceCurrent(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	prepared, err := store.PrepareCurrentNodeCompletion(ctx, CurrentNodeCompletionRequest{
		Source: source.Reference,
	})
	if err != nil {
		t.Fatalf("PrepareCurrentNodeCompletion: %v", err)
	}
	result := prepared.Result()
	if result.CurrentNodeCompletion == nil {
		t.Fatal("prepared mutation omitted completion result")
	}
	completed := *result.CurrentNodeCompletion
	if len(completed.Mutation.Removed) != 1 ||
		len(completed.Mutation.Created) != 1 {
		t.Fatalf("prepared completion result = %+v, want one source replacement", result)
	}
	result.CurrentNodeCompletion.Mutation.Created = nil
	immutable := prepared.Result()
	if immutable.CurrentNodeCompletion == nil ||
		len(immutable.CurrentNodeCompletion.Mutation.Created) != 1 {
		t.Fatalf("prepared completion changed through caller-owned result: %+v", immutable)
	}
	if err := prepared.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(source.Reference) {
		t.Fatalf("Current Nodes after completion rollback = %+v, want source %v", currentNodes, source.Reference)
	}
}

func TestPreparedRetainedSuccessorRollbackRestoresUnownedSourceSession(t *testing.T) {
	fixture := newImmediateContextCompletionFixture(t, workflow.ContextModeContinueSession)
	if _, err := fixture.store.db.ExecContext(
		fixture.ctx,
		`UPDATE sessions SET task_id = NULL WHERE id = ?`,
		fixture.sessionID.String(),
	); err != nil {
		t.Fatalf("clear source Session Task owner: %v", err)
	}

	prepared, err := fixture.store.PrepareCurrentNodeCompletion(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "rollback retained successor"},
	})
	if err != nil {
		t.Fatalf("PrepareCurrentNodeCompletion: %v", err)
	}
	successor := prepared.Result().Mutation.Created[0]
	if err := prepared.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	owner, err := fixture.store.TaskIDForSession(fixture.ctx, fixture.sessionID)
	if err != nil {
		t.Fatalf("TaskIDForSession: %v", err)
	}
	if owner != nil {
		t.Fatalf("rolled-back Session owner = %q, want absent", *owner)
	}
	if _, err := fixture.store.LatestTaskSessionForNode(fixture.ctx, successor.Reference); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rolled-back successor provenance = %v, want absent", err)
	}
	currentNodes, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.source.Reference.TaskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(fixture.source.Reference) {
		t.Fatalf("Current Nodes after rollback = %+v, want source", currentNodes)
	}
}

func TestPreparedRetainedSuccessorCancellationRollsBackBindingAndProvenance(t *testing.T) {
	fixture := newImmediateContextCompletionFixture(t, workflow.ContextModeContinueSession)
	if _, err := fixture.store.db.ExecContext(
		fixture.ctx,
		`UPDATE sessions SET task_id = NULL WHERE id = ?`,
		fixture.sessionID.String(),
	); err != nil {
		t.Fatalf("clear source Session Task owner: %v", err)
	}
	ctx, cancel := context.WithCancel(fixture.ctx)
	prepared, err := fixture.store.PrepareCurrentNodeCompletion(ctx, CurrentNodeCompletionRequest{
		Source:       fixture.source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "cancel retained successor"},
	})
	if err != nil {
		t.Fatalf("PrepareCurrentNodeCompletion: %v", err)
	}
	successor := prepared.Result().Mutation.Created[0]
	cancel()
	if err := prepared.Commit(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit after cancellation = %v, want context cancellation", err)
	}
	owner, err := fixture.store.TaskIDForSession(fixture.ctx, fixture.sessionID)
	if err != nil {
		t.Fatalf("TaskIDForSession: %v", err)
	}
	if owner != nil {
		t.Fatalf("canceled Session owner = %q, want absent", *owner)
	}
	if _, err := fixture.store.LatestTaskSessionForNode(fixture.ctx, successor.Reference); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("canceled successor provenance = %v, want absent", err)
	}
}

func TestRetainedSuccessorAssociationFailureRollsBackCurrentNodeReplacement(t *testing.T) {
	fixture := newImmediateContextCompletionFixture(t, workflow.ContextModeContinueSession)
	fixture.store.now = func() time.Time { return time.UnixMilli(0).UTC() }

	if _, err := fixture.store.PrepareCurrentNodeCompletion(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "association failure"},
	}); err == nil {
		t.Fatal("PrepareCurrentNodeCompletion accepted invalid association time")
	}
	currentNodes, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.source.Reference.TaskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(fixture.source.Reference) {
		t.Fatalf("Current Nodes after association failure = %+v, want source", currentNodes)
	}
}

func TestRetainedSuccessorRejectsContradictorySessionTaskOwnerWithoutChanges(t *testing.T) {
	fixture := newImmediateContextCompletionFixture(t, workflow.ContextModeContinueSession)
	sourceTask, err := fixture.store.queries.GetTask(fixture.ctx, string(fixture.source.Reference.TaskID))
	if err != nil {
		t.Fatalf("GetTask source: %v", err)
	}
	otherTask := createDefaultTask(t, fixture.ctx, fixture.store, sourceTask.ProjectID)
	if _, err := fixture.store.db.ExecContext(
		fixture.ctx,
		`UPDATE sessions SET task_id = ? WHERE id = ?`,
		otherTask.ID,
		fixture.sessionID.String(),
	); err != nil {
		t.Fatalf("seed contradictory Session Task owner: %v", err)
	}

	if _, err := fixture.store.PrepareCurrentNodeCompletion(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "contradictory owner"},
	}); err == nil {
		t.Fatal("PrepareCurrentNodeCompletion accepted contradictory Session Task owner")
	}
	currentNodes, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.source.Reference.TaskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(fixture.source.Reference) {
		t.Fatalf("Current Nodes after contradictory ownership = %+v, want source", currentNodes)
	}
	owner, err := fixture.store.TaskIDForSession(fixture.ctx, fixture.sessionID)
	if err != nil {
		t.Fatalf("TaskIDForSession: %v", err)
	}
	if owner == nil || *owner != otherTask.ID {
		t.Fatalf("Session owner after rejection = %v, want %q", owner, otherTask.ID)
	}
}

func TestPreparedPendingApprovalRollbackKeepsFrozenApprovalAndSource(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "prepared approval"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("completion did not create a pending Approval")
	}

	prepared, err := store.PreparePendingApprovalApply(ctx, completed.PendingApproval.ID)
	if err != nil {
		t.Fatalf("PreparePendingApprovalApply: %v", err)
	}
	result := prepared.Result()
	if result.PendingApprovalApply == nil ||
		len(result.CreatedExecutableCurrentNodes) != 1 {
		t.Fatalf("prepared Approval result = %+v, want one executable target", result)
	}
	if err := prepared.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	approvals, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(source.Reference) ||
		len(approvals) != 1 || approvals[0].ID != completed.PendingApproval.ID {
		t.Fatalf("rollback state: current=%+v approvals=%+v, want frozen source Approval", currentNodes, approvals)
	}
}

func TestPreparedExecutableManualMoveRollbackKeepsCurrentNodeAndExecutionTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	move, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	prepared, err := store.PrepareManualMoveApply(ctx, move, &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	})
	if err != nil {
		t.Fatalf("PrepareManualMoveApply: %v", err)
	}
	result := prepared.Result()
	if result.ManualMove == nil ||
		result.ManualMove.Outcome != ManualMoveResultOutcomeApplied ||
		len(result.CreatedExecutableCurrentNodes) != 1 {
		t.Fatalf("prepared Manual Move result = %+v, want one executable target", result)
	}
	if err := prepared.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(source.Reference) ||
		targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("rollback state: current=%+v target=%+v, want unchanged source and unlocked target", currentNodes, targetContext.Task.ExecutionTarget)
	}
}

func TestExecutableManualMoveTargetSelectionFailureKeepsCurrentNodeAndExecutionTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	move, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if _, err := store.PrepareManualMoveApply(ctx, move, nil); !errors.Is(err, ErrExecutionTargetRequired) {
		t.Fatalf("PrepareManualMoveApply error = %v, want %v", err, ErrExecutionTargetRequired)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(source.Reference) ||
		targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("selection failure state: current=%+v target=%+v, want unchanged source and unlocked target", currentNodes, targetContext.Task.ExecutionTarget)
	}
}

func TestPreparedNoOpManualMoveCommitsWithoutCreatingExecutableWork(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	move, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: source.Reference.NodeID,
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	prepared, err := store.PrepareManualMoveApply(ctx, move, nil)
	if err != nil {
		t.Fatalf("PrepareManualMoveApply: %v", err)
	}
	result := prepared.Result()
	if result.ManualMove == nil ||
		result.ManualMove.Outcome != ManualMoveResultOutcomeNoOp ||
		len(result.CreatedExecutableCurrentNodes) != 0 {
		t.Fatalf("prepared no-op Manual Move = %+v", result)
	}
	if err := prepared.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(source.Reference) {
		t.Fatalf("Current Nodes after no-op = %+v, want source", currentNodes)
	}
}

func TestPreparedTerminalApprovalCreatesNoExecutableWork(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "done")
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source: source.Reference,
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("completion did not create terminal Approval")
	}
	prepared, err := store.PreparePendingApprovalApply(ctx, completed.PendingApproval.ID)
	if err != nil {
		t.Fatalf("PreparePendingApprovalApply: %v", err)
	}
	result := prepared.Result()
	if result.PendingApprovalApply == nil ||
		len(result.PendingApprovalApply.Mutation.Created) != 1 ||
		result.PendingApprovalApply.Mutation.Created[0].Scheduling != nil ||
		len(result.CreatedExecutableCurrentNodes) != 0 {
		t.Fatalf("prepared terminal Approval = %+v, want non-executable target only", result)
	}
	if err := prepared.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
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
