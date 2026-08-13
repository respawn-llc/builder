package workflowstore

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/invariant"
	"core/shared/runtimeids"
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
	auditID := testNodeID("node-audit-" + workflowID.String())
	auditGroupID := testTransitionGroupID("group-audit-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		synth := nodeByKey(t, def, "synth")
		doneGroupID := testTransitionGroupID("group-synth-done-" + workflowID.String())
		req.Nodes = append(req.Nodes, NodeRecord{
			ID:           auditID,
			WorkflowID:   workflowID,
			Key:          "audit",
			Kind:         workflow.NodeKindAgent,
			DisplayName:  "Audit",
			SubagentRole: "coder",
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
			ID:                testEdgeID("edge-audit-" + workflowID.String()),
			WorkflowID:        workflowID,
			TransitionGroupID: auditGroupID,
			Key:               "audit",
			TargetNodeID:      auditID,
			AssigneeSelection: workflow.AssigneeSelectionConfigured,
			ThinkingSelection: workflow.ThinkingSelectionConfigured,
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
		required.Description == nil ||
		*required.Description != "Joined branch summary." {
		t.Fatalf("required value = %+v, want described prior Join parameter", required)
	}
}

func TestManualMovePreviewRequiresAndHonorsStableTransitionSelection(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		plan := nodeByKey(t, def, "plan")
		target := nodeByKey(t, def, "implement")
		groupID := testTransitionGroupID("group-alternate-" + workflowID.String())
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           groupID,
			WorkflowID:   workflowID,
			SourceNodeID: workflow.NodeIDOf(plan),
			TransitionID: "alternate",
			DisplayName:  "Alternate",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                testEdgeID("edge-alternate-" + workflowID.String()),
			WorkflowID:        workflowID,
			TransitionGroupID: groupID,
			Key:               "alternate",
			TargetNodeID:      workflow.NodeIDOf(target),
			AssigneeSelection: workflow.AssigneeSelectionConfigured,
			ThinkingSelection: workflow.ThinkingSelectionConfigured,
			ContextMode:       workflow.ContextModeNewSession,
			PromptTemplate:    "Alternate {{.Params.prior_summary}}.",
			Parameters:        []workflow.Parameter{{Key: "prior_summary", Description: "Prior summary.", Purpose: workflow.ParameterPurposeOrdinary}},
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

func TestManualMovePlansNewSessionSelectorValuesWithoutCurrentNode(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, testEdgeID("edge-audit-"+workflowID.String()))
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	edge := edgeByKey(t, definition, "audit")
	group, err := transitionGroupForEdge(definition, edge)
	if err != nil {
		t.Fatalf("transitionGroupForEdge: %v", err)
	}
	source, err := currentNodeDefinitionNode(definition, group.SourceNodeID)
	if err != nil {
		t.Fatalf("source node: %v", err)
	}
	target, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
	if err != nil {
		t.Fatalf("target node: %v", err)
	}
	planned, err := store.planTransitionParameterContract(
		ctx,
		store.queries,
		definition,
		edge,
		source,
		target,
		nil,
		nil,
		true,
		true,
		transitionContractContextResolutionRequired,
	)
	if err != nil {
		t.Fatalf("planTransitionParameterContract: %v", err)
	}
	if len(planned.Parameters) != 1 ||
		planned.Parameters[0].Key != "role" ||
		planned.Parameters[0].Purpose != workflow.ParameterPurposeTargetAssignee {
		t.Fatalf("planned parameters = %+v, want exposed target role", planned.Parameters)
	}
}

func TestManualMovePreviewHidesAuthorizedSoleRoleSelection(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	store.roleResolver = completionTargetCatalog{
		roles: map[string]workflow.TargetAgentRole{
			"coder": {Identity: "coder", QuestionsEnabled: true},
		},
		selectable: []workflow.TargetAgentRole{
			{Identity: "reviewer", ExplicitAgentCallable: true},
		},
	}
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, testEdgeID("edge-audit-"+workflowID.String()))
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(nodeByKey(t, definition, "audit")),
		TransitionKey: func() *workflow.TransitionID {
			value := workflow.TransitionID("audit")
			return &value
		}(),
	})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	if len(preview.Choices) != 1 || len(preview.Choices[0].RequiredValues) != 1 ||
		preview.Choices[0].RequiredValues[0].OutputName != "summary" {
		t.Fatalf("manual move preview = %+v, want only ordinary summary", preview)
	}
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(nodeByKey(t, definition, "audit")),
		TransitionKey: func() *workflow.TransitionID {
			value := workflow.TransitionID("audit")
			return &value
		}(),
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := store.applyManualMoveWithoutAssignments(ctx, prepared, &ExecutionTargetCandidate{
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
	if len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].AgentExecutionSelection == nil ||
		moved.Mutation.Created[0].AgentExecutionSelection.Assignee != "reviewer" {
		t.Fatalf("manual move target = %+v, want sole reviewer", moved.Mutation.Created)
	}
	_ = review
}

