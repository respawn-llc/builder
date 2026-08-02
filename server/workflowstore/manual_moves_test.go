package workflowstore

import (
	"errors"
	"testing"

	"core/server/workflow"
)

func TestManualMoveForwardExecutableAgentReplacesSerialCurrentNode(t *testing.T) {
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
	edge := edgeByKey(t, definition, "next")

	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		OutputValues: map[string]string{"prior_summary": "manual plan"},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if !prepared.RequiresExecutionTarget() {
		t.Fatal("forward executable move did not require execution-target selection")
	}
	moved, err := store.ApplyManualMove(ctx, prepared, &ExecutionTargetCandidate{
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
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if len(moved.Removed) != 1 || !moved.Removed[0].Equal(source.Reference) {
		t.Fatalf("manual move removed = %+v, want source current node", moved.Removed)
	}
	if len(moved.Created) != 1 ||
		moved.Created[0].Reference.NodeID != workflow.NodeIDOf(target) ||
		moved.Created[0].EnteredByEdgeID == nil ||
		*moved.Created[0].EnteredByEdgeID != edge.ID ||
		moved.Created[0].Scheduling == nil ||
		moved.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady ||
		moved.Created[0].CurrentInputValues["prior_summary"] != "manual plan" {
		t.Fatalf("manual move created = %+v, want ready materialized target", moved.Created)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(moved.Created[0].Reference) {
		t.Fatalf("current nodes after manual move = %+v, want target only", currentNodes)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil || targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
		t.Fatalf("execution target after manual move = %+v, want locked none target", targetContext.Task.ExecutionTarget)
	}
}

func TestPrepareManualMoveValidatesExecutableCompletionShapeWithoutMutation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	_, err = store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		OutputValues: map[string]string{"unknown": "value"},
	})
	var validationErr CompletionValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("PrepareManualMove error = %T %v, want CompletionValidationError", err, err)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(started.Reference) {
		t.Fatalf("current nodes = %+v, want unchanged source", currentNodes)
	}
}

func TestPrepareManualMoveDryRunsTargetValueAndContextMaterialization(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeContinueSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")

	if _, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		OutputValues: map[string]string{"prior_summary": "manual plan"},
	}); err == nil {
		t.Fatal("PrepareManualMove accepted a target whose required continuation context could not be materialized")
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(source.Reference) {
		t.Fatalf("current nodes = %+v, want unchanged source", currentNodes)
	}
}

