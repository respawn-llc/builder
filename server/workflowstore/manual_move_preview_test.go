package workflowstore

import (
	"errors"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
)

func TestManualMovePreviewReturnsNoOpForAnAlreadyCurrentDestination(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	target := started.Mutation.Created[0].Reference.NodeID

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: target})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeNoOp ||
		len(preview.CurrentNodes) != 1 ||
		!preview.CurrentNodes[0].Reference.Equal(started.Mutation.Created[0].Reference) {
		t.Fatalf("preview = %+v, want authoritative no-op Current Nodes", preview)
	}
}

func TestManualMovePreviewNoOpPrecedesWorkflowValidation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	store.roleResolver = testsetup.RoleResolver{"coder": {}}

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: started.Mutation.Created[0].Reference.NodeID,
	})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeNoOp {
		t.Fatalf("preview = %+v, want no-op before invalid Workflow validation", preview)
	}
}

func TestManualMovePreviewReturnsDirectOutcomeForTerminalDestination(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "done")

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeDirect || len(preview.CurrentNodes) != 0 {
		t.Fatalf("preview = %+v, want direct outcome", preview)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("started mutation = %+v, want one source Current Node", started.Mutation)
	}
}

func TestManualMovePreviewFindsIncomingTransitionWithoutCurrentEdge(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	if _, err := store.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeTransition ||
		len(preview.Choices) != 1 ||
		preview.Choices[0].TransitionKey != "next" {
		t.Fatalf("preview = %+v, want one incoming Transition choice", preview)
	}
}

func TestManualMovePreviewExpandsFanoutTransitionChoice(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	if _, err := store.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "impl_a")

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeTransition ||
		len(preview.Choices) != 1 ||
		len(preview.Choices[0].Edges) != 2 ||
		preview.Choices[0].TransitionKey != "split" {
		t.Fatalf("preview = %+v, want expanded Fan-Out Transition choice", preview)
	}
}

func TestManualMovePreviewDescribesPriorJoinParameterRequirement(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	auditID := workflow.NodeID("node-audit-" + workflowID.String())
	auditGroupID := workflow.TransitionGroupID("group-audit-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		synth := nodeByKey(t, def, "synth")
		doneGroupID := workflow.TransitionGroupID("group-synth-done-" + workflowID.String())
		req.Nodes = append(req.Nodes, NodeRecord{
			ID:             auditID,
			WorkflowID:     workflowID,
			Key:            "audit",
			Kind:           workflow.NodeKindAgent,
			DisplayName:    "Audit",
			SubagentRole:   "coder",
			PromptTemplate: "Audit.",
		})
		req.TransitionGroups = mutateWorkflowGraphSaveTransitionGroup(
			req.TransitionGroups,
			doneGroupID,
			func(group *TransitionGroupRecord) {
				group.SourceNodeID = auditID
			},
		)
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           auditGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: workflow.NodeIDOf(synth),
			TransitionID: "audit",
			DisplayName:  "Audit",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                workflow.EdgeID("edge-audit-" + workflowID.String()),
			WorkflowID:        workflowID,
			TransitionGroupID: auditGroupID,
			Key:               "audit",
			TargetNodeID:      auditID,
			ContextMode:       workflow.ContextModeNewSession,
			PromptTemplate:    "Audit {{.Params.synthesize.joined}}.",
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	if _, err := store.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: auditID,
	})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeTransition ||
		len(preview.Choices) != 1 ||
		len(preview.Choices[0].RequiredValues) != 1 {
		t.Fatalf("preview = %+v, want one Transition with one required value", preview)
	}
	required := preview.Choices[0].RequiredValues[0]
	if required.NodeKey != "join" ||
		required.OutputName != "joined" ||
		strings.TrimSpace(required.Description) == "" {
		t.Fatalf("required value = %+v, want described prior Join parameter", required)
	}
}