func TestManualMoveAppliesAutomaticSoleRoleSelection(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	store.roleResolver = completionTargetCatalog{
		roles: map[string]workflow.TargetAgentRole{
			"coder": {Identity: "coder", QuestionsEnabled: true},
		},
		selectable: []workflow.TargetAgentRole{
			{Identity: "reviewer", ExplicitAgentCallable: true},
		},
	}
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, testEdgeID("edge-audit-"+workflowID.String()))
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	targetID := workflow.NodeIDOf(nodeByKey(t, definition, "audit"))
	transition := workflow.TransitionID("audit")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  targetID,
		TransitionKey: &transition,
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := store.applyManualMoveWithoutAssignments(ctx, prepared, &ExecutionTargetCandidate{
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
	if len(moved.Mutation.Created) != 1 {
		t.Fatalf("manual move mutation = %+v, want one target", moved.Mutation)
	}
	selection := moved.Mutation.Created[0].AgentExecutionSelection
	if selection == nil || selection.Assignee != "reviewer" ||
		selection.Origin != workflow.AssigneeOriginTransitionSelected {
		t.Fatalf("manual move selection = %+v, want automatic reviewer", selection)
	}
	_ = review
}

func TestManualMoveValidatesAndAppliesManyRoleSelection(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, testEdgeID("edge-audit-"+workflowID.String()))
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	targetID := workflow.NodeIDOf(nodeByKey(t, definition, "audit"))
	transition := workflow.TransitionID("audit")
	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  targetID,
		TransitionKey: &transition,
	})
	if err != nil {
		t.Fatalf("PreviewManualMove: %v", err)
	}
	foundRole := false
	for _, required := range preview.Choices[0].RequiredValues {
		if required.NodeKey == "review" && required.OutputName == "role" {
			foundRole = true
		}
	}
	if !foundRole {
		t.Fatalf("manual move required values = %+v, want role", preview.Choices[0].RequiredValues)
	}
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  targetID,
		TransitionKey: &transition,
		Values:        map[workflow.ModelKey]map[string]string{"review": {"role": "reviewer"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := store.applyManualMoveWithoutAssignments(ctx, prepared, &ExecutionTargetCandidate{
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
	selection := moved.Mutation.Created[0].AgentExecutionSelection
	if selection == nil || selection.Assignee != "reviewer" ||
		selection.Origin != workflow.AssigneeOriginTransitionSelected {
		t.Fatalf("manual move many-role selection = %+v, want reviewer", selection)
	}
	_ = review
}

func TestManualMovePreviewBlocksSerialDestinationInsideFanoutBranch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	detailAID := testNodeID("node-detail-a-" + workflowID.String())
	detailBID := testNodeID("node-detail-b-" + workflowID.String())
	joinAGroupID := testTransitionGroupID("group-join-a-" + workflowID.String())
	joinBGroupID := testTransitionGroupID("group-join-b-" + workflowID.String())
	detailAGroupID := testTransitionGroupID("group-detail-a-" + workflowID.String())
	detailBGroupID := testTransitionGroupID("group-detail-b-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		implA := nodeByKey(t, def, "impl_a")
		implB := nodeByKey(t, def, "impl_b")
		join := nodeByKey(t, def, "join")
		req.Nodes = append(req.Nodes,
			NodeRecord{ID: detailAID, WorkflowID: workflowID, Key: "detail_a", Kind: workflow.NodeKindAgent, DisplayName: "Detail A", SubagentRole: "coder"},
			NodeRecord{ID: detailBID, WorkflowID: workflowID, Key: "detail_b", Kind: workflow.NodeKindAgent, DisplayName: "Detail B", SubagentRole: "coder"},
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
			EdgeRecord{ID: testEdgeID("edge-detail-a-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: detailAGroupID, Key: "detail_a", TargetNodeID: detailAID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Detail A."},
			EdgeRecord{ID: testEdgeID("edge-detail-b-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: detailBGroupID, Key: "detail_b", TargetNodeID: detailBID, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Detail B."},
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

func TestManualMovePreviewReportsUnavailableImmediateContextForNonCurrentSource(t *testing.T) {
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
	done := nodeByKey(t, definition, "done")
	if _, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(done)}); err != nil {
		t.Fatalf("move to done: %v", err)
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

func TestManualMoveBackwardUsesRetainedImmediateSourceSession(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeContinueSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	sessionID := associateAndBindCurrentNodeSessionForTest(
		t, ctx, store, binding, cfg, started.Mutation.Created[0].Reference,
	)
	if _, err := store.CurrentTaskSessionForNode(ctx, started.Mutation.Created[0].Reference); err != nil {
		t.Fatalf("CurrentTaskSessionForNode before move to done: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKey(t, definition, "done")
	if _, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(done)}); err != nil {
		t.Fatalf("move to done: %v", err)
	}
	if _, err := store.CurrentTaskSessionForNode(ctx, started.Mutation.Created[0].Reference); err != nil {
		t.Fatalf("CurrentTaskSessionForNode after move to done: %v", err)
	}

	target := nodeByKey(t, definition, "implement")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values: map[workflow.ModelKey]map[string]string{
			"plan": {"prior_summary": "retained plan"},
		},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove backward: %v", err)
	}
	moved, err := store.applyManualMoveWithoutAssignments(ctx, prepared, &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: ExecutionTargetProvenanceResolved},
		Root:     ExecutionRoot{SourceWorkspaceID: binding.WorkspaceID, SourceWorkspaceRoot: binding.CanonicalRoot},
	})
	if err != nil {
		t.Fatalf("ApplyManualMove backward: %v", err)
	}
	if len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].SessionID == nil ||
		*moved.Mutation.Created[0].SessionID != sessionID {
		t.Fatalf("manual move = %+v, want retained source Session %q", moved, sessionID)
	}
}

func TestManualMoveCreatesFreshRetainedTargetAfterPlannedSourceBinds(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	plan := nodeByKey(t, definition, "plan")
	review := nodeByKey(t, definition, "review")
	audit := nodeByKey(t, definition, "audit")
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		reviewEdge := workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID)
		reviewEdge.ContextMode = workflow.ContextModeContinueSession
		reviewEdge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
		appendManualMoveRetainedReviewEdge(
			req,
			workflowID,
			audit,
			review,
			"post-bind",
			workflow.ContextSourcePreviousTarget,
		)
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	implementationA := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, implementationA.Reference)
	reviewResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       implementationA.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "implementation A"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode implementation A: %v", err)
	}
	firstReview := reviewResult.Mutation.Created[0]
	retainedReviewSessionID := associateAndBindCurrentNodeSessionForTest(
		t,
		ctx,
		store,
		binding,
		cfg,
		firstReview.Reference,
	)
	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source: firstReview.Reference, TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode Review: %v", err)
	}
	auditSessionID := associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, auditResult.Mutation.Created[0].Reference)
	implementationBMove := applyManualMoveFixture(t, ctx, store, binding, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(plan),
	})
	implementationB := implementationBMove.Mutation.Created[0]
	if implementationB.SessionID != nil {
		t.Fatalf("planned Implementation B = %+v, want unbound", implementationB)
	}
	associateAndBindCurrentNodeSessionForTest(
		t,
		ctx,
		store,
		binding,
		cfg,
		implementationB.Reference,
	)
	if _, err := store.CurrentTaskSessionForNode(ctx, firstReview.Reference); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("CurrentTaskSessionForNode Review after B binds = %v, want sql.ErrNoRows", err)
	}

	transitionKey := workflow.TransitionID("rework")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(review),
		TransitionKey: &transitionKey,
		Values: map[workflow.ModelKey]map[string]string{
			"plan":  {"summary": "implementation B"},
			"audit": {"summary": "implementation B"},
		},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove Review: %v", err)
	}
	moved, err := store.applyManualMoveWithoutAssignments(ctx, prepared, nil)
	if err != nil {
		t.Fatalf("ApplyManualMove Review: %v", err)
	}
	if len(moved.Mutation.Created) != 1 || moved.Mutation.Created[0].SessionID != nil {
		t.Fatalf(
			"manual Review = %+v, want fresh instead of historical %q",
			moved.Mutation.Created,
			retainedReviewSessionID,
		)
	}
	sourceID, ok := moved.Mutation.Created[0].ContinuationSource.ExactSessionID()
	if !ok || sourceID != auditSessionID {
		t.Fatalf("manual Review source = %q, %v; want selected Audit %q", sourceID, ok, auditSessionID)
	}
}

