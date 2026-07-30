package workflowstore

import (
	"context"
	"testing"

	"core/server/workflow"
)

func TestCompleteCurrentNodeMaterializesChainedInputsAndPriorNodeValues(t *testing.T) {
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
	if review.PriorNodeValues["plan"]["summary"] != "approved plan" {
		t.Fatalf("review prior node values = %+v, want plan summary retained for downstream audit", review.PriorNodeValues)
	}
	if review.PriorNodeValues["review"]["summary"] != "approved plan" {
		t.Fatalf("review prior transition values = %+v, want review transition summary retained for downstream audit", review.PriorNodeValues)
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
	if audit.PriorNodeValues["plan"]["summary"] != "approved plan" {
		t.Fatalf("audit prior node values = %+v, want plan summary carried from review current node", audit.PriorNodeValues)
	}
	if audit.PriorNodeValues["review"]["summary"] != "approved plan" {
		t.Fatalf("audit prior transition values = %+v, want review transition summary carried from review current node", audit.PriorNodeValues)
	}
	startContext, err := store.ResolveCurrentNodeStartContext(ctx, audit.Reference)
	if err != nil {
		t.Fatalf("ResolveCurrentNodeStartContext audit: %v", err)
	}
	if startContext.PriorParameterValues["review"]["summary"] != "approved plan" {
		t.Fatalf("audit start prior parameter values = %+v, want review transition namespace", startContext.PriorParameterValues)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(audit.Reference) ||
		currentNodes[0].PriorNodeValues["plan"]["summary"] != "approved plan" ||
		currentNodes[0].PriorNodeValues["review"]["summary"] != "approved plan" {
		t.Fatalf("current nodes = %+v, want audit-owned materialized values", currentNodes)
	}
}

func TestCompleteCurrentNodeUsesTransitionParametersInsteadOfTargetInputFields(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		review := nodeByKey(t, def, "review")
		reviewRecord := workflowGraphSaveNodeRecord(t, req.Nodes, workflow.NodeIDOf(review))
		reviewRecord.InputFields = []workflow.InputField{{
			Name:        "changes",
			Description: "Legacy target input.",
		}}
		workflowGraphSaveEdgeRecord(
			t,
			req.Edges,
			workflow.EdgeID("edge-review-"+string(workflowID)),
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

func TestCompleteCurrentNodeJoinCarriesPriorParametersAndMaterializesJoinOutput(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		synth := nodeByKey(t, def, "synth")
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		auditID := workflow.NodeID("node-audit-" + string(workflowID))
		auditGroupID := workflow.TransitionGroupID("group-audit-" + string(workflowID))
		req.Nodes = append(req.Nodes, NodeRecord{
			ID:             auditID,
			WorkflowID:     workflowID,
			Key:            "audit",
			Kind:           workflow.NodeKindAgent,
			DisplayName:    "Audit",
			SubagentRole:   "coder",
			PromptTemplate: "Audit.",
		})
		workflowGraphSaveEdgeRecord(
			t,
			req.Edges,
			workflow.EdgeID("edge-join-synth-"+string(workflowID)),
		).PromptTemplate = "Synthesize {{.Params.joined}} from {{.Params.split.summary}}."
		for index := range req.TransitionGroups {
			if req.TransitionGroups[index].SourceNodeID == workflow.NodeIDOf(synth) {
				req.TransitionGroups[index].TransitionID = "audit"
				req.TransitionGroups[index].DisplayName = "Audit"
			}
		}
		for index := range req.Edges {
			if req.Edges[index].TransitionGroupID != workflow.TransitionGroupID("group-synth-done-"+string(workflowID)) {
				continue
			}
			req.Edges[index].Key = "audit"
			req.Edges[index].TargetNodeID = auditID
			req.Edges[index].PromptTemplate = "Audit {{.Params.done.joined}} from {{.Params.split.summary}}."
		}
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           auditGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: auditID,
			TransitionID: "done",
			DisplayName:  "Done",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                workflow.EdgeID("edge-audit-done-" + string(workflowID)),
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
		TransitionID: "join",
		OutputValues: map[string]string{"joined": "joined implementation"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode join A: %v", err)
	}
	joined, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_b"].Reference,
		TransitionID: "join",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode join B: %v", err)
	}
	if len(joined.Mutation.Created) != 1 {
		t.Fatalf("join completion mutation = %+v, want synth current node", joined.Mutation)
	}
	synth := joined.Mutation.Created[0]
	if synth.PriorNodeValues["split"]["summary"] != "approved plan" {
		t.Fatalf("synth prior parameter values = %+v, want pre-fanout split output retained across join", synth.PriorNodeValues)
	}
	if synth.PriorNodeValues["done"]["joined"] != "joined implementation" {
		t.Fatalf("synth prior parameter values = %+v, want join transition output under done namespace", synth.PriorNodeValues)
	}

	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       synth.Reference,
		TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode synth: %v", err)
	}
	if len(auditResult.Mutation.Created) != 1 ||
		auditResult.Mutation.Created[0].PriorNodeValues["split"]["summary"] != "approved plan" ||
		auditResult.Mutation.Created[0].PriorNodeValues["done"]["joined"] != "joined implementation" {
		t.Fatalf("audit current node = %+v, want propagated pre-fanout and join transition outputs", auditResult.Mutation.Created)
	}
}

func TestCompleteCurrentNodeJoinDerivesProvidersFromThreeIncomingBranches(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		branchCID := workflow.NodeID("node-impl-c-" + string(workflowID))
		branchCGroupID := workflow.TransitionGroupID("group-join-c-" + string(workflowID))
		synth := nodeByKey(t, def, "synth")
		synthRecord := workflowGraphSaveNodeRecord(t, req.Nodes, workflow.NodeIDOf(synth))
		synthRecord.InputFields = nil
		workflowGraphSaveEdgeRecord(
			t,
			req.Edges,
			workflow.EdgeID("edge-join-synth-"+string(workflowID)),
		).PromptTemplate = "Synthesize {{.Params.joined}} {{.Params.compliance_findings}}."
		req.Nodes = append(req.Nodes, NodeRecord{
			ID:             branchCID,
			WorkflowID:     workflowID,
			Key:            "impl_c",
			Kind:           workflow.NodeKindAgent,
			DisplayName:    "Implement C",
			SubagentRole:   "coder",
			PromptTemplate: "C.",
		})
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           branchCGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: branchCID,
			TransitionID: "join",
			DisplayName:  "Join",
		})
		req.Edges = append(req.Edges,
			EdgeRecord{
				ID:                workflow.EdgeID("edge-split-c-" + string(workflowID)),
				WorkflowID:        workflowID,
				TransitionGroupID: workflow.TransitionGroupID("group-split-" + string(workflowID)),
				Key:               "split_c",
				TargetNodeID:      branchCID,
				ContextMode:       workflow.ContextModeNewSession,
				PromptTemplate:    "C.",
			},
			EdgeRecord{
				ID:                workflow.EdgeID("edge-join-c-" + string(workflowID)),
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
		TransitionID: "join",
		OutputValues: map[string]string{"joined": "joined implementation"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode join A: %v", err)
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_b"].Reference,
		TransitionID: "join",
	}); err != nil {
		t.Fatalf("CompleteCurrentNode join B: %v", err)
	}
	joined, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_c"].Reference,
		TransitionID: "join",
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

func createMaterializedCurrentNodeWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Materialized Current Node Values"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	reviewID := workflow.NodeID("node-review-" + string(created.ID))
	auditID := workflow.NodeID("node-audit-" + string(created.ID))
	startGroupID := workflow.TransitionGroupID("group-start-" + string(created.ID))
	reviewGroupID := workflow.TransitionGroupID("group-review-" + string(created.ID))
	auditGroupID := workflow.TransitionGroupID("group-audit-" + string(created.ID))
	doneGroupID := workflow.TransitionGroupID("group-done-" + string(created.ID))
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes,
			NodeRecord{
				ID:             planID,
				WorkflowID:     created.ID,
				Key:            "plan",
				Kind:           workflow.NodeKindAgent,
				DisplayName:    "Plan",
				SubagentRole:   "coder",
				PromptTemplate: "Plan the work.",
				OutputFields:   []workflow.OutputField{{Name: "summary", Description: "Plan summary."}},
			},
			NodeRecord{
				ID:             reviewID,
				WorkflowID:     created.ID,
				Key:            "review",
				Kind:           workflow.NodeKindAgent,
				DisplayName:    "Review",
				SubagentRole:   "coder",
				PromptTemplate: "Review {{.Inputs.summary}}.",
				InputFields:    []workflow.InputField{{Name: "summary", Description: "Plan summary."}},
			},
			NodeRecord{
				ID:             auditID,
				WorkflowID:     created.ID,
				Key:            "audit",
				Kind:           workflow.NodeKindAgent,
				DisplayName:    "Audit",
				SubagentRole:   "coder",
				PromptTemplate: "Audit {{.Nodes.plan.summary}} and {{.Params.review.summary}}.",
			},
		)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: reviewGroupID, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "review", DisplayName: "Review"},
			TransitionGroupRecord{ID: auditGroupID, WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "audit", DisplayName: "Audit"},
			TransitionGroupRecord{ID: doneGroupID, WorkflowID: created.ID, SourceNodeID: auditID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan the work."},
			EdgeRecord{ID: workflow.EdgeID("edge-review-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: reviewGroupID, Key: "review", TargetNodeID: reviewID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review {{.Inputs.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
			EdgeRecord{ID: workflow.EdgeID("edge-audit-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: auditGroupID, Key: "audit", TargetNodeID: auditID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Audit {{.Nodes.plan.summary}} and {{.Params.review.summary}}."},
			EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	return created.ID
}
