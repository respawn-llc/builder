package workflowstore

import (
	"context"
	"testing"

	"core/server/metadata"
	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestManualMoveRouterPreviewAndApplyPreservesRouteValues(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createManualMoveStaticReviewRouterWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("get workflow definition: %v", err)
	}

	t.Run("qa", func(t *testing.T) {
		task := createDefaultTask(t, ctx, store, binding.ProjectID)
		implementation := advanceStaticReviewTaskToImplementation(t, ctx, store, task.ID)
		completeManualMoveFixtureNode(t, ctx, store, CurrentNodeCompletionRequest{
			Source:       implementation.Reference,
			TransitionID: "implementation_ready",
			Commentary:   "Implementation is ready.",
		}, "complete implementation_ready")

		qa := nodeByKey(t, definition, "qa")
		preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(qa)})
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
		required := manualMoveRequiredValueMap(choice.RequiredValues)
		plan := required["scope_review.plan_file_path"]
		if plan.ResolvedValue == nil || *plan.ResolvedValue != "plans/KENT-477.md" ||
			plan.Description == nil || *plan.Description != "Plan file path." {
			t.Fatalf("plan route value = %+v, want resolved authored value", plan)
		}
		commentary := required["implementation.commentary"]
		if commentary.ResolvedValue == nil || *commentary.ResolvedValue != "Implementation is ready." ||
			commentary.Description != nil {
			t.Fatalf("commentary route value = %+v, want resolved absent description", commentary)
		}

		moved := applyManualMoveFixture(t, ctx, store, binding, ManualMoveRequest{
			TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(qa),
			Values: map[workflow.ModelKey]map[string]string{
				"scope_review":   {"plan_file_path": "plans/KENT-477.md"},
				"implementation": {"commentary": "Implementation is ready."},
			},
		})
		if moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(qa) {
			t.Fatalf("QA move = %+v, want applied QA current node", moved)
		}
	})

	t.Run("join", func(t *testing.T) {
		task := createDefaultTask(t, ctx, store, binding.ProjectID)
		implementation := advanceStaticReviewTaskToImplementation(t, ctx, store, task.ID)
		sessionID := associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, implementation.Reference)
		completeManualMoveFixtureNode(t, ctx, store, CurrentNodeCompletionRequest{
			Source:       implementation.Reference,
			TransitionID: "implementation_ready",
		}, "complete implementation_ready")

		target := nodeByKey(t, definition, "implementation")
		transition := workflow.TransitionID("code_review_rejected")
		preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{
			TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target), TransitionKey: &transition,
		})
		if err != nil {
			t.Fatalf("preview code_review_rejected manual move: %v", err)
		}
		if preview.Outcome != ManualMovePreviewOutcomeTransition || len(preview.Choices) != 1 {
			t.Fatalf("Implementation preview = %+v, want one transition choice", preview)
		}
		choice := preview.Choices[0]
		if choice.TransitionKey != "code_review_rejected" || len(choice.RequiredValues) != 3 {
			t.Fatalf("Implementation choice = %+v, want rejected transition with three values", choice)
		}
		required := manualMoveRequiredValueMap(choice.RequiredValues)
		for key, want := range map[string]string{
			"code_review_parallel_join.code_review_findings": "No code findings.",
			"code_review_parallel_join.compliance_findings":  "No compliance findings.",
			"scope_review.plan_file_path":                    "plans/KENT-477.md",
		} {
			value := required[key]
			if value.ResolvedValue == nil || *value.ResolvedValue != want {
				t.Fatalf("%s = %+v, want resolved %q", key, value, want)
			}
		}
		for key, want := range map[string]string{
			"code_review_parallel_join.code_review_findings": "Code review findings.",
			"code_review_parallel_join.compliance_findings":  "Compliance findings.",
		} {
			value := required[key]
			if value.Description == nil || *value.Description != want {
				t.Fatalf("%s description = %+v, want %q", key, value.Description, want)
			}
		}

		moved := applyManualMoveFixture(t, ctx, store, binding, ManualMoveRequest{
			TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target), TransitionKey: &transition,
			Values: map[workflow.ModelKey]map[string]string{
				"code_review_parallel_join": {
					"code_review_findings": "No code findings.",
					"compliance_findings":  "No compliance findings.",
				},
				"scope_review": {"plan_file_path": "plans/KENT-477.md"},
			},
		})
		if moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) ||
			moved.Mutation.Created[0].SessionID == nil ||
			*moved.Mutation.Created[0].SessionID != sessionID {
			t.Fatalf("Implementation move = %+v, want applied target with retained session %q", moved, sessionID)
		}
	})
}

func completeManualMoveFixtureNode(t *testing.T, ctx context.Context, store *Store, req CurrentNodeCompletionRequest, label string) CurrentNodeCompletionResult {
	t.Helper()
	result, err := store.CompleteCurrentNode(ctx, req)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	return result
}

