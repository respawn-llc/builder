package workflowstore

import (
	"context"
	"testing"

	"core/server/metadata"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/runtimeids"
)

func TestManualMoveRouterPreviewAndApplyPreservesRouteValues(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createManualMoveStaticReviewRouterWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)

	t.Run("qa route", func(t *testing.T) {
		testManualMoveQARoute(t, ctx, store, binding, workflowID)
	})
	t.Run("join route", func(t *testing.T) {
		testManualMoveJoinRoute(t, ctx, store, binding, cfg, workflowID)
	})
}

func advanceStaticReviewTaskToImplementation(
	t *testing.T,
	ctx context.Context,
	store *Store,
	taskID workflow.TaskID,
	codeFindings string,
	complianceFindings string,
) workflow.CurrentNode {
	t.Helper()
	scopeReview := startTask(t, ctx, store, taskID).Mutation.Created[0]
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       scopeReview.Reference,
		TransitionID: "plan_approved",
		OutputValues: map[string]string{"plan_file_path": "plans/KENT-477.md"},
	}); err != nil {
		t.Fatalf("complete plan_approved: %v", err)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("list plan checkpoint current node: %v", err)
	}
	split, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       currentNodes[0].Reference,
		TransitionID: "begin_review",
	})
	if err != nil {
		t.Fatalf("complete begin_review: %v", err)
	}
	branches := make(map[workflow.TransitionBranchKey]workflow.CurrentNode)
	for _, currentNode := range split.Mutation.Created {
		branch, ok := currentNode.Reference.TransitionBranchKey()
		if !ok {
			t.Fatalf("review branch = %+v, want branch scope", currentNode)
		}
		branches[branch] = currentNode
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["review_a"].Reference,
		TransitionID: "approve_findings_a",
		OutputValues: map[string]string{"code_review_findings": codeFindings},
	}); err != nil {
		t.Fatalf("complete approve_findings_a: %v", err)
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["review_b"].Reference,
		TransitionID: "approve_findings_b",
		OutputValues: map[string]string{"compliance_findings": complianceFindings},
	}); err != nil {
		t.Fatalf("complete approve_findings_b: %v", err)
	}
	currentNodes, err = store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("list implementation after Join: %v", err)
	}
	if len(currentNodes) != 1 {
		t.Fatalf("current nodes after Join = %+v, want implementation", currentNodes)
	}
	return currentNodes[0]
}

