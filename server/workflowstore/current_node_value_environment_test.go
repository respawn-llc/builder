package workflowstore

import (
	"context"
	"testing"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestCompleteCurrentNodeMaterializesChainedInputsAndPriorTransitionParameters(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	started := startTask(t, ctx, store, task.ID)
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v, want plan current node", started.Mutation)
	}
	reviewResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       started.Mutation.Created[0].Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "approved plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode plan: %v", err)
	}
	if len(reviewResult.Mutation.Created) != 1 {
		t.Fatalf("plan completion mutation = %+v, want review current node", reviewResult.Mutation)
	}
	review := reviewResult.Mutation.Created[0]
	if review.CurrentInputValues["summary"] != "approved plan" {
		t.Fatalf("review current inputs = %+v, want materialized summary", review.CurrentInputValues)
	}
	if review.PriorValues.TransitionParameters["review"]["summary"] != "approved plan" {
		t.Fatalf("review prior Transition parameters = %+v, want review transition summary retained for downstream audit", review.PriorValues)
	}

	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       review.Reference,
		TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode review: %v", err)
	}
	if len(auditResult.Mutation.Created) != 1 {
		t.Fatalf("review completion mutation = %+v, want audit current node", auditResult.Mutation)
	}
	audit := auditResult.Mutation.Created[0]
	if audit.PriorValues.TransitionParameters["review"]["summary"] != "approved plan" {
		t.Fatalf("audit prior Transition parameters = %+v, want review transition summary carried from review current node", audit.PriorValues)
	}
	startContext, err := store.ResolveCurrentNodeStartContext(ctx, audit.Reference)
	if err != nil {
		t.Fatalf("ResolveCurrentNodeStartContext audit: %v", err)
	}
	if startContext.CurrentNode.PriorValues.TransitionParameters["review"]["summary"] != "approved plan" {
		t.Fatalf("audit start prior Transition parameters = %+v, want review transition namespace", startContext.CurrentNode.PriorValues)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(audit.Reference) ||
		currentNodes[0].PriorValues.TransitionParameters["review"]["summary"] != "approved plan" {
		t.Fatalf("current nodes = %+v, want audit-owned materialized values", currentNodes)
	}
}

func TestCompleteCurrentNodeMaterializesCurrentAndPriorTransitionCommentary(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveEdgeRecord(
			t,
			req.Edges,
			testEdgeID("edge-review-"+workflowID.String()),
		).PromptTemplate = "Review {{.Params.commentary}}."
		workflowGraphSaveEdgeRecord(
			t,
			req.Edges,
			testEdgeID("edge-audit-"+workflowID.String()),
		).PromptTemplate = "Audit {{.Params.review.commentary}}."
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	reviewResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "approved plan"},
		Commentary:   "plan handoff",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode plan: %v", err)
	}
	review := reviewResult.Mutation.Created[0]
	if review.CurrentInputValues[workflow.RuntimePromptParameterCommentary] != "plan handoff" {
		t.Fatalf("review current inputs = %+v, want direct commentary", review.CurrentInputValues)
	}
	if review.PriorValues.TransitionParameters["review"][workflow.RuntimePromptParameterCommentary] != "plan handoff" {
		t.Fatalf("review prior Transition values = %+v, want review commentary", review.PriorValues)
	}
	reviewStart, err := store.ResolveCurrentNodeStartContext(ctx, review.Reference)
	if err != nil {
		t.Fatalf("ResolveCurrentNodeStartContext review: %v", err)
	}
	if reviewStart.ParameterValues[workflow.RuntimePromptParameterCommentary] != "plan handoff" {
		t.Fatalf("review start parameters = %+v, want direct commentary", reviewStart.ParameterValues)
	}

	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       review.Reference,
		TransitionID: "audit",
		Commentary:   "review handoff",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode review: %v", err)
	}
	audit := auditResult.Mutation.Created[0]
	if audit.CurrentInputValues[workflow.RuntimePromptParameterCommentary] != "review handoff" {
		t.Fatalf("audit current inputs = %+v, want direct commentary", audit.CurrentInputValues)
	}
	if audit.PriorValues.TransitionParameters["review"][workflow.RuntimePromptParameterCommentary] != "plan handoff" {
		t.Fatalf("audit prior Transition values = %+v, want review commentary", audit.PriorValues)
	}
}

