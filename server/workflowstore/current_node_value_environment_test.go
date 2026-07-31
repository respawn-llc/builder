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

func createMaterializedCurrentNodeWorkflow(t *testing.T, ctx context.Context, store *Store) runtimeids.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Materialized Current Node Values"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	planID := workflow.NodeID("node-plan-" + created.ID.String())
	reviewID := workflow.NodeID("node-review-" + created.ID.String())
	auditID := workflow.NodeID("node-audit-" + created.ID.String())
	startGroupID := workflow.TransitionGroupID("group-start-" + created.ID.String())
	reviewGroupID := workflow.TransitionGroupID("group-review-" + created.ID.String())
	auditGroupID := workflow.TransitionGroupID("group-audit-" + created.ID.String())
	doneGroupID := workflow.TransitionGroupID("group-done-" + created.ID.String())
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
				PromptTemplate: "Audit {{.Params.review.summary}}.",
			},
		)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: reviewGroupID, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "review", DisplayName: "Review"},
			TransitionGroupRecord{ID: auditGroupID, WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "audit", DisplayName: "Audit"},
			TransitionGroupRecord{ID: doneGroupID, WorkflowID: created.ID, SourceNodeID: auditID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: workflow.EdgeID("edge-start-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan the work."},
			EdgeRecord{ID: workflow.EdgeID("edge-review-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: reviewGroupID, Key: "review", TargetNodeID: reviewID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review {{.Inputs.summary}}."},
			EdgeRecord{ID: workflow.EdgeID("edge-audit-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: auditGroupID, Key: "audit", TargetNodeID: auditID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Audit {{.Nodes.plan.summary}}."},
			EdgeRecord{ID: workflow.EdgeID("edge-done-" + created.ID.String()), WorkflowID: created.ID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
	return created.ID
}
