package workflowstore

import (
	"testing"

	"core/server/workflow"
)

func TestPreflightTaskResumeRejectsEditedTransitionParameterWithoutMutatingCurrentNode(t *testing.T) {
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
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	review := reviewResult.Mutation.Created[0]
	if review.EnteredByEdgeID == nil {
		t.Fatal("review Current Node has no entering Edge")
	}
	if err := store.InterruptCurrentNode(
		ctx,
		review.Reference,
		workflow.CurrentNodeInterruptionReasonUserInterrupt,
		workflow.CurrentNodeInterruptionDetail{Code: string(workflow.CurrentNodeInterruptionReasonUserInterrupt)},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	parameters, err := marshalJSONArray([]workflow.Parameter{
		{Key: "summary", Description: "Review summary."},
		{Key: "risk", Description: "New risk."},
	})
	if err != nil {
		t.Fatalf("marshal edited Transition Parameters: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE workflow_edges
SET parameters_json = ?
WHERE id = ?`, parameters, string(*review.EnteredByEdgeID)); err != nil {
		t.Fatalf("edit entering Transition Branch: %v", err)
	}

	classifications, err := store.PreflightTaskResume(ctx, task.ID)
	if err != nil {
		t.Fatalf("PreflightTaskResume: %v", err)
	}
	if len(classifications) != 1 || !classifications[0].CurrentNode.Reference.Equal(review.Reference) {
		t.Fatalf("classifications = %+v, want one classification for %v", classifications, review.Reference)
	}
	if len(classifications[0].Diagnostics) != 1 {
		t.Fatalf("resume diagnostics = %+v, want one missing-parameter diagnostic", classifications[0].Diagnostics)
	}
	diagnostic := classifications[0].Diagnostics[0]
	if diagnostic.Code != "workflow.resume.parameter_not_materialized" ||
		!diagnostic.CurrentNode.Equal(review.Reference) ||
		diagnostic.EnteringEdgeID != *review.EnteredByEdgeID ||
		diagnostic.ParameterKey != "risk" {
		t.Fatalf("resume diagnostic = %+v, want exact Current Node, entering Edge, and Parameter context", diagnostic)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("current nodes = %+v, want interrupted Current Node preserved", currentNodes)
	}
}

func TestWorkflowGraphSaveAllowsParameterEditForInterruptedCurrentNode(t *testing.T) {
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
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	review := reviewResult.Mutation.Created[0]
	if err := store.InterruptCurrentNode(
		ctx,
		review.Reference,
		workflow.CurrentNodeInterruptionReasonUserInterrupt,
		workflow.CurrentNodeInterruptionDetail{Code: string(workflow.CurrentNodeInterruptionReasonUserInterrupt)},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}

	definition, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	request := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, definition)
	edgeID := *review.EnteredByEdgeID
	foundEdge := false
	for index := range request.Edges {
		if request.Edges[index].ID != edgeID {
			continue
		}
		foundEdge = true
		request.Edges[index].Parameters = append(request.Edges[index].Parameters, workflow.Parameter{
			Key:         "risk",
			Description: "New risk.",
		})
	}
	if !foundEdge {
		t.Fatalf("workflow edge %q not found in save request", edgeID)
	}
	saved, err := store.SaveWorkflowGraph(ctx, request)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph parameter edit: %v", err)
	}
	if !saved.Saved || workflowGraphSaveBlockerCount(saved.Blockers, "active_transition_contract_changed") != 0 {
		t.Fatalf("SaveWorkflowGraph parameter edit = %+v, want saved without active-transition blocker", saved)
	}
	classifications, err := store.PreflightTaskResume(ctx, task.ID)
	if err != nil {
		t.Fatalf("PreflightTaskResume: %v", err)
	}
	if len(classifications) != 1 || len(classifications[0].Diagnostics) != 1 {
		t.Fatalf("resume classifications = %+v, want one missing-Parameter diagnostic", classifications)
	}
	diagnostic := classifications[0].Diagnostics[0]
	if diagnostic.Code != CurrentNodeResumeParameterNotMaterializedCode ||
		!diagnostic.CurrentNode.Equal(review.Reference) ||
		diagnostic.EnteringEdgeID != edgeID ||
		diagnostic.ParameterKey != "risk" {
		t.Fatalf("resume diagnostic = %+v, want exact Current Node, entering Edge, and Parameter context", diagnostic)
	}
}

func TestPreflightTaskResumeRejectsEditedPriorParameterReference(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveEdgeRecord(t, req.Edges, edgeByKey(t, def, "audit").ID).PromptTemplate = "Audit."
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
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode review: %v", err)
	}
	current := audit.Mutation.Created[0]
	if err := store.InterruptCurrentNode(
		ctx,
		current.Reference,
		workflow.CurrentNodeInterruptionReasonUserInterrupt,
		workflow.CurrentNodeInterruptionDetail{Code: string(workflow.CurrentNodeInterruptionReasonUserInterrupt)},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE task_current_nodes
SET prior_node_values_json = '{"transition_parameters":{}}'
WHERE task_id = ? AND node_id = ? AND transition_branch_key IS NULL`,
		string(task.ID), string(current.Reference.NodeID),
	); err != nil {
		t.Fatalf("clear prior parameter values: %v", err)
	}

	definition, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	request := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, definition)
	auditEdgeID := edgeByKey(t, definition, "audit").ID
	foundEdge := false
	for index := range request.Edges {
		if request.Edges[index].ID == auditEdgeID {
			foundEdge = true
			request.Edges[index].PromptTemplate = "Audit {{.Params.review.summary}}."
		}
	}
	if !foundEdge {
		t.Fatalf("audit Edge %q not found in save request", auditEdgeID)
	}
	saved, err := store.SaveWorkflowGraph(ctx, request)
	if err != nil || !saved.Saved {
		t.Fatalf("SaveWorkflowGraph prior-Parameter prompt edit = %+v, err=%v; want saved", saved, err)
	}

	classifications, err := store.PreflightTaskResume(ctx, task.ID)
	if err != nil {
		t.Fatalf("PreflightTaskResume: %v", err)
	}
	if len(classifications) != 1 || len(classifications[0].Diagnostics) != 1 {
		t.Fatalf("resume classifications = %+v, want one missing prior-Parameter diagnostic", classifications)
	}
	diagnostic := classifications[0].Diagnostics[0]
	if diagnostic.Code != CurrentNodeResumeParameterNotMaterializedCode ||
		!diagnostic.CurrentNode.Equal(current.Reference) ||
		diagnostic.EnteringEdgeID != auditEdgeID ||
		diagnostic.ParameterKey != "summary" {
		t.Fatalf("resume diagnostic = %+v, want prior Parameter context", diagnostic)
	}
}

func TestPreflightTaskResumeRejectsEditedJoinDerivedParameter(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	startTask(t, ctx, store, task.ID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	synthID := workflow.NodeIDOf(nodeByKey(t, definition, "synth"))
	synth, err := workflow.NewCurrentNodeReference(task.ID, synthID, nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM task_current_nodes WHERE task_id = ?`, task.ID); err != nil {
		t.Fatalf("delete seeded Current Node: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO task_current_nodes (
    task_id, node_id, current_input_values_json, prior_node_values_json,
    scheduling_state, interruption_reason, interruption_detail_json, interrupted_at_unix_ms, entered_by_edge_id
) VALUES (?, ?, '{"joined":"existing"}', '{"transition_parameters":{}}', 'interrupted', 'user_interrupt', '{}', 1, ?)`,
		task.ID, synthID, "edge-join-synth-"+workflowID.String()); err != nil {
		t.Fatalf("seed Join Current Node: %v", err)
	}
	parameters, err := marshalJSONArray([]workflow.Parameter{
		{Key: "joined", Description: "Joined branch summary."},
		{Key: "risk", Description: "New derived requirement."},
	})
	if err != nil {
		t.Fatalf("marshal edited Join Parameters: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE workflow_edges
SET parameters_json = ?
WHERE id = ?`, parameters, "edge-join-a-"+workflowID.String()); err != nil {
		t.Fatalf("edit Join incoming Transition Branch: %v", err)
	}

	classifications, err := store.PreflightTaskResume(ctx, task.ID)
	if err != nil {
		t.Fatalf("PreflightTaskResume: %v", err)
	}
	if len(classifications) != 1 || len(classifications[0].Diagnostics) != 1 {
		t.Fatalf("Join resume classifications = %+v, want one missing derived binding", classifications)
	}
	diagnostic := classifications[0].Diagnostics[0]
	if diagnostic.Code != CurrentNodeResumeParameterNotMaterializedCode ||
		!diagnostic.CurrentNode.Equal(synth) ||
		diagnostic.EnteringEdgeID != workflow.EdgeID("edge-join-synth-"+workflowID.String()) ||
		diagnostic.ParameterKey != "risk" {
		t.Fatalf("Join resume diagnostic = %+v, want exact derived binding context", diagnostic)
	}
}

func TestPreflightTaskResumeRejectsEnteringEdgeForDifferentTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, definition, "review")
	reference, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(agent), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM task_current_nodes WHERE task_id = ?`, task.ID); err != nil {
		t.Fatalf("delete Current Node: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO task_current_nodes (
    task_id, node_id, current_input_values_json, prior_node_values_json,
    scheduling_state, interruption_reason, interruption_detail_json, interrupted_at_unix_ms, entered_by_edge_id
) VALUES (?, ?, '{}', '{"transition_parameters":{}}', 'interrupted', 'user_interrupt', '{}', 1, ?)`,
		task.ID, reference.NodeID, "edge-start-"+workflowID.String()); err != nil {
		t.Fatalf("seed mismatched Current Node: %v", err)
	}
	if _, err := store.PreflightTaskResume(ctx, task.ID); err == nil {
		t.Fatal("PreflightTaskResume accepted an entering Edge targeting a different Node")
	}
}