func TestManualMovePreviewRequiresAndHonorsStableTransitionSelection(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		plan := nodeByKey(t, def, "plan")
		target := nodeByKey(t, def, "implement")
		groupID := workflow.TransitionGroupID("group-alternate-" + workflowID.String())
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           groupID,
			WorkflowID:   workflowID,
			SourceNodeID: workflow.NodeIDOf(plan),
			TransitionID: "alternate",
			DisplayName:  "Alternate",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                workflow.EdgeID("edge-alternate-" + workflowID.String()),
			WorkflowID:        workflowID,
			TransitionGroupID: groupID,
			Key:               "alternate",
			TargetNodeID:      workflow.NodeIDOf(target),
			ContextMode:       workflow.ContextModeNewSession,
			PromptTemplate:    "Alternate {{.Params.prior_summary}}.",
			Parameters:        []workflow.Parameter{{Key: "prior_summary", Description: "Prior summary."}},
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	if _, err := store.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeTransition || len(preview.Choices) != 2 {
		t.Fatalf("preview = %+v, want stable sorted choices", preview)
	}
	if _, err := store.PrepareManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)}); !errors.Is(err, ErrManualMoveTransitionSelectionRequired) {
		t.Fatalf("ambiguous preparation error = %v, want selection-required", err)
	}
	selected := workflow.TransitionID("alternate")
	selectedPreview, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &selected,
	})
	if err != nil {
		t.Fatalf("selected PreviewManualMove: %v", err)
	}
	if len(selectedPreview.Choices) != 1 || selectedPreview.Choices[0].TransitionKey != selected {
		t.Fatalf("selected preview = %+v, want alternate only", selectedPreview)
	}
	unknown := workflow.TransitionID("missing")
	if _, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &unknown,
	}); !errors.Is(err, ErrManualMoveTransitionNotUsable) {
		t.Fatalf("stale selection error = %v, want transition-not-usable", err)
	}
}

func TestManualMovePreviewRejectsFieldsForDirectDestinations(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	if _, err := store.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "done")
	transitionKey := workflow.TransitionID("next")

	_, err = store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &transitionKey,
		Values:        map[workflow.ModelKey]map[string]string{"plan": {"summary": "unexpected"}},
	})
	if !errors.Is(err, ErrManualMoveDirectFieldsNotAllowed) {
		t.Fatalf("direct destination fields error = %v, want direct-fields-not-allowed", err)
	}
}

func TestManualMovePreviewBlocksSerialDestinationInsideFanoutBranch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	detailAID := workflow.NodeID("node-detail-a-" + workflowID.String())
	detailBID := workflow.NodeID("node-detail-b-" + workflowID.String())
	joinAGroupID := workflow.TransitionGroupID("group-join-a-" + workflowID.String())
	joinBGroupID := workflow.TransitionGroupID("group-join-b-" + workflowID.String())
	detailAGroupID := workflow.TransitionGroupID("group-detail-a-" + workflowID.String())
	detailBGroupID := workflow.TransitionGroupID("group-detail-b-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		implA := nodeByKey(t, def, "impl_a")
		implB := nodeByKey(t, def, "impl_b")
		join := nodeByKey(t, def, "join")
		req.Nodes = append(req.Nodes,
			NodeRecord{ID: detailAID, WorkflowID: workflowID, Key: "detail_a", Kind: workflow.NodeKindAgent, DisplayName: "Detail A", SubagentRole: "coder", PromptTemplate: "Detail A."},
			NodeRecord{ID: detailBID, WorkflowID: workflowID, Key: "detail_b", Kind: workflow.NodeKindAgent, DisplayName: "Detail B", SubagentRole: "coder", PromptTemplate: "Detail B."},
		)
		req.TransitionGroups = mutateWorkflowGraphSaveTransitionGroup(req.TransitionGroups, joinAGroupID, func(group *TransitionGroupRecord) {
			group.SourceNodeID = detailAID
		})
		req.TransitionGroups = mutateWorkflowGraphSaveTransitionGroup(req.TransitionGroups, joinBGroupID, func(group *TransitionGroupRecord) {
			group.SourceNodeID = detailBID
		})
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: detailAGroupID, WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(implA), TransitionID: "detail_a", DisplayName: "Detail"},
			TransitionGroupRecord{ID: detailBGroupID, WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(implB), TransitionID: "detail_b", DisplayName: "Detail"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-detail-a-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: detailAGroupID, Key: "detail_a", TargetNodeID: detailAID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Detail A."},
			EdgeRecord{ID: workflow.EdgeID("edge-detail-b-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: detailBGroupID, Key: "detail_b", TargetNodeID: detailBID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Detail B."},
		)
		_ = join
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	if _, err := store.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: detailAID})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeBlocked ||
		preview.Blocker != ManualMoveBlockerParallelBranchRequiresFanOut {
		t.Fatalf("preview = %+v, want parallel-branch blocker", preview)
	}
}

func TestManualMovePreviewReportsUnavailableImmediateContext(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeContinueSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	if _, err := store.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeBlocked ||
		preview.Blocker != ManualMoveBlockerContextSessionUnavailable {
		t.Fatalf("preview = %+v, want unavailable-context blocker", preview)
	}
}