func applyManualMoveFixture(t *testing.T, ctx context.Context, store *Store, binding metadata.Binding, req ManualMoveRequest) ManualMoveResult {
	prepared, err := store.PrepareManualMove(ctx, req)
	if err != nil {
		t.Fatalf("prepare manual move: %v", err)
	}
	moved, err := store.ApplyManualMove(ctx, prepared, noneManualMoveExecutionTargetCandidate(binding))
	if err != nil {
		t.Fatalf("apply manual move: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied || len(moved.Mutation.Created) != 1 {
		t.Fatalf("manual move = %+v, want one applied current node", moved)
	}
	return moved
}

func advanceStaticReviewTaskToImplementation(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID) workflow.CurrentNode {
	t.Helper()
	scope := startTask(t, ctx, store, taskID).Mutation.Created[0]
	completeManualMoveFixtureNode(t, ctx, store, CurrentNodeCompletionRequest{
		Source: scope.Reference, TransitionID: "plan_approved",
		OutputValues: map[string]string{"plan_file_path": "plans/KENT-477.md"},
	}, "complete plan_approved")
	nodes, err := store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("list plan checkpoint: %v", err)
	}
	split := completeManualMoveFixtureNode(t, ctx, store, CurrentNodeCompletionRequest{
		Source: nodes[0].Reference, TransitionID: "begin_review",
	}, "complete begin_review").Mutation.Created
	branches := make(map[workflow.TransitionBranchKey]workflow.CurrentNode, len(split))
	for _, node := range split {
		branch, ok := node.Reference.TransitionBranchKey()
		if !ok {
			t.Fatalf("review branch = %+v, want branch scope", node)
		}
		branches[branch] = node
	}
	completeManualMoveFixtureNode(t, ctx, store, CurrentNodeCompletionRequest{
		Source: branches["review_a"].Reference, TransitionID: "approve_findings_a",
		OutputValues: map[string]string{"code_review_findings": "No code findings."},
	}, "complete approve_findings_a")
	completeManualMoveFixtureNode(t, ctx, store, CurrentNodeCompletionRequest{
		Source: branches["review_b"].Reference, TransitionID: "approve_findings_b",
		OutputValues: map[string]string{"compliance_findings": "No compliance findings."},
	}, "complete approve_findings_b")
	nodes, err = store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("list implementation after Join: %v", err)
	}
	return nodes[0]
}

func manualMoveRequiredValueMap(values []ManualMoveRequiredValue) map[string]ManualMoveRequiredValue {
	required := make(map[string]ManualMoveRequiredValue, len(values))
	for _, value := range values {
		required[string(value.NodeKey)+"."+value.OutputName] = value
	}
	return required
}

