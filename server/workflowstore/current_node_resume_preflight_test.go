package workflowstore

import (
	"context"
	"testing"

	"core/server/workflow"
	"core/shared/runtimeids"
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

	edgeID := *review.EnteredByEdgeID
	saved := saveWorkflowParameterEdit(t, ctx, store, workflowID, edgeID)
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

func TestWorkflowGraphSaveBlocksParameterEditForReadyCurrentNode(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	saved := saveWorkflowParameterEdit(t, ctx, store, workflowID, *started.EnteredByEdgeID)
	if saved.Saved || workflowGraphSaveBlockerCount(saved.Blockers, "active_transition_parameter_changed") == 0 {
		t.Fatalf("ready Current Node parameter edit = %+v, want active-transition-parameter blocker", saved)
	}
}

func TestWorkflowGraphSaveBlocksParameterEditForAdmittedCurrentNode(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	if err := store.AdmitCurrentNode(ctx, started.Reference); err != nil {
		t.Fatalf("AdmitCurrentNode: %v", err)
	}

	saved := saveWorkflowParameterEdit(t, ctx, store, workflowID, *started.EnteredByEdgeID)
	if saved.Saved || workflowGraphSaveBlockerCount(saved.Blockers, "active_transition_parameter_changed") == 0 {
		t.Fatalf("admitted Current Node parameter edit = %+v, want active-transition-parameter blocker", saved)
	}
}

func TestWorkflowGraphSaveBlocksParameterEditBetweenAdmissionAndRunnerStart(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	admitted := make(chan struct{})
	releaseRunnerPreparation := make(chan struct{})
	admissionErr := make(chan error, 1)
	go func() {
		err := store.AdmitCurrentNode(ctx, started.Reference)
		close(admitted)
		<-releaseRunnerPreparation
		admissionErr <- err
	}()
	<-admitted
	saved := saveWorkflowParameterEdit(t, ctx, store, workflowID, *started.EnteredByEdgeID)
	close(releaseRunnerPreparation)
	if err := <-admissionErr; err != nil {
		t.Fatalf("AdmitCurrentNode: %v", err)
	}
	if saved.Saved || workflowGraphSaveBlockerCount(saved.Blockers, "active_transition_parameter_changed") == 0 {
		t.Fatalf("admit-to-runner parameter edit = %+v, want active-transition-parameter blocker", saved)
	}
}

func TestWorkflowGraphSaveBlocksParameterEditForPendingApproval(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "review")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "approved plan"},
	})
	if err != nil || completed.PendingApproval == nil {
		t.Fatalf("CompleteCurrentNode = %+v, %v; want pending approval", completed, err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	edgeID := edgeByKey(t, definition, "review").ID

	saved := saveWorkflowParameterEdit(t, ctx, store, workflowID, edgeID)
	if saved.Saved || workflowGraphSaveBlockerCount(saved.Blockers, "active_transition_parameter_changed") == 0 {
		t.Fatalf("pending Approval parameter edit = %+v, want active-transition-parameter blocker", saved)
	}
}

func TestWorkflowGraphSaveBlocksParameterEditForUnresolvedParallelBranch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	split, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "approved plan"},
	})
	if err != nil || len(split.Mutation.Created) != 2 {
		t.Fatalf("CompleteCurrentNode split = %+v, %v; want two parallel branches", split, err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	edgeID := edgeByKey(t, definition, "split_a").ID

	saved := saveWorkflowParameterEdit(t, ctx, store, workflowID, edgeID)
	if saved.Saved || workflowGraphSaveBlockerCount(saved.Blockers, "active_transition_parameter_changed") == 0 {
		t.Fatalf("parallel branch parameter edit = %+v, want active-transition-parameter blocker", saved)
	}
}

func TestWorkflowGraphSaveBlocksJoinProviderParameterEditForReadyDownstream(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	split, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "approved plan"},
	})
	if err != nil || len(split.Mutation.Created) != 2 {
		t.Fatalf("CompleteCurrentNode split = %+v, %v; want two parallel branches", split, err)
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       split.Mutation.Created[0].Reference,
		TransitionID: "join_a",
		OutputValues: map[string]string{"joined": "joined value"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode first join arrival: %v", err)
	}
	joined, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       split.Mutation.Created[1].Reference,
		TransitionID: "join_b",
		OutputValues: map[string]string{},
	})
	if err != nil || len(joined.AutomaticIntents) != 1 {
		t.Fatalf("CompleteCurrentNode second join arrival = %+v, %v; want synth intent", joined, err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	providerEdge := edgeByKey(t, definition, "join_a").ID

	saved := saveWorkflowParameterEdit(t, ctx, store, workflowID, providerEdge)
	if saved.Saved || workflowGraphSaveBlockerCount(saved.Blockers, "active_transition_parameter_changed") == 0 {
		t.Fatalf("Join provider parameter edit = %+v, want active-transition-parameter blocker", saved)
	}
}

func saveWorkflowParameterEdit(
	t *testing.T,
	ctx context.Context,
	store *Store,
	workflowID runtimeids.WorkflowID,
	edgeID workflow.EdgeID,
) WorkflowGraphSaveResult {
	t.Helper()
	definition, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition before parameter edit: %v", err)
	}
	request := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, definition)
	for index := range request.Edges {
		if request.Edges[index].ID != edgeID {
			continue
		}
		request.Edges[index].Parameters = append(request.Edges[index].Parameters, workflow.Parameter{
			Key:         "risk",
			Description: "New risk.",
		})
		saved, saveErr := store.SaveWorkflowGraph(ctx, request)
		if saveErr != nil {
			t.Fatalf("SaveWorkflowGraph parameter edit: %v", saveErr)
		}
		return saved
	}
	t.Fatalf("workflow edge %q not found in parameter edit request", edgeID)
	return WorkflowGraphSaveResult{}
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
