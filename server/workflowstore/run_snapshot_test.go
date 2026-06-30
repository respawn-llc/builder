package workflowstore

import (
	"database/sql"
	"errors"
	"sync"
	"testing"

	"core/server/workflow"
)

func TestCompleteRunUsesRunStartSnapshotAfterGraphChanges(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	beforeEdit := currentWorkflowRevision(t, ctx, store, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	forceWorkflowGraphRowsForSnapshotTest(t, ctx, store, workflowID,
		[]NodeRecord{{ID: "node-extra-terminal", WorkflowID: workflowID, Key: "archived", Kind: workflow.NodeKindTerminal, DisplayName: "Archived"}},
		[]TransitionGroupRecord{{ID: "group-archive", WorkflowID: workflowID, SourceNodeID: agent.ID, TransitionID: "archive", DisplayName: "Archive"}},
		[]EdgeRecord{{ID: "edge-archive", WorkflowID: workflowID, TransitionGroupID: "group-archive", Key: "archive", TargetNodeID: "node-extra-terminal", ContextMode: workflow.ContextModeNewSession}},
	)
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "archive", OutputValues: map[string]string{"summary": "done"}}); !completionHasCode(err, CompletionCodeInvalidTransitionID) {
		t.Fatalf("expected completion to reject transition added after run start, got %v", err)
	}
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", Commentary: "finished"})
	if completed.State != "applied" || len(completed.PlacementIDs) != 1 || len(completed.RunIDs) != 0 {
		t.Fatalf("completion result = %+v", completed)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].TransitionID != "done" || transitions[1].State != "applied" {
		t.Fatalf("transitions after completion = %+v", transitions)
	}
	edges, err := store.ListTransitionEdges(ctx, transitions[1].ID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(edges) != 1 || edges[0].EdgeKey != "done" || edges[0].WorkflowRevisionSeen != beforeEdit || edges[0].TargetPlacementID != completed.PlacementIDs[0] {
		t.Fatalf("completion edge snapshot = %+v, want one done edge at revision %d", edges, beforeEdit)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	terminalActive := false
	sourceCompleted := false
	for _, placement := range placements {
		if placement.ID == started.PlacementID && placement.State == "completed" {
			sourceCompleted = true
		}
		if placement.ID == completed.PlacementIDs[0] && placement.State == "active" {
			terminalActive = true
		}
	}
	if !sourceCompleted || !terminalActive {
		t.Fatalf("placements after completion = %+v, want completed source and active terminal", placements)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].CompletedAt == 0 {
		t.Fatalf("runs after completion = %+v", runs)
	}
}

func TestCompleteRunBuildsChildSnapshotFromParentRevision(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	reviewerID := workflow.NodeID("node-reviewer-" + string(workflowID))
	if _, err := store.AddNode(ctx, NodeRecord{ID: reviewerID, WorkflowID: workflowID, Key: "reviewer", Kind: workflow.NodeKindAgent, DisplayName: "Reviewer", SubagentRole: "coder", PromptTemplate: "Review work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}}); err != nil {
		t.Fatalf("AddNode reviewer: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-review-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: agent.ID, TransitionID: "review", DisplayName: "Review"}); err != nil {
		t.Fatalf("AddTransitionGroup review: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-review-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-review-" + string(workflowID)), Key: "review", TargetNodeID: reviewerID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review work."}); err != nil {
		t.Fatalf("AddEdge review: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-review-done-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: reviewerID, TransitionID: "review_done", DisplayName: "Review Done"}); err != nil {
		t.Fatalf("AddTransitionGroup review done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-review-done-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-review-done-" + string(workflowID)), Key: "review_done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession, OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}}}); err != nil {
		t.Fatalf("AddEdge review done: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	archiveID := workflow.NodeID("node-archive-" + string(workflowID))
	forceWorkflowGraphRowsForSnapshotTest(t, ctx, store, workflowID,
		[]NodeRecord{{ID: archiveID, WorkflowID: workflowID, Key: "archive", Kind: workflow.NodeKindTerminal, DisplayName: "Archive"}},
		[]TransitionGroupRecord{{ID: workflow.TransitionGroupID("group-review-archive-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: reviewerID, TransitionID: "archive", DisplayName: "Archive"}},
		[]EdgeRecord{{ID: workflow.EdgeID("edge-review-archive-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-review-archive-" + string(workflowID)), Key: "archive", TargetNodeID: archiveID, ContextMode: workflow.ContextModeNewSession}},
	)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "review"})
	if len(completed.RunIDs) != 1 {
		t.Fatalf("completion child runs = %+v, want one", completed.RunIDs)
	}
	runContext, err := store.GetRunStartContext(ctx, completed.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if len(runContext.TransitionIDs) != 1 || runContext.TransitionIDs[0] != "review_done" {
		t.Fatalf("child transition ids = %+v, want only review_done from parent snapshot", runContext.TransitionIDs)
	}
}

func TestStartTaskRejectsCanceledAndAlreadyStartedTasks(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	canceled, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Canceled", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask canceled: %v", err)
	}
	if err := store.CancelTask(ctx, canceled.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if _, err := store.StartTask(ctx, canceled.ID); !errors.Is(err, ErrTaskCanceled) {
		t.Fatalf("StartTask canceled error = %v", err)
	}

	startedTask, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Started", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask started: %v", err)
	}
	if _, err := store.StartTask(ctx, startedTask.ID); err != nil {
		t.Fatalf("StartTask first: %v", err)
	}
	if _, err := store.StartTask(ctx, startedTask.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("StartTask second = %v, want sql.ErrNoRows", err)
	}
	runs, err := store.ListRuns(ctx, startedTask.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs after duplicate start = %+v, want exactly one", runs)
	}
}

func TestRunStartContextExposesAcceptedStartTransitionPath(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	input, err := store.GetRunStartContext(ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.AcceptedTransitionPath.SourceNodeDisplayName != "Backlog" || input.AcceptedTransitionPath.TargetNodeDisplayName != "Agent" {
		t.Fatalf("accepted transition path = %+v, want Backlog -> Agent", input.AcceptedTransitionPath)
	}
}

func TestWorkflowTransitionsRefreshTaskUpdatedAt(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	// Intentional direct timestamp fixture: verifies workflow transitions refresh
	// stale task updated_at rows without depending on wall-clock sleeps.
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET updated_at_unix_ms = 1 WHERE id = ?`, string(task.ID)); err != nil {
		t.Fatalf("force stale task timestamp: %v", err)
	}
	started := startTask(t, ctx, store, task.ID)
	afterStart, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask after start: %v", err)
	}
	if afterStart.UpdatedAtUnixMs <= 1 {
		t.Fatalf("task updated_at after start = %d, want refreshed", afterStart.UpdatedAtUnixMs)
	}
	// Intentional direct timestamp fixture: reset the row stale again before
	// completion so completion's refresh is tested independently.
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET updated_at_unix_ms = 2 WHERE id = ?`, string(task.ID)); err != nil {
		t.Fatalf("force stale task timestamp after start: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	afterComplete, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask after complete: %v", err)
	}
	if afterComplete.UpdatedAtUnixMs <= 2 {
		t.Fatalf("task updated_at after complete = %d, want refreshed", afterComplete.UpdatedAtUnixMs)
	}
}

func TestStartTaskConcurrentCallsCreateOneRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.StartTask(ctx, task.ID)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	noRows := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, sql.ErrNoRows):
			noRows++
		default:
			t.Fatalf("StartTask concurrent unexpected error: %v", err)
		}
	}
	if successes != 1 || noRows != 1 {
		t.Fatalf("concurrent starts successes=%d noRows=%d, want 1/1", successes, noRows)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs after concurrent start = %+v, want exactly one", runs)
	}
}

func TestCompleteRunCreatesTargetRunForContinueSessionContextMode(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeContinueSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "plan done"}})
	if len(completed.RunIDs) != 1 {
		t.Fatalf("target run ids = %+v, want one continuation target", completed.RunIDs)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 || runs[0].CompletedAt == 0 || runs[1].CompletedAt != 0 || runs[1].InterruptedAt != 0 {
		t.Fatalf("runs after continuation completion = %+v, want completed source and active target", runs)
	}
	edges, err := store.ListTransitionEdges(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	var persistedContextMode string
	if err := store.db.QueryRowContext(ctx, `SELECT context_mode FROM task_transition_edges WHERE id = ?`, edges[0].ID).Scan(&persistedContextMode); err != nil {
		t.Fatalf("query transition edge context mode: %v", err)
	}
	if len(edges) != 1 || persistedContextMode != string(workflow.ContextModeContinueSession) || edges[0].TargetPlacementID != runs[1].PlacementID {
		t.Fatalf("transition edge snapshot = %+v, want continue_session target edge", edges)
	}
	input, err := store.GetRunStartContext(ctx, completed.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.Node.Key != "implement" || input.InputValues["prior_summary"] != "plan done" {
		t.Fatalf("target run context = %+v, want implement node with bound prior output", input)
	}
	var runMetadataJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT metadata_json FROM task_runs WHERE id = ?`, string(completed.RunIDs[0])).Scan(&runMetadataJSON); err != nil {
		t.Fatalf("query target run metadata: %v", err)
	}
	runMetadata := struct {
		ContextMode     string `json:"context_mode"`
		SourceRunID     string `json:"source_run_id"`
		SourceSessionID string `json:"source_session_id"`
	}{}
	if err := workflow.UnmarshalString(runMetadataJSON, &runMetadata); err != nil {
		t.Fatalf("unmarshal target run metadata: %v", err)
	}
	if runMetadata.ContextMode != string(workflow.ContextModeContinueSession) || runMetadata.SourceRunID != string(started.RunID) {
		t.Fatalf("target run metadata = %+v, want context mode and source run", runMetadata)
	}
}

func TestRunStartContextResolvesPriorTransitionParameters(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createPromptNodeReferenceWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"summary": "plan summary"}})
	if len(completed.RunIDs) != 1 {
		t.Fatalf("target run ids = %+v, want one", completed.RunIDs)
	}
	audit := completeRun(t, ctx, store, CompleteRunRequest{RunID: completed.RunIDs[0], TransitionID: "audit"})
	if len(audit.RunIDs) != 1 {
		t.Fatalf("audit target run ids = %+v, want one", audit.RunIDs)
	}
	input, err := store.GetRunStartContext(ctx, audit.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.PriorParameterValues["next"]["summary"] != "plan summary" {
		t.Fatalf("prior parameter values = %+v, want next.summary", input.PriorParameterValues)
	}
	mutatedOutputs, err := workflow.MarshalString(map[string]string{"summary": "mutated later"})
	if err != nil {
		t.Fatalf("marshal mutated outputs: %v", err)
	}
	// Intentional corruption fixture: mutate persisted transition outputs after
	// child run creation to prove run-start snapshots stay frozen.
	if _, err := store.db.ExecContext(ctx, `UPDATE task_transitions SET output_values_json = ? WHERE source_run_id = ?`, mutatedOutputs, string(started.RunID)); err != nil {
		t.Fatalf("mutate source output: %v", err)
	}
	frozenInput, err := store.GetRunStartContext(ctx, audit.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext frozen: %v", err)
	}
	if frozenInput.PriorParameterValues["next"]["summary"] != "plan summary" {
		t.Fatalf("frozen prior parameter values = %+v, want original plan summary", frozenInput.PriorParameterValues)
	}
}

func TestTransitionParameterDerivesOutputRequirement(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createPromptNodeReferenceWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	sourceContext, err := store.GetRunStartContext(ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext source: %v", err)
	}
	if len(sourceContext.Node.OutputFields) != 1 || sourceContext.Node.OutputFields[0].Name != "summary" || sourceContext.Node.OutputFields[0].Description != "Plan summary." {
		t.Fatalf("source output fields = %+v, want prompt-derived summary description", sourceContext.Node.OutputFields)
	}
	if len(sourceContext.TransitionOptions) != 1 || sourceContext.TransitionOptions[0].ID != "next" || sourceContext.TransitionOptions[0].Description != "Continue after planning is complete." || len(sourceContext.TransitionOptions[0].Parameters) != 1 || sourceContext.TransitionOptions[0].Parameters[0].Key != "summary" || sourceContext.TransitionOptions[0].Parameters[0].Description != "Plan summary." {
		t.Fatalf("source transition options = %+v, want next description and summary parameter", sourceContext.TransitionOptions)
	}

	_, err = store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next"})
	if !completionHasCode(err, CompletionCodeRequiredOutputMissing) {
		t.Fatalf("CompleteRun error = %v, want required output", err)
	}
}

func TestTransitionOptionsFromSnapshotUnionFanoutBranchParameters(t *testing.T) {
	options := transitionOptionsFromSnapshot(runStartSnapshot{
		Node: nodeContractSnapshot{ID: "node-plan"},
		TransitionGroups: []transitionContractSnapshot{
			{
				SourceNodeID: "node-plan",
				TransitionID: "split",
				DisplayName:  "Split",
				Edges: []edgeContractSnapshot{
					{ID: "edge-a", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
					{ID: "edge-b", Parameters: []workflow.Parameter{{Key: "risk", Description: "Known risk."}}},
				},
			},
		},
	})

	if len(options) != 1 || options[0].ID != "split" || options[0].DisplayName != "Split" {
		t.Fatalf("transition options = %+v, want split", options)
	}
	if len(options[0].Parameters) != 2 || options[0].Parameters[0].Key != "summary" || options[0].Parameters[1].Key != "risk" {
		t.Fatalf("transition option parameters = %+v, want branch parameter union", options[0].Parameters)
	}
}

func TestPriorTransitionParameterApprovalFreezesOutputValue(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createPromptNodeReferenceWorkflow(t, ctx, store)
	if _, err := store.UpdateEdge(ctx, EdgeRecord{
		ID:                workflow.EdgeID("edge-audit-" + string(workflowID)),
		WorkflowID:        workflowID,
		TransitionGroupID: workflow.TransitionGroupID("group-audit-" + string(workflowID)),
		Key:               "audit",
		TargetNodeID:      workflow.NodeID("node-audit-" + string(workflowID)),
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Audit {{.Params.next.summary}}.",
		RequiresApproval:  true,
	}); err != nil {
		t.Fatalf("UpdateEdge approval: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	review := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"summary": "approval summary"}})
	if len(review.RunIDs) != 1 {
		t.Fatalf("review target run ids = %+v, want one", review.RunIDs)
	}
	pending, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: review.RunIDs[0], TransitionID: "audit"})
	if err != nil {
		t.Fatalf("CompleteRun audit: %v", err)
	}
	if !pending.RequiresApproval {
		t.Fatalf("completion result = %+v, want pending approval", pending)
	}
	mutatedOutputs, err := workflow.MarshalString(map[string]string{"summary": "mutated before approval"})
	if err != nil {
		t.Fatalf("marshal mutated outputs: %v", err)
	}
	// Intentional corruption fixture: mutate persisted transition outputs before
	// approval to prove pending approval snapshots stay frozen.
	if _, err := store.db.ExecContext(ctx, `UPDATE task_transitions SET output_values_json = ? WHERE source_run_id = ?`, mutatedOutputs, string(started.RunID)); err != nil {
		t.Fatalf("mutate source output: %v", err)
	}

	approved, err := store.ApproveTransition(ctx, pending.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approved run ids = %+v, want one", approved.RunIDs)
	}
	input, err := store.GetRunStartContext(ctx, approved.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.PriorParameterValues["next"]["summary"] != "approval summary" {
		t.Fatalf("approved prior parameter values = %+v, want frozen approval summary", input.PriorParameterValues)
	}
}