func createManualMoveStaticReviewRouterWorkflow(t *testing.T, ctx context.Context, store *Store) runtimeids.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Manual Move Static Review Router"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	workflowID, suffix := created.ID, created.ID.String()
	nodeID := func(key string) workflow.NodeID { return testNodeID("node-" + key + "-" + suffix) }
	groupID := func(key string) workflow.TransitionGroupID {
		return testTransitionGroupID("group-" + key + "-" + suffix)
	}
	edgeID := func(key string) workflow.EdgeID { return testEdgeID("edge-" + key + "-" + suffix) }
	scope, plan, implementation := nodeID("scope-review"), nodeID("plan-checkpoint"), nodeID("implementation")
	reviewA, reviewB := nodeID("review-a"), nodeID("review-b")
	join, router, qa := nodeID("code-review-parallel-join"), nodeID("router"), nodeID("qa")
	start, planApproved := groupID("start"), groupID("plan-approved")
	beginReview, approveA, approveB := groupID("begin-review"), groupID("approve-findings-a"), groupID("approve-findings-b")
	approveReview, implementationReady := groupID("approve-review-findings"), groupID("implementation-ready")
	qaReady, rejected := groupID("qa-ready"), groupID("code-review-rejected")
	qaDone := groupID("qa-done")
	joinA, joinB := edgeID("approve-findings-a"), edgeID("approve-findings-b")

	agent := func(id workflow.NodeID, key, display string) NodeRecord {
		return NodeRecord{ID: id, WorkflowID: workflowID, Key: workflow.ModelKey(key), Kind: workflow.NodeKindAgent, DisplayName: display, SubagentRole: "coder"}
	}
	group := func(id workflow.TransitionGroupID, source workflow.NodeID, transition, display string) TransitionGroupRecord {
		return TransitionGroupRecord{ID: id, WorkflowID: workflowID, SourceNodeID: source, TransitionID: workflow.TransitionID(transition), DisplayName: display}
	}
	edge := func(id workflow.EdgeID, group workflow.TransitionGroupID, key string, target workflow.NodeID, mode workflow.ContextMode, prompt string) EdgeRecord {
		return EdgeRecord{ID: id, WorkflowID: workflowID, TransitionGroupID: group, Key: workflow.ModelKey(key), TargetNodeID: target, ContextMode: mode, PromptTemplate: prompt, AssigneeSelection: workflow.AssigneeSelectionConfigured, ThinkingSelection: workflow.ThinkingSelectionConfigured}
	}

	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		startNode, done := nodeByKind(t, def, workflow.NodeKindStart), nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes,
			agent(scope, "scope_review", "Scope Review"), agent(plan, "plan_checkpoint", "Plan Checkpoint"),
			agent(implementation, "implementation", "Implementation"), agent(reviewA, "review_a", "Review A"),
			agent(reviewB, "review_b", "Review B"),
			NodeRecord{ID: join, WorkflowID: workflowID, Key: "code_review_parallel_join", Kind: workflow.NodeKindJoin, DisplayName: "Code Review Parallel Join", JoinInputProviders: []workflow.JoinInputProvider{
				{InputName: "code_review_findings", ProviderEdgeID: joinA}, {InputName: "compliance_findings", ProviderEdgeID: joinB},
			}},
			NodeRecord{ID: router, WorkflowID: workflowID, Key: "router", Kind: workflow.NodeKindScript, DisplayName: "Router", ScriptPath: "scripts/static_review_router.sh"},
			agent(qa, "qa", "QA"),
		)
		req.TransitionGroups = append(req.TransitionGroups,
			group(start, workflow.NodeIDOf(startNode), "start", "Start"),
			group(planApproved, scope, "plan_approved", "Plan Approved"),
			group(beginReview, plan, "begin_review", "Begin Review"),
			group(approveA, reviewA, "approve_findings_a", "Approve A"),
			group(approveB, reviewB, "approve_findings_b", "Approve B"),
			group(approveReview, join, "approve_review_findings", "Approve Review Findings"),
			group(implementationReady, implementation, "implementation_ready", "Implementation Ready"),
			group(qaReady, router, "qa_ready", "QA Ready"),
			group(rejected, router, "code_review_rejected", "Code Review Rejected"),
			group(qaDone, qa, "qa_done", "Done"),
		)
		planEdge := edge(edgeID("plan-approved"), planApproved, "plan_approved", plan, workflow.ContextModeNewSession, "Check {{.Params.plan_file_path}}.")
		planEdge.Parameters = []workflow.Parameter{{Key: "plan_file_path", Description: "Plan file path.", Purpose: workflow.ParameterPurposeOrdinary}}
		codeEdge := edge(joinA, approveA, "approve_findings_a", join, workflow.ContextModeNewSession, "")
		codeEdge.Parameters = []workflow.Parameter{{Key: "code_review_findings", Description: "Code review findings.", Purpose: workflow.ParameterPurposeOrdinary}}
		complianceEdge := edge(joinB, approveB, "approve_findings_b", join, workflow.ContextModeNewSession, "")
		complianceEdge.Parameters = []workflow.Parameter{{Key: "compliance_findings", Description: "Compliance findings.", Purpose: workflow.ParameterPurposeOrdinary}}
		qaEdge := edge(edgeID("qa-ready"), qaReady, "qa_ready", qa, workflow.ContextModeContinueSession, "QA {{.Params.plan_approved.plan_file_path}} {{.Params.implementation_ready.commentary}}.")
		qaEdge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
		rejectedEdge := edge(edgeID("code-review-rejected"), rejected, "code_review_rejected", implementation, workflow.ContextModeContinueSession, "Rework {{.Params.approve_review_findings.code_review_findings}} {{.Params.approve_review_findings.compliance_findings}}.")
		rejectedEdge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}
		req.Edges = append(req.Edges,
			edge(edgeID("start"), start, "start", scope, workflow.ContextModeNewSession, "Review scope."),
			planEdge,
			edge(edgeID("begin-review-a"), beginReview, "review_a", reviewA, workflow.ContextModeNewSession, "Review code."),
			edge(edgeID("begin-review-b"), beginReview, "review_b", reviewB, workflow.ContextModeNewSession, "Review compliance."),
			codeEdge, complianceEdge,
			edge(edgeID("approve-review-findings"), approveReview, "approve_review_findings", implementation, workflow.ContextModeNewSession, "Review {{.Params.code_review_findings}} {{.Params.compliance_findings}}."),
			edge(edgeID("implementation-ready"), implementationReady, "implementation_ready", router, workflow.ContextModeNewSession, ""),
			qaEdge, rejectedEdge,
			edge(edgeID("done-qa"), qaDone, "done", workflow.NodeIDOf(done), workflow.ContextModeNewSession, ""),
		)
	})
	return workflowID
}