func TestManualMoveRetainedTargetUsesCurrentAssociationBeforePlannedSourceBinds(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	review := nodeByKey(t, definition, "review")
	audit := nodeByKey(t, definition, "audit")
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		reviewEdge := workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID)
		reviewEdge.ContextMode = workflow.ContextModeContinueSession
		reviewEdge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
		appendManualMoveRetainedReviewEdge(
			req,
			workflowID,
			audit,
			review,
			"unbound-source",
			workflow.ContextSourcePreviousTargetOrNew,
		)
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	implementationA := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	associateAndBindCurrentNodeSessionForTest(
		t, ctx, store, binding, cfg, implementationA.Reference,
	)
	reviewResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       implementationA.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "implementation A"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode implementation A: %v", err)
	}
	firstReview := reviewResult.Mutation.Created[0]
	associateAndBindCurrentNodeSessionForTest(
		t, ctx, store, binding, cfg, firstReview.Reference,
	)
	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source: firstReview.Reference, TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode Review: %v", err)
	}
	auditSessionID := associateAndBindCurrentNodeSessionForTest(
		t, ctx, store, binding, cfg, auditResult.Mutation.Created[0].Reference,
	)
	transitionKey := workflow.TransitionID("rework")
	reviewFromAudit := applyManualMoveFixture(t, ctx, store, binding, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(review),
		TransitionKey: &transitionKey,
		Values: map[workflow.ModelKey]map[string]string{
			"audit": {"summary": "audit A"},
		},
	}).Mutation.Created[0]
	if reviewFromAudit.SessionID != nil {
		t.Fatalf("planned Review from Audit = %+v, want fresh", reviewFromAudit)
	}
	retainedReviewSessionID := associateAndBindCurrentNodeSessionForTest(
		t, ctx, store, binding, cfg, reviewFromAudit.Reference,
	)
	replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		reviewFromAudit,
		workflow.NodeIDOf(audit),
		nil,
		workflow.DeferredSelfMaterializedContinuationSource(),
	)
	if _, err := store.db.ExecContext(ctx, `
UPDATE task_current_nodes
SET entered_by_edge_id = ?
WHERE task_id = ?
  AND transition_branch_key IS NULL`,
		testGraphEntityBlob(t, string(edgeByKey(t, definition, "audit").ID)),
		string(task.ID),
	); err != nil {
		t.Fatalf("set unbound Audit entering Edge: %v", err)
	}
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		reworkEdge := workflowGraphSaveEdgeRecord(
			t,
			req.Edges,
			*reviewFromAudit.EnteredByEdgeID,
		)
		reworkEdge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}
	})

	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(review),
		TransitionKey: &transitionKey,
		Values: map[workflow.ModelKey]map[string]string{
			"audit": {"summary": "audit B"},
		},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove Review: %v", err)
	}
	moved, err := store.applyManualMoveWithoutAssignments(ctx, prepared, nil)
	if err != nil {
		t.Fatalf("ApplyManualMove Review: %v", err)
	}
	if len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].SessionID == nil ||
		*moved.Mutation.Created[0].SessionID != retainedReviewSessionID {
		t.Fatalf("manual Review = %+v, want retained Review Session %q", moved.Mutation.Created, retainedReviewSessionID)
	}
	sourceID, exact := moved.Mutation.Created[0].ContinuationSource.ExactSessionID()
	if !exact || sourceID != auditSessionID {
		t.Fatalf("manual Review source = %q, %v; want retained Audit Session %q", sourceID, exact, auditSessionID)
	}
}

