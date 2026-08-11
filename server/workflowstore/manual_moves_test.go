package workflowstore

import (
	"errors"
	"strings"
	"testing"

	"core/server/workflow"
)

func TestPrepareManualMoveRejectsOversizedCommentary(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	target := startTask(t, ctx, store, task.ID).Mutation.Created[0].Reference.NodeID

	_, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: target,
		Commentary:   strings.Repeat("x", workflow.MaxCommentaryBytes+1),
	})
	var validation CompletionValidationError
	if !errors.As(err, &validation) || !validation.HasCode(CompletionCodeCommentaryTooLarge) {
		t.Fatalf("PrepareManualMove error = %T %v, want oversized commentary validation", err, err)
	}
}

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
		Commentary:   "  manual note  ",
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
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
	if moved.Outcome != ManualMoveResultOutcomeApplied {
		t.Fatalf("manual move outcome = %q, want applied", moved.Outcome)
	}
	if len(moved.Mutation.Removed) != 1 || !moved.Mutation.Removed[0].Equal(source.Reference) {
		t.Fatalf("manual move removed = %+v, want source current node", moved.Mutation.Removed)
	}
	if len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) ||
		moved.Mutation.Created[0].EnteredByEdgeID == nil ||
		*moved.Mutation.Created[0].EnteredByEdgeID != edge.ID ||
		moved.Mutation.Created[0].Scheduling == nil ||
		moved.Mutation.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady ||
		moved.Mutation.Created[0].CurrentInputValues["prior_summary"] != "manual plan" ||
		moved.Mutation.Created[0].CurrentInputValues[workflow.RuntimePromptParameterCommentary] != "manual note" {
		t.Fatalf("manual move created = %+v, want ready materialized target", moved.Mutation.Created)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(moved.Mutation.Created[0].Reference) {
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
	_ = started
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	before, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes before rejected move: %v", err)
	}
	_, err = store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values: map[workflow.ModelKey]map[string]string{
			"plan": {"unknown": "value"},
		},
	})
	if !errors.Is(err, ErrManualMoveValuesInvalid) {
		t.Fatalf("PrepareManualMove error = %T %v, want ErrManualMoveValuesInvalid", err, err)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != len(before) {
		t.Fatalf("current nodes after rejected move = %+v, before = %+v", currentNodes, before)
	}
	for index := range before {
		if !currentNodes[index].Reference.Equal(before[index].Reference) {
			t.Fatalf("current nodes after rejected move = %+v, before = %+v", currentNodes, before)
		}
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
		Values: map[workflow.ModelKey]map[string]string{
			"plan": {"prior_summary": "manual plan"},
		},
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
		Commentary:   "  Manual proposal is ready.  ",
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual proposal"}},
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
	if moved.Outcome != ManualMoveResultOutcomeApplied {
		t.Fatalf("manual move outcome = %q, want applied", moved.Outcome)
	}
	if len(moved.TaskAttentionResolution.Approvals) != 1 ||
		moved.TaskAttentionResolution.Approvals[0].ApprovalID != supersededApprovalID {
		t.Fatalf("manual move attention resolution = %+v, want superseded Approval", moved.TaskAttentionResolution)
	}
	if len(moved.Mutation.Removed) != 1 || !moved.Mutation.Removed[0].Equal(source.Reference) ||
		len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) ||
		moved.Mutation.Created[0].CurrentInputValues["prior_summary"] != "manual proposal" ||
		moved.Mutation.Created[0].CurrentInputValues[workflow.RuntimePromptParameterCommentary] != "Manual proposal is ready." {
		t.Fatalf("manual move mutation = %+v, want immediate approved replacement", moved.Mutation)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes before Approval: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(moved.Mutation.Created[0].Reference) {
		t.Fatalf("current nodes after manual Approval = %+v, want target only", currentNodes)
	}
	approvals, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("pending Approvals = %+v, want none after manual Approval", approvals)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil || targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
		t.Fatalf("execution target after manual move = %+v, want locked none target", targetContext.Task.ExecutionTarget)
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
	if moved.Outcome != ManualMoveResultOutcomeApplied ||
		len(moved.Mutation.Removed) != 1 || !moved.Mutation.Removed[0].Equal(source) ||
		len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != fixture.scriptID ||
		moved.Mutation.Created[0].Scheduling == nil ||
		moved.Mutation.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
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

		preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
		if err != nil || preview.Outcome != ManualMovePreviewOutcomeTransition {
			t.Fatalf("missing-edge manual move preview = %+v, error = %v, want transition choice", preview, err)
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

		preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
		if err != nil || preview.Outcome != ManualMovePreviewOutcomeTransition {
			t.Fatalf("backward manual move preview = %+v, error = %v, want transition choice", preview, err)
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

		preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
		if err != nil || preview.Outcome != ManualMovePreviewOutcomeNoOp {
			t.Fatalf("parallel manual move preview = %+v, error = %v, want same-current no-op", preview, err)
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
	if moved.Outcome != ManualMoveResultOutcomeApplied ||
		len(moved.Mutation.Created) != 1 || moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(start) {
		t.Fatalf("manual move target = %+v, want start current node", moved.Mutation.Created)
	}
	approvals, err = store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals after move: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("pending approvals after move = %+v, want none", approvals)
	}
}

func TestManualMoveFanoutTransitionReplacesTaskWithEveryBranch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "impl_a")
	transition := workflow.TransitionID("split")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &transition,
		Values:        map[workflow.ModelKey]map[string]string{"plan": {"summary": "manual plan"}},
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
	if moved.Outcome != ManualMoveResultOutcomeApplied ||
		len(moved.Mutation.Removed) != 1 ||
		!moved.Mutation.Removed[0].Equal(source.Reference) ||
		len(moved.Mutation.Created) != 2 {
		t.Fatalf("fan-out manual move = %+v, want task-wide branch replacement", moved)
	}
	branches := make(map[workflow.TransitionBranchKey]bool)
	for _, currentNode := range moved.Mutation.Created {
		branch, ok := currentNode.Reference.TransitionBranchKey()
		if !ok {
			t.Fatalf("created fan-out node = %+v, want branch scope", currentNode)
		}
		branches[branch] = true
	}
	if !branches["split_a"] || !branches["split_b"] {
		t.Fatalf("created branches = %+v, want split_a and split_b", branches)
	}
}

func TestManualMoveFromPartiallyArrivedFanoutReplacesTheWholeTaskGroup(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	split, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode split: %v", err)
	}
	branches := make(map[workflow.TransitionBranchKey]workflow.CurrentNode)
	for _, currentNode := range split.Mutation.Created {
		branch, ok := currentNode.Reference.TransitionBranchKey()
		if !ok {
			t.Fatalf("split current node = %+v, want branch scope", currentNode)
		}
		branches[branch] = currentNode
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_a"].Reference,
		TransitionID: "join_a",
		OutputValues: map[string]string{"joined": "branch A"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode partial join: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "synth")
	transition := workflow.TransitionID("synthesize")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &transition,
		Values:        map[workflow.ModelKey]map[string]string{"impl_a": {"joined": "manual branch A"}},
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
	if moved.Outcome != ManualMoveResultOutcomeApplied || len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) {
		t.Fatalf("manual move from fan-out = %+v, want one post-Join target", moved)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(moved.Mutation.Created[0].Reference) {
		t.Fatalf("current nodes = %+v, want only post-Join target", currentNodes)
	}
}

func TestManualMoveFinalRevalidationReturnsNoOpWithoutExecutionTargetMutation(t *testing.T) {
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
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "automatic plan"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode before final apply: %v", err)
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
	if moved.Outcome != ManualMoveResultOutcomeNoOp || len(moved.CurrentNodes) != 1 ||
		moved.CurrentNodes[0].Reference.NodeID != workflow.NodeIDOf(target) {
		t.Fatalf("final revalidation result = %+v, want authoritative no-op", moved)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("execution target after final no-op = %+v, want unchanged", targetContext.Task.ExecutionTarget)
	}
}

func TestManualMoveScriptValidationRollsBackReplacement(t *testing.T) {
	fixture := newScriptExecutionFixture(t, "scripts/missing", nil)
	prepared, err := fixture.store.PrepareManualMove(fixture.ctx, ManualMoveRequest{
		TaskID:       fixture.task.ID,
		TargetNodeID: fixture.scriptID,
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if _, err := fixture.store.ApplyManualMove(fixture.ctx, prepared, nil); err == nil {
		t.Fatal("ApplyManualMove: want invalid script error")
	}
	currentNodes, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].Reference.NodeID == fixture.scriptID {
		t.Fatalf("current nodes after invalid script move = %+v, want unchanged source", currentNodes)
	}
}