func TestCompleteCurrentNodeRecoversEnteringTransitionParameterFromCurrentInput(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	reviewResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "approved plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode plan: %v", err)
	}
	review := reviewResult.Mutation.Created[0]
	if review.CurrentInputValues["summary"] != "approved plan" {
		t.Fatalf("review current inputs = %+v, want entering Transition Parameter", review.CurrentInputValues)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE task_current_nodes
SET prior_node_values_json = '{"transition_parameters":{}}'
WHERE task_id = ?
  AND node_id = ?
  AND transition_branch_key IS NULL`,
		string(review.Reference.TaskID),
		testGraphEntityBlob(t, string(review.Reference.NodeID)),
	); err != nil {
		t.Fatalf("simulate Current Node missing its entering Transition namespace: %v", err)
	}

	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       review.Reference,
		TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode review with recoverable entering Transition Parameter: %v", err)
	}
	if len(auditResult.Mutation.Created) != 1 {
		t.Fatalf("review completion mutation = %+v, want audit current node", auditResult.Mutation)
	}
	audit := auditResult.Mutation.Created[0]
	if audit.PriorValues.TransitionParameters["review"]["summary"] != "approved plan" {
		t.Fatalf("audit prior Transition values = %+v, want recovered review summary", audit.PriorValues)
	}
}

func TestCompleteCurrentNodeUsesTransitionParametersInsteadOfTargetInputFields(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveEdgeRecord(
			t,
			req.Edges,
			testEdgeID("edge-review-"+workflowID.String()),
		).PromptTemplate = "Review {{.Params.summary}}."
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "approved plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode with transition parameter contract: %v", err)
	}
	if len(completed.Mutation.Created) != 1 {
		t.Fatalf("completion mutation = %+v, want review current node", completed.Mutation)
	}
	review := completed.Mutation.Created[0]
	if review.CurrentInputValues["summary"] != "approved plan" {
		t.Fatalf("review current inputs = %+v, want transition parameter materialized", review.CurrentInputValues)
	}
	if _, exists := review.CurrentInputValues["changes"]; exists {
		t.Fatalf("review current inputs = %+v, do not want target input field to replace transition contract", review.CurrentInputValues)
	}
}

func TestCompleteCurrentNodePreservesPathSpecificPriorParametersAcrossLoop(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		review := nodeByKey(t, def, "review")
		audit := nodeByKey(t, def, "audit")
		auditEdge := workflowGraphSaveEdgeRecord(
			t,
			req.Edges,
			testEdgeID("edge-audit-"+workflowID.String()),
		)
		auditEdge.PromptTemplate = "Audit {{.Params.summary}}."
		auditEdge.Parameters = []workflow.Parameter{{
			Key:         "summary",
			Description: "Audit findings.",
		}}
		reworkGroupID := testTransitionGroupID("group-rework-" + workflowID.String())
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           reworkGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: workflow.NodeIDOf(audit),
			TransitionID: "rework",
			DisplayName:  "Rework",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                testEdgeID("edge-rework-" + workflowID.String()),
			WorkflowID:        workflowID,
			TransitionGroupID: reworkGroupID,
			Key:               "rework",
			TargetNodeID:      workflow.NodeIDOf(review),
			ContextMode:       workflow.ContextModeNewSession,
			PromptTemplate:    "Rework {{.Params.audit.summary}}.",
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "approved plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode plan: %v", err)
	}
	audit, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
		OutputValues: map[string]string{"summary": "blocking findings"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode review: %v", err)
	}
	if audit.Mutation.Created[0].PriorValues.TransitionParameters["audit"]["summary"] != "blocking findings" {
		t.Fatalf("audit prior values = %+v, want path-specific findings", audit.Mutation.Created[0].PriorValues)
	}
	reworked, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       audit.Mutation.Created[0].Reference,
		TransitionID: "rework",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode audit: %v", err)
	}
	if reworked.Mutation.Created[0].PriorValues.TransitionParameters["audit"]["summary"] != "blocking findings" {
		t.Fatalf("reworked prior values = %+v, want path-specific findings preserved across loop", reworked.Mutation.Created[0].PriorValues)
	}
}

func TestCompleteCurrentNodeJoinCarriesPriorParametersAndMaterializesJoinOutput(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		synth := nodeByKey(t, def, "synth")
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		auditID := testNodeID("node-audit-" + workflowID.String())
		auditGroupID := testTransitionGroupID("group-audit-" + workflowID.String())
		req.Nodes = append(req.Nodes, NodeRecord{
			ID:           auditID,
			WorkflowID:   workflowID,
			Key:          "audit",
			Kind:         workflow.NodeKindAgent,
			DisplayName:  "Audit",
			SubagentRole: "coder",
		})
		workflowGraphSaveEdgeRecord(
			t,
			req.Edges,
			testEdgeID("edge-join-synth-"+workflowID.String()),
		).PromptTemplate = "Synthesize {{.Params.joined}} from {{.Params.split.summary}} and {{.Params.split.commentary}}."
		for index := range req.TransitionGroups {
			if req.TransitionGroups[index].SourceNodeID == workflow.NodeIDOf(synth) {
				req.TransitionGroups[index].TransitionID = "audit"
				req.TransitionGroups[index].DisplayName = "Audit"
			}
		}
		for index := range req.Edges {
			if req.Edges[index].TransitionGroupID != testTransitionGroupID("group-synth-done-"+workflowID.String()) {
				continue
			}
			req.Edges[index].Key = "audit"
			req.Edges[index].TargetNodeID = auditID
			req.Edges[index].PromptTemplate = "Audit {{.Params.synthesize.joined}} from {{.Params.split.summary}} and {{.Params.split.commentary}}."
		}
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           auditGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: auditID,
			TransitionID: "audit_done",
			DisplayName:  "Done",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                testEdgeID("edge-audit-done-" + workflowID.String()),
			WorkflowID:        workflowID,
			TransitionGroupID: auditGroupID,
			Key:               "done",
			TargetNodeID:      workflow.NodeIDOf(done),
			ContextMode:       workflow.ContextModeNewSession,
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	split, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "approved plan"},
		Commentary:   "implementation handoff",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode split: %v", err)
	}
	branches := make(map[workflow.TransitionBranchKey]workflow.CurrentNode, len(split.Mutation.Created))
	for _, branch := range split.Mutation.Created {
		branchKey, present := branch.Reference.TransitionBranchKey()
		if !present {
			t.Fatalf("fanout branch = %+v, want branch scope", branch)
		}
		branches[branchKey] = branch
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_a"].Reference,
		TransitionID: "join_a",
		OutputValues: map[string]string{"joined": "joined implementation"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode join A: %v", err)
	}
	joined, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_b"].Reference,
		TransitionID: "join_b",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode join B: %v", err)
	}
	if len(joined.Mutation.Created) != 1 {
		t.Fatalf("join completion mutation = %+v, want synth current node", joined.Mutation)
	}
	synth := joined.Mutation.Created[0]
	if synth.PriorValues.TransitionParameters["split"]["summary"] != "approved plan" {
		t.Fatalf("synth prior parameter values = %+v, want pre-fanout split output retained across join", synth.PriorValues)
	}
	if synth.PriorValues.TransitionParameters["split"][workflow.RuntimePromptParameterCommentary] != "implementation handoff" {
		t.Fatalf("synth prior parameter values = %+v, want pre-fanout split commentary retained across join", synth.PriorValues)
	}
	if synth.PriorValues.TransitionParameters["synthesize"]["joined"] != "joined implementation" {
		t.Fatalf("synth prior parameter values = %+v, want join transition output under synthesize namespace", synth.PriorValues)
	}

	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       synth.Reference,
		TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode synth: %v", err)
	}
	if len(auditResult.Mutation.Created) != 1 ||
		auditResult.Mutation.Created[0].PriorValues.TransitionParameters["split"]["summary"] != "approved plan" ||
		auditResult.Mutation.Created[0].PriorValues.TransitionParameters["split"][workflow.RuntimePromptParameterCommentary] != "implementation handoff" ||
		auditResult.Mutation.Created[0].PriorValues.TransitionParameters["synthesize"]["joined"] != "joined implementation" {
		t.Fatalf("audit current node = %+v, want propagated pre-fanout and join transition outputs", auditResult.Mutation.Created)
	}
}

func TestCompleteCurrentNodeJoinDerivesProvidersFromThreeIncomingBranches(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		branchCID := testNodeID("node-impl-c-" + workflowID.String())
		branchCGroupID := testTransitionGroupID("group-join-c-" + workflowID.String())
		workflowGraphSaveEdgeRecord(
			t,
			req.Edges,
			testEdgeID("edge-join-synth-"+workflowID.String()),
		).PromptTemplate = "Synthesize {{.Params.joined}} {{.Params.compliance_findings}}."
		req.Nodes = append(req.Nodes, NodeRecord{
			ID:           branchCID,
			WorkflowID:   workflowID,
			Key:          "impl_c",
			Kind:         workflow.NodeKindAgent,
			DisplayName:  "Implement C",
			SubagentRole: "coder",
		})
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           branchCGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: branchCID,
			TransitionID: "join_c",
			DisplayName:  "Join",
		})
		req.Edges = append(req.Edges,
			EdgeRecord{
				ID:                testEdgeID("edge-split-c-" + workflowID.String()),
				WorkflowID:        workflowID,
				TransitionGroupID: testTransitionGroupID("group-split-" + workflowID.String()),
				Key:               "split_c",
				TargetNodeID:      branchCID,
				ContextMode:       workflow.ContextModeNewSession,
				PromptTemplate:    "C.",
			},
			EdgeRecord{
				ID:                testEdgeID("edge-join-c-" + workflowID.String()),
				WorkflowID:        workflowID,
				TransitionGroupID: branchCGroupID,
				Key:               "join_c",
				TargetNodeID:      workflow.NodeIDOf(nodeByKey(t, def, "join")),
				ContextMode:       workflow.ContextModeNewSession,
				Parameters: []workflow.Parameter{{
					Key:         "compliance_findings",
					Description: "Compliance findings.",
				}},
			},
		)
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	split, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "approved plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode split: %v", err)
	}
	branches := make(map[workflow.TransitionBranchKey]workflow.CurrentNode, len(split.Mutation.Created))
	for _, branch := range split.Mutation.Created {
		branchKey, present := branch.Reference.TransitionBranchKey()
		if !present {
			t.Fatalf("fanout branch = %+v, want branch scope", branch)
		}
		branches[branchKey] = branch
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_a"].Reference,
		TransitionID: "join_a",
		OutputValues: map[string]string{"joined": "joined implementation"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode join A: %v", err)
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_b"].Reference,
		TransitionID: "join_b",
	}); err != nil {
		t.Fatalf("CompleteCurrentNode join B: %v", err)
	}
	joined, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_c"].Reference,
		TransitionID: "join_c",
		OutputValues: map[string]string{"compliance_findings": "approved"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode join C with incomplete stored provider map: %v", err)
	}
	if len(joined.Mutation.Created) != 1 ||
		joined.Mutation.Created[0].CurrentInputValues["joined"] != "joined implementation" ||
		joined.Mutation.Created[0].CurrentInputValues["compliance_findings"] != "approved" {
		t.Fatalf("joined Current Node = %+v, want Join aggregate materialized without target input fields", joined.Mutation.Created)
	}
}

func createMaterializedCurrentNodeWorkflow(t *testing.T, ctx context.Context, store *Store) runtimeids.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Materialized Current Node Values"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	planID := testNodeID("node-plan-" + created.ID.String())
	reviewID := testNodeID("node-review-" + created.ID.String())
	auditID := testNodeID("node-audit-" + created.ID.String())
	startGroupID := testTransitionGroupID("group-start-" + created.ID.String())
	reviewGroupID := testTransitionGroupID("group-review-" + created.ID.String())
	auditGroupID := testTransitionGroupID("group-audit-" + created.ID.String())
	doneGroupID := testTransitionGroupID("group-done-" + created.ID.String())
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes,
			NodeRecord{
				ID:           planID,
				WorkflowID:   created.ID,
				Key:          "plan",
				Kind:         workflow.NodeKindAgent,
				DisplayName:  "Plan",
				SubagentRole: "coder",
			},
			NodeRecord{
				ID:           reviewID,
				WorkflowID:   created.ID,
				Key:          "review",
				Kind:         workflow.NodeKindAgent,
				DisplayName:  "Review",
				SubagentRole: "coder",
			},
			NodeRecord{
				ID:           auditID,
				WorkflowID:   created.ID,
				Key:          "audit",
				Kind:         workflow.NodeKindAgent,
				DisplayName:  "Audit",
				SubagentRole: "coder",
			},
		)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: reviewGroupID, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "review", DisplayName: "Review"},
			TransitionGroupRecord{ID: auditGroupID, WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "audit", DisplayName: "Audit"},
			TransitionGroupRecord{ID: doneGroupID, WorkflowID: created.ID, SourceNodeID: auditID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: testEdgeID("edge-start-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan the work."},
			EdgeRecord{ID: testEdgeID("edge-review-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: reviewGroupID, Key: "review", TargetNodeID: reviewID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Review summary."}}},
			EdgeRecord{ID: testEdgeID("edge-audit-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: auditGroupID, Key: "audit", TargetNodeID: auditID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Audit {{.Params.review.summary}}."},
			EdgeRecord{ID: testEdgeID("edge-done-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	return created.ID
}