func TestManualMovePreviewAndApplyUsesUnscopedRetainedSessionForParallelTask(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	synthEdge := edgeByKey(t, definition, "synth")
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, synthEdge.ID)
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{
			Kind:    workflow.ContextSourceSelectedNode,
			NodeKey: "plan",
		}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	planSessionID := associateTaskSessionForTest(
		t,
		ctx,
		store,
		binding,
		cfg,
		started.Mutation.Created[0].Reference,
		time.UnixMilli(1_700_000_000_000).UTC(),
	)
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       started.Mutation.Created[0].Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "plan complete"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	target := nodeByKey(t, definition, "synth")

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
	})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeTransition || len(preview.Choices) != 1 {
		t.Fatalf("preview = %+v, want one transition using retained serial context", preview)
	}
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values:       map[workflow.ModelKey]map[string]string{"impl_a": {"joined": "joined summary"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := store.ApplyManualMove(ctx, prepared)
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied ||
		len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].SessionID == nil ||
		*moved.Mutation.Created[0].SessionID != planSessionID {
		t.Fatalf("manual move = %+v, want target using unscoped retained plan Session %q", moved, planSessionID)
	}
}

func TestManualMovePreviewPrefillsAndOverridesPendingApprovalValues(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "approved plan"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if len(preview.Choices) != 1 || len(preview.Choices[0].RequiredValues) != 1 {
		t.Fatalf("preview = %+v, want one required value", preview)
	}
	required := preview.Choices[0].RequiredValues[0]
	if required.NodeKey != "plan" || required.OutputName != "prior_summary" ||
		required.Description != "Prior summary." ||
		required.ResolvedValue == nil || *required.ResolvedValue != "approved plan" {
		t.Fatalf("required value = %+v, want pending Approval prefill", required)
	}

	selected := workflow.TransitionID("next")
	override := "overridden plan"
	overridden, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &selected,
		Values:        map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": override}},
	})
	if err != nil {
		t.Fatalf("overridden PreviewManualMove: %v", err)
	}
	if overridden.Choices[0].RequiredValues[0].ResolvedValue == nil ||
		*overridden.Choices[0].RequiredValues[0].ResolvedValue != override {
		t.Fatalf("overridden required value = %+v, want %q", overridden.Choices[0].RequiredValues[0], override)
	}

	_, err = store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &selected,
		Values:        map[workflow.ModelKey]map[string]string{"other": {"value": "unexpected"}},
	})
	if !errors.Is(err, ErrManualMoveValuesInvalid) {
		t.Fatalf("extra nested value error = %v, want values-invalid", err)
	}
	_, err = store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &selected,
		Values:        map[workflow.ModelKey]map[string]string{"extra": {}},
	})
	if !errors.Is(err, ErrManualMoveValuesInvalid) {
		t.Fatalf("extra empty value node error = %v, want values-invalid", err)
	}
	_, err = store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &selected,
		Values: map[workflow.ModelKey]map[string]string{
			"plan": {"prior_summary": strings.Repeat("x", workflow.MaxOutputValueBytes+1)},
		},
	})
	if !errors.Is(err, ErrManualMoveValuesInvalid) {
		t.Fatalf("oversized nested value error = %v, want values-invalid", err)
	}
}

func TestManualMovePreviewPrefillsPartiallyArrivedFanoutValuesBySourceNode(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	splitResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       started.Mutation.Created[0].Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "plan summary"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode split: %v", err)
	}
	branches := make(map[workflow.TransitionBranchKey]workflow.CurrentNode)
	for _, branch := range splitResult.Mutation.Created {
		key, ok := branch.Reference.TransitionBranchKey()
		if !ok {
			t.Fatalf("split branch = %+v, want branch scope", branch)
		}
		branches[key] = branch
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_a"].Reference,
		TransitionID: "join_a",
		OutputValues: map[string]string{"joined": "arrived from A"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode partial join: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "synth")

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
	})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeTransition || len(preview.Choices) != 1 {
		t.Fatalf("preview = %+v, want one synth Transition choice", preview)
	}
	choice := preview.Choices[0]
	if len(choice.RequiredValues) != 1 ||
		choice.RequiredValues[0].NodeKey != "impl_a" ||
		choice.RequiredValues[0].OutputName != "joined" ||
		choice.RequiredValues[0].ResolvedValue == nil ||
		*choice.RequiredValues[0].ResolvedValue != "arrived from A" {
		t.Fatalf("required values = %+v, want topology-mapped arrived value", choice.RequiredValues)
	}
}