func testManualMoveQARoute(t *testing.T, ctx context.Context, store *Store, binding metadata.Binding, workflowID runtimeids.WorkflowID) {
	t.Helper()
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	implementation := advanceStaticReviewTaskToImplementation(t, ctx, store, task.ID, "No code findings.", "No compliance findings.")
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       implementation.Reference,
		TransitionID: "implementation_ready",
		Commentary:   "Implementation is ready.",
	}); err != nil {
		t.Fatalf("complete implementation_ready: %v", err)
	}

	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("get workflow definition: %v", err)
	}
	qa := nodeByKey(t, definition, "qa")
	router := nodeByKey(t, definition, "router")
	routerNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("list router current node: %v", err)
	}
	if len(routerNodes) != 1 || routerNodes[0].Reference.NodeID != workflow.NodeIDOf(router) {
		t.Fatalf("router current nodes = %+v, want one router node", routerNodes)
	}

	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(qa),
	})
	if err != nil {
		t.Fatalf("preview QA manual move: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeTransition || len(preview.Choices) != 1 {
		t.Fatalf("QA preview = %+v, want one transition choice", preview)
	}
	choice := preview.Choices[0]
	if choice.TransitionKey != "qa_ready" || len(choice.RequiredValues) != 2 {
		t.Fatalf("QA choice = %+v, want qa_ready with two required values", choice)
	}
	required := make(map[string]ManualMoveRequiredValue, len(choice.RequiredValues))
	for _, value := range choice.RequiredValues {
		required[string(value.NodeKey)+"."+value.OutputName] = value
	}
	planValue := required["scope_review.plan_file_path"]
	if planValue.ResolvedValue == nil ||
		*planValue.ResolvedValue != "plans/KENT-477.md" ||
		planValue.Description == nil ||
		*planValue.Description != "Plan file path." {
		t.Fatalf("plan route value = %+v, want resolved authored value", planValue)
	}
	commentaryValue := required["implementation.commentary"]
	if commentaryValue.ResolvedValue == nil ||
		*commentaryValue.ResolvedValue != "Implementation is ready." ||
		commentaryValue.Description != nil {
		t.Fatalf("commentary route value = %+v, want resolved absent description", commentaryValue)
	}

	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(qa),
		Values: map[workflow.ModelKey]map[string]string{
			"scope_review":   {"plan_file_path": "plans/KENT-477.md"},
			"implementation": {"commentary": "Implementation is ready."},
		},
	})
	if err != nil {
		t.Fatalf("prepare QA manual move: %v", err)
	}
	moved, err := store.ApplyManualMove(ctx, prepared, noneManualMoveExecutionTargetCandidate(binding))
	if err != nil {
		t.Fatalf("apply QA manual move: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied ||
		len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(qa) {
		t.Fatalf("QA move = %+v, want applied QA current node", moved)
	}
}

func testManualMoveJoinRoute(t *testing.T, ctx context.Context, store *Store, binding metadata.Binding, cfg config.App, workflowID runtimeids.WorkflowID) {
	t.Helper()
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	implementation := advanceStaticReviewTaskToImplementation(t, ctx, store, task.ID, "No code findings.", "No compliance findings.")
	sessionID := associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, implementation.Reference)
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       implementation.Reference,
		TransitionID: "implementation_ready",
	}); err != nil {
		t.Fatalf("complete implementation_ready: %v", err)
	}

	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("get workflow definition: %v", err)
	}
	router := nodeByKey(t, definition, "router")
	routerNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("list router current node: %v", err)
	}
	if len(routerNodes) != 1 || !routerNodes[0].Reference.Equal(workflow.CurrentNodeReference{
		TaskID: task.ID,
		NodeID: workflow.NodeIDOf(router),
	}) {
		t.Fatalf("router current nodes = %+v, want one router node", routerNodes)
	}

	target := nodeByKey(t, definition, "implementation")
	transition := workflow.TransitionID("code_review_rejected")
	preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &transition,
	})
	if err != nil {
		t.Fatalf("preview code_review_rejected manual move: %v", err)
	}
	if preview.Outcome != ManualMovePreviewOutcomeTransition || len(preview.Choices) != 1 {
		t.Fatalf("Implementation preview = %+v, want one transition choice", preview)
	}
	choice := preview.Choices[0]
	if choice.TransitionKey != "code_review_rejected" || len(choice.RequiredValues) != 3 {
		t.Fatalf("Implementation choice transition=%q required_values=%#v, want rejected transition with three values", choice.TransitionKey, choice.RequiredValues)
	}
	required := make(map[string]ManualMoveRequiredValue, len(choice.RequiredValues))
	for _, value := range choice.RequiredValues {
		required[string(value.NodeKey)+"."+value.OutputName] = value
	}
	codeFindings := required["code_review_parallel_join.code_review_findings"]
	if codeFindings.ResolvedValue == nil ||
		*codeFindings.ResolvedValue != "No code findings." ||
		codeFindings.Description == nil ||
		*codeFindings.Description != "Code review findings." {
		t.Fatalf("code findings route value = %+v, want resolved authored Join value", codeFindings)
	}
	complianceFindings := required["code_review_parallel_join.compliance_findings"]
	if complianceFindings.ResolvedValue == nil ||
		*complianceFindings.ResolvedValue != "No compliance findings." ||
		complianceFindings.Description == nil ||
		*complianceFindings.Description != "Compliance findings." {
		t.Fatalf("compliance findings route value = %+v, want resolved authored Join value", complianceFindings)
	}
	planValue := required["scope_review.plan_file_path"]
	if planValue.ResolvedValue == nil || *planValue.ResolvedValue != "plans/KENT-477.md" {
		t.Fatalf("plan route value = %+v, want resolved prior value", planValue)
	}

	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &transition,
		Values: map[workflow.ModelKey]map[string]string{
			"code_review_parallel_join": {
				"code_review_findings": "No code findings.",
				"compliance_findings":  "No compliance findings.",
			},
			"scope_review": {"plan_file_path": "plans/KENT-477.md"},
		},
	})
	if err != nil {
		t.Fatalf("prepare code_review_rejected manual move: %v", err)
	}
	moved, err := store.ApplyManualMove(ctx, prepared, noneManualMoveExecutionTargetCandidate(binding))
	if err != nil {
		t.Fatalf("apply code_review_rejected manual move: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied ||
		len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) {
		t.Fatalf("Implementation move = %+v, want applied target", moved)
	}
	if moved.Mutation.Created[0].SessionID == nil || *moved.Mutation.Created[0].SessionID != sessionID {
		t.Fatalf("Implementation move = %+v, want retained previous-target session %q", moved, sessionID)
	}
}