func TestManualMoveForwardExecutableReplacesApprovalWithoutStartingTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "automatic proposal"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("CompleteCurrentNode returned no pending Approval")
	}
	supersededApprovalID := completed.PendingApproval.ID
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")

	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		OutputValues: map[string]string{"prior_summary": "manual proposal"},
		Commentary:   "  Manual proposal is ready.  ",
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := store.ApplyManualMove(ctx, prepared, &ExecutionTargetCandidate{
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
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if moved.PendingApproval == nil || moved.PendingApproval.ID == supersededApprovalID {
		t.Fatalf("manual move pending Approval = %+v, want replacement", moved.PendingApproval)
	}
	if len(moved.TaskAttentionResolution.Approvals) != 1 ||
		moved.TaskAttentionResolution.Approvals[0].ApprovalID != supersededApprovalID {
		t.Fatalf("manual move attention resolution = %+v, want superseded Approval", moved.TaskAttentionResolution)
	}
	if len(moved.Retained) != 1 || !moved.Retained[0].Reference.Equal(source.Reference) {
		t.Fatalf("manual move retained = %+v, want source Current Node", moved.Retained)
	}
	if len(moved.Removed) != 0 || len(moved.Created) != 0 {
		t.Fatalf("manual move mutation = %+v, want no Current Node replacement before Approval", moved.CurrentNodeMutationResult)
	}
	if moved.PendingApproval.OutputValues["prior_summary"] != "manual proposal" ||
		moved.PendingApproval.Commentary != "Manual proposal is ready." ||
		len(moved.PendingApproval.Branches) != 1 ||
		moved.PendingApproval.Branches[0].Target.CurrentNode.CurrentInputValues["prior_summary"] != "manual proposal" {
		t.Fatalf("manual move pending Approval = %+v, want frozen manual values", moved.PendingApproval)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes before Approval: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(source.Reference) {
		t.Fatalf("current nodes before Approval = %+v, want source only", currentNodes)
	}
	approvals, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(approvals) != 1 ||
		approvals[0].ID != moved.PendingApproval.ID ||
		approvals[0].Commentary != "Manual proposal is ready." {
		t.Fatalf("pending Approvals = %+v, want only replacement Approval", approvals)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil || targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
		t.Fatalf("execution target after manual move = %+v, want locked none target", targetContext.Task.ExecutionTarget)
	}

	approved, err := store.ApplyPendingApproval(ctx, moved.PendingApproval.ID)
	if err != nil {
		t.Fatalf("ApplyPendingApproval: %v", err)
	}
	if len(approved.Mutation.Removed) != 1 || !approved.Mutation.Removed[0].Equal(source.Reference) ||
		len(approved.Mutation.Created) != 1 ||
		approved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) {
		t.Fatalf("Approval mutation = %+v, want source replaced with target", approved.Mutation)
	}
	if len(approved.TaskAttentionResolution.Approvals) != 1 ||
		approved.TaskAttentionResolution.Approvals[0].ApprovalID != moved.PendingApproval.ID {
		t.Fatalf("Approval attention resolution = %+v, want applied Approval", approved.TaskAttentionResolution)
	}
}

func TestManualMoveForwardExecutableScriptValidatesAndMaterializesTarget(t *testing.T) {
	fixture := newScriptExecutionFixture(t, "scripts/complete", []byte("#!/bin/sh\nprintf '{}'\n"))
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, definition, workflow.NodeKindStart)
	source, err := workflow.NewCurrentNodeReference(fixture.task.ID, workflow.NodeIDOf(start), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}

	prepared, err := fixture.store.PrepareManualMove(fixture.ctx, ManualMoveRequest{
		TaskID:       fixture.task.ID,
		TargetNodeID: fixture.scriptID,
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if !prepared.RequiresExecutionTarget() {
		t.Fatal("script move did not require an execution target")
	}
	moved, err := fixture.store.ApplyManualMove(fixture.ctx, prepared, nil)
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if len(moved.Removed) != 1 || !moved.Removed[0].Equal(source) ||
		len(moved.Created) != 1 ||
		moved.Created[0].Reference.NodeID != fixture.scriptID ||
		moved.Created[0].Scheduling == nil ||
		moved.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("script move mutation = %+v, want ready script target", moved)
	}
}

func TestManualMoveExecutableRejectsMissingBackwardAndParallelPaths(t *testing.T) {
	t.Run("missing direct edge", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		task := createDefaultTask(t, ctx, store, binding.ProjectID)
		definition, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		target := nodeByKey(t, definition, "implement")

		if _, err := store.PrepareManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)}); !errors.Is(err, ErrManualMoveExecutableTargetNeedsEdge) {
			t.Fatalf("missing-edge manual move preparation error = %v, want %v", err, ErrManualMoveExecutableTargetNeedsEdge)
		}
	})

	t.Run("backward executable target", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		task := createDefaultTask(t, ctx, store, binding.ProjectID)
		source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
		if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: "next",
			OutputValues: map[string]string{"prior_summary": "plan"},
		}); err != nil {
			t.Fatalf("CompleteCurrentNode: %v", err)
		}
		definition, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		target := nodeByKey(t, definition, "plan")

		if _, err := store.PrepareManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)}); !errors.Is(err, ErrManualMoveExecutableTargetNeedsEdge) {
			t.Fatalf("backward manual move preparation error = %v, want %v", err, ErrManualMoveExecutableTargetNeedsEdge)
		}
	})

	t.Run("parallel current nodes", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createFanoutJoinWorkflow(t, ctx, store)
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		task := createDefaultTask(t, ctx, store, binding.ProjectID)
		source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
		if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: "split",
			OutputValues: map[string]string{"summary": "plan"},
		}); err != nil {
			t.Fatalf("CompleteCurrentNode split: %v", err)
		}
		definition, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		target := nodeByKey(t, definition, "impl_a")

		if _, err := store.PrepareManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)}); !errors.Is(err, ErrManualMoveExecutableTargetNeedsEdge) {
			t.Fatalf("parallel manual move preparation error = %v, want %v", err, ErrManualMoveExecutableTargetNeedsEdge)
		}
	})
}

func TestManualMoveToNonExecutableSupersedesPendingApproval(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "plan"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	approvals, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals before move: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("pending approvals before move = %+v, want one", approvals)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, definition, workflow.NodeKindStart)

	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(start)})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := store.ApplyManualMove(ctx, prepared, nil)
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if len(moved.Created) != 1 || moved.Created[0].Reference.NodeID != workflow.NodeIDOf(start) {
		t.Fatalf("manual move target = %+v, want start current node", moved.Created)
	}
	approvals, err = store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals after move: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("pending approvals after move = %+v, want none", approvals)
	}
}