func TestManualMoveRetainedTargetStartsFreshWhenUnboundSourceHasNoHistory(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	review := nodeByKey(t, definition, "review")
	audit := nodeByKey(t, definition, "audit")
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		reviewEdge := workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID)
		reviewEdge.ContextMode = workflow.ContextModeContinueSession
		reviewEdge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
		appendManualMoveRetainedReviewEdge(
			req,
			workflowID,
			audit,
			review,
			"unbound-no-history",
			workflow.ContextSourcePreviousTarget,
		)
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, plan.Reference)
	reviewResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "implementation A"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode Plan: %v", err)
	}
	firstReview := reviewResult.Mutation.Created[0]
	retainedReviewSessionID := associateAndBindCurrentNodeSessionForTest(
		t, ctx, store, binding, cfg, firstReview.Reference,
	)
	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source: firstReview.Reference, TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode Review: %v", err)
	}
	replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		auditResult.Mutation.Created[0],
		workflow.NodeIDOf(audit),
		nil,
		workflow.DeferredSelfMaterializedContinuationSource(),
	)
	if _, err := store.db.ExecContext(ctx, `
UPDATE task_current_nodes
SET entered_by_edge_id = ?
WHERE task_id = ?
  AND transition_branch_key IS NULL`,
		testGraphEntityBlob(t, string(edgeByKey(t, definition, "audit").ID)),
		string(task.ID),
	); err != nil {
		t.Fatalf("set unbound Audit entering Edge: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE session_workflow_node_associations
SET association_status = 'historical'
WHERE task_id = ?
  AND node_id = ?`,
		string(task.ID),
		testGraphEntityBlob(t, string(workflow.NodeIDOf(audit))),
	); err != nil {
		t.Fatalf("remove Audit current association: %v", err)
	}

	transitionKey := workflow.TransitionID("rework")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(review),
		TransitionKey: &transitionKey,
		Values: map[workflow.ModelKey]map[string]string{
			"audit": {"summary": "audit B"},
		},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove Review: %v", err)
	}
	moved, err := store.applyManualMoveWithoutAssignments(ctx, prepared, noneManualMoveExecutionTargetCandidate(binding))
	if err != nil {
		t.Fatalf("ApplyManualMove Review: %v", err)
	}
	if len(moved.Mutation.Created) != 1 || moved.Mutation.Created[0].SessionID != nil {
		t.Fatalf("manual Review = %+v, want fresh instead of retained %q", moved.Mutation.Created, retainedReviewSessionID)
	}
	if moved.Mutation.Created[0].ContinuationSource.Kind() != workflow.MaterializedContinuationSourceDeferredSelf {
		t.Fatalf("manual Review source = %q, want deferred self", moved.Mutation.Created[0].ContinuationSource.Kind())
	}
}

func TestManualMoveStrictRetainedTargetWithoutHistoryFailsForUnboundSource(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	review := nodeByKey(t, definition, "review")
	audit := nodeByKey(t, definition, "audit")
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		appendManualMoveRetainedReviewEdge(
			req,
			workflowID,
			audit,
			review,
			"unbound-strict-no-target-history",
			workflow.ContextSourcePreviousTarget,
		)
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		workflow.NodeIDOf(audit),
		nil,
		workflow.DeferredSelfMaterializedContinuationSource(),
	)
	if _, err := store.db.ExecContext(ctx, `
UPDATE task_current_nodes
SET entered_by_edge_id = ?
WHERE task_id = ?
  AND transition_branch_key IS NULL`,
		testGraphEntityBlob(t, string(edgeByKey(t, definition, "audit").ID)),
		string(task.ID),
	); err != nil {
		t.Fatalf("set unbound Audit entering Edge: %v", err)
	}

	transitionKey := workflow.TransitionID("rework")
	_, err = store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(review),
		TransitionKey: &transitionKey,
		Values: map[workflow.ModelKey]map[string]string{
			"audit": {"summary": "audit"},
		},
	})
	var unavailable workflow.RetainedTargetUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("PrepareManualMove error = %T %v, want RetainedTargetUnavailableError", err, err)
	}
	currentNodes, listErr := store.ListCurrentNodes(ctx, task.ID)
	if listErr != nil {
		t.Fatalf("ListCurrentNodes: %v", listErr)
	}
	if len(currentNodes) != 1 || currentNodes[0].Reference.NodeID != workflow.NodeIDOf(audit) {
		t.Fatalf("Current Nodes after strict failure = %+v, want unchanged Audit", currentNodes)
	}
}

func TestManualMoveRetainedTargetWithoutHistoryFailsStrictAndCreatesFallbackWithoutInvariant(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	review := nodeByKey(t, definition, "review")
	audit := nodeByKey(t, definition, "audit")
	var reworkEdgeID workflow.EdgeID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		reworkEdgeID = appendManualMoveRetainedReviewEdge(
			req,
			workflowID,
			audit,
			review,
			"bypass",
			workflow.ContextSourcePreviousTargetOrNew,
		)
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	reviewResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "bypassed plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode Plan: %v", err)
	}
	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       reviewResult.Mutation.Created[0].Reference,
		TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode Review: %v", err)
	}
	origin := auditResult.Mutation.Created[0]
	associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, origin.Reference)
	transitionKey := workflow.TransitionID("rework")
	request := ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(review),
		TransitionKey: &transitionKey,
		Values: map[workflow.ModelKey]map[string]string{
			"plan":  {"summary": "bypassed review"},
			"audit": {"summary": "bypassed review"},
		},
	}
	preparedFallback, err := store.PrepareManualMove(ctx, request)
	if err != nil {
		t.Fatalf("PrepareManualMove fallback: %v", err)
	}
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		reworkEdge := workflowGraphSaveEdgeRecord(t, req.Edges, reworkEdgeID)
		reworkEdge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}
	})
	diagnostics := testsetup.CaptureSlog(t)
	_, err = store.PreviewManualMove(ctx, request)
	var unavailable workflow.RetainedTargetUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("PreviewManualMove error = %T %v, want RetainedTargetUnavailableError", err, err)
	}
	_, err = store.applyManualMoveWithoutAssignments(ctx, preparedFallback, noneManualMoveExecutionTargetCandidate(binding))
	if !errors.As(err, &unavailable) {
		t.Fatalf("ApplyManualMove error = %T %v, want RetainedTargetUnavailableError", err, err)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after strict failure: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(origin.Reference) {
		t.Fatalf("Current Nodes after strict failure = %+v, want unchanged Audit", currentNodes)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("strict ordinary unavailable diagnostics = %q, want none", diagnostics.String())
	}

	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		reworkEdge := workflowGraphSaveEdgeRecord(t, req.Edges, reworkEdgeID)
		reworkEdge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
	})
	preview, err := store.PreviewManualMove(ctx, request)
	if err != nil {
		t.Fatalf("PreviewManualMove fallback: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeTransition || len(preview.Choices) != 1 {
		t.Fatalf("fallback preview = %+v, want one transition", preview)
	}
	preparedFallback, err = store.PrepareManualMove(ctx, request)
	if err != nil {
		t.Fatalf("PrepareManualMove fallback retry: %v", err)
	}
	moved, err := store.applyManualMoveWithoutAssignments(ctx, preparedFallback, noneManualMoveExecutionTargetCandidate(binding))
	if err != nil {
		t.Fatalf("ApplyManualMove fallback: %v", err)
	}
	if len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].SessionID != nil ||
		moved.Mutation.Created[0].ContinuationSource.Kind() != workflow.MaterializedContinuationSourceExact {
		t.Fatalf("fallback manual move = %+v, want fresh target with selected-source proof", moved)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("fallback ordinary unavailable diagnostics = %q, want none", diagnostics.String())
	}
}

func TestManualMoveFanoutRetainedTargetUsesSerialAssociationDuringApply(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	sourceSessionID := associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, source.Reference)
	source.SessionID = &sourceSessionID
	exactSource, err := workflow.NewExactMaterializedContinuationSource(sourceSessionID)
	if err != nil {
		t.Fatalf("NewExactMaterializedContinuationSource: %v", err)
	}
	source.ContinuationSource = exactSource
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	for _, targetKey := range []string{"impl_a", "impl_b"} {
		target := nodeByKey(t, definition, targetKey)
		reference := replaceSerialCurrentNodeBindingFixture(t, ctx, store, source, workflow.NodeIDOf(target), nil, source.ContinuationSource)
		associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, reference)
		replaceSerialCurrentNodeBindingFixture(t, ctx, store, source, source.Reference.NodeID, source.SessionID, source.ContinuationSource)
	}
	for key, targetKey := range map[string]string{"split_a": "impl_a", "split_b": "impl_b"} {
		edge := edgeByKey(t, definition, key)
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}
		branchKey := workflow.TransitionBranchKey(edge.Key)
		resolved, err := resolveTransitionContext(
			ctx, store.queries, definition, edge, task.ID, &source, &branchKey,
			nodeByKey(t, definition, "plan"), nodeByKey(t, definition, targetKey), true,
		)
		if err != nil || resolved.targetSessionID() == nil {
			t.Fatalf("resolve manual move %s = %+v, %v; want retained serial Session", key, resolved, err)
		}
	}
}

func appendManualMoveRetainedReviewEdge(
	req *WorkflowGraphSaveRequest,
	workflowID runtimeids.WorkflowID,
	source workflow.Node,
	target workflow.Node,
	identity string,
	contextSource workflow.ContextSourceKind,
) workflow.EdgeID {
	groupID := testTransitionGroupID("group-manual-" + identity + "-rework-" + workflowID.String())
	edgeID := testEdgeID("edge-manual-" + identity + "-rework-" + workflowID.String())
	req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
		ID:           groupID,
		WorkflowID:   workflowID,
		SourceNodeID: workflow.NodeIDOf(source),
		TransitionID: "rework",
		DisplayName:  "Rework",
	})
	req.Edges = append(req.Edges, EdgeRecord{
		ID:                edgeID,
		WorkflowID:        workflowID,
		TransitionGroupID: groupID,
		Key:               "rework",
		TargetNodeID:      workflow.NodeIDOf(target),
		AssigneeSelection: workflow.AssigneeSelectionConfigured,
		ThinkingSelection: workflow.ThinkingSelectionConfigured,
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: contextSource},
		PromptTemplate:    "Review {{.Params.summary}}.",
		Parameters: []workflow.Parameter{{
			Key:         "summary",
			Description: "Review summary.",
			Purpose:     workflow.ParameterPurposeOrdinary,
		}},
	})
	return edgeID
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
	planSessionID := associateAndBindCurrentNodeSessionForTest(
		t,
		ctx,
		store,
		binding,
		cfg,
		started.Mutation.Created[0].Reference,
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
	moved, err := store.applyManualMoveWithoutAssignments(ctx, prepared, &ExecutionTargetCandidate{
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
		required.Description == nil ||
		*required.Description != "Prior summary." ||
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

func TestPreviewManualMoveFailsFastForUnresolvedLegacySourceInDebug(t *testing.T) {
	fixture := newReworkContextCompletionFixture(t, workflow.ContextSourcePreviousTarget)
	fixture.store.invariantPolicy = invariant.NewPolicy(invariant.WithMode(invariant.ModePanic))
	markCurrentNodeContinuationSourceLegacy(t, fixture)
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	review := nodeByKey(t, definition, "review")
	transitionKey := workflow.TransitionID("rework")

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("PreviewManualMove did not panic for unresolved legacy source in debug")
		}
	}()
	_, _ = fixture.store.PreviewManualMove(fixture.ctx, ManualMoveRequest{
		TaskID:        fixture.audit.Reference.TaskID,
		TargetNodeID:  workflow.NodeIDOf(review),
		TransitionKey: &transitionKey,
		Values: map[workflow.ModelKey]map[string]string{
			"audit": {"summary": "review again"},
		},
	})
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