func createManualMoveStaticReviewRouterWorkflow(t *testing.T, ctx context.Context, store *Store) runtimeids.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Manual Move Static Review Router"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	workflowID := created.ID
	scopeReviewID := workflow.NodeID("node-scope-review-" + workflowID.String())
	planCheckpointID := workflow.NodeID("node-plan-checkpoint-" + workflowID.String())
	implementationID := workflow.NodeID("node-implementation-" + workflowID.String())
	reviewAID := workflow.NodeID("node-review-a-" + workflowID.String())
	reviewBID := workflow.NodeID("node-review-b-" + workflowID.String())
	joinID := workflow.NodeID("node-code-review-parallel-join-" + workflowID.String())
	routerID := workflow.NodeID("node-router-" + workflowID.String())
	qaID := workflow.NodeID("node-qa-" + workflowID.String())
	startGroupID := workflow.TransitionGroupID("group-start-" + workflowID.String())
	planApprovedGroupID := workflow.TransitionGroupID("group-plan-approved-" + workflowID.String())
	beginReviewGroupID := workflow.TransitionGroupID("group-begin-review-" + workflowID.String())
	approveAGroupID := workflow.TransitionGroupID("group-approve-findings-a-" + workflowID.String())
	approveBGroupID := workflow.TransitionGroupID("group-approve-findings-b-" + workflowID.String())
	approveReviewGroupID := workflow.TransitionGroupID("group-approve-review-findings-" + workflowID.String())
	implementationReadyGroupID := workflow.TransitionGroupID("group-implementation-ready-" + workflowID.String())
	qaReadyGroupID := workflow.TransitionGroupID("group-qa-ready-" + workflowID.String())
	rejectedGroupID := workflow.TransitionGroupID("group-code-review-rejected-" + workflowID.String())
	doneImplementationGroupID := workflow.TransitionGroupID("group-implementation-done-" + workflowID.String())
	doneQAGroupID := workflow.TransitionGroupID("group-qa-done-" + workflowID.String())
	joinAEdgeID := workflow.EdgeID("edge-approve-findings-a-" + workflowID.String())
	joinBEdgeID := workflow.EdgeID("edge-approve-findings-b-" + workflowID.String())

	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes,
			NodeRecord{ID: scopeReviewID, WorkflowID: workflowID, Key: "scope_review", Kind: workflow.NodeKindAgent, DisplayName: "Scope Review", SubagentRole: "coder"},
			NodeRecord{ID: planCheckpointID, WorkflowID: workflowID, Key: "plan_checkpoint", Kind: workflow.NodeKindAgent, DisplayName: "Plan Checkpoint", SubagentRole: "coder"},
			NodeRecord{ID: implementationID, WorkflowID: workflowID, Key: "implementation", Kind: workflow.NodeKindAgent, DisplayName: "Implementation", SubagentRole: "coder"},
			NodeRecord{ID: reviewAID, WorkflowID: workflowID, Key: "review_a", Kind: workflow.NodeKindAgent, DisplayName: "Review A", SubagentRole: "coder"},
			NodeRecord{ID: reviewBID, WorkflowID: workflowID, Key: "review_b", Kind: workflow.NodeKindAgent, DisplayName: "Review B", SubagentRole: "coder"},
			NodeRecord{ID: joinID, WorkflowID: workflowID, Key: "code_review_parallel_join", Kind: workflow.NodeKindJoin, DisplayName: "Code Review Parallel Join", JoinInputProviders: []workflow.JoinInputProvider{
				{InputName: "code_review_findings", ProviderEdgeID: joinAEdgeID},
				{InputName: "compliance_findings", ProviderEdgeID: joinBEdgeID},
			}},
			NodeRecord{ID: routerID, WorkflowID: workflowID, Key: "router", Kind: workflow.NodeKindScript, DisplayName: "Router", ScriptPath: "scripts/static_review_router.sh"},
			NodeRecord{ID: qaID, WorkflowID: workflowID, Key: "qa", Kind: workflow.NodeKindAgent, DisplayName: "QA", SubagentRole: "coder"},
		)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroupID, WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: planApprovedGroupID, WorkflowID: workflowID, SourceNodeID: scopeReviewID, TransitionID: "plan_approved", DisplayName: "Plan Approved"},
			TransitionGroupRecord{ID: beginReviewGroupID, WorkflowID: workflowID, SourceNodeID: planCheckpointID, TransitionID: "begin_review", DisplayName: "Begin Review"},
			TransitionGroupRecord{ID: approveAGroupID, WorkflowID: workflowID, SourceNodeID: reviewAID, TransitionID: "approve_findings_a", DisplayName: "Approve A"},
			TransitionGroupRecord{ID: approveBGroupID, WorkflowID: workflowID, SourceNodeID: reviewBID, TransitionID: "approve_findings_b", DisplayName: "Approve B"},
			TransitionGroupRecord{ID: approveReviewGroupID, WorkflowID: workflowID, SourceNodeID: joinID, TransitionID: "approve_review_findings", DisplayName: "Approve Review Findings"},
			TransitionGroupRecord{ID: implementationReadyGroupID, WorkflowID: workflowID, SourceNodeID: implementationID, TransitionID: "implementation_ready", DisplayName: "Implementation Ready"},
			TransitionGroupRecord{ID: qaReadyGroupID, WorkflowID: workflowID, SourceNodeID: routerID, TransitionID: "qa_ready", DisplayName: "QA Ready"},
			TransitionGroupRecord{ID: rejectedGroupID, WorkflowID: workflowID, SourceNodeID: routerID, TransitionID: "code_review_rejected", DisplayName: "Code Review Rejected"},
			TransitionGroupRecord{ID: doneImplementationGroupID, WorkflowID: workflowID, SourceNodeID: implementationID, TransitionID: "implementation_done", DisplayName: "Done"},
			TransitionGroupRecord{ID: doneQAGroupID, WorkflowID: workflowID, SourceNodeID: qaID, TransitionID: "qa_done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-start-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: scopeReviewID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review scope."},
			EdgeRecord{ID: workflow.EdgeID("edge-plan-approved-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: planApprovedGroupID, Key: "plan_approved", TargetNodeID: planCheckpointID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Check {{.Params.plan_file_path}}.", Parameters: []workflow.Parameter{{Key: "plan_file_path", Description: "Plan file path."}}},
			EdgeRecord{ID: workflow.EdgeID("edge-begin-review-a-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: beginReviewGroupID, Key: "review_a", TargetNodeID: reviewAID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review code."},
			EdgeRecord{ID: workflow.EdgeID("edge-begin-review-b-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: beginReviewGroupID, Key: "review_b", TargetNodeID: reviewBID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review compliance."},
			EdgeRecord{ID: joinAEdgeID, WorkflowID: workflowID, TransitionGroupID: approveAGroupID, Key: "approve_findings_a", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "code_review_findings", Description: "Code review findings."}}},
			EdgeRecord{ID: joinBEdgeID, WorkflowID: workflowID, TransitionGroupID: approveBGroupID, Key: "approve_findings_b", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "compliance_findings", Description: "Compliance findings."}}},
			EdgeRecord{ID: workflow.EdgeID("edge-approve-review-findings-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: approveReviewGroupID, Key: "approve_review_findings", TargetNodeID: implementationID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review {{.Params.code_review_findings}} {{.Params.compliance_findings}}."},
			EdgeRecord{ID: workflow.EdgeID("edge-implementation-ready-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: implementationReadyGroupID, Key: "implementation_ready", TargetNodeID: routerID, ContextMode: workflow.ContextModeNewSession},
			EdgeRecord{ID: workflow.EdgeID("edge-qa-ready-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: qaReadyGroupID, Key: "qa_ready", TargetNodeID: qaID, ContextMode: workflow.ContextModeContinueSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}, PromptTemplate: "QA {{.Params.plan_approved.plan_file_path}} {{.Params.implementation_ready.commentary}}."},
			EdgeRecord{ID: workflow.EdgeID("edge-code-review-rejected-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: rejectedGroupID, Key: "code_review_rejected", TargetNodeID: implementationID, ContextMode: workflow.ContextModeContinueSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}, PromptTemplate: "Rework {{.Params.approve_review_findings.code_review_findings}} {{.Params.approve_review_findings.compliance_findings}}."},
			EdgeRecord{ID: workflow.EdgeID("edge-done-implementation-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: doneImplementationGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
			EdgeRecord{ID: workflow.EdgeID("edge-done-qa-" + workflowID.String()), WorkflowID: workflowID, TransitionGroupID: doneQAGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	return workflowID
}
