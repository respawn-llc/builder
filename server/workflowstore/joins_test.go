package workflowstore

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func TestCompleteRunFanoutCreatesParallelBranchPlacements(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeID := "worktree-fanout-" + string(workflowID)
	worktreeRoot := filepath.Join(t.TempDir(), "fanout-worktree")
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{ID: worktreeID, WorkspaceID: binding.WorkspaceID, CanonicalRoot: worktreeRoot, Managed: true, CreatedBranch: true}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	// Intentional direct fixture: the test needs an existing managed worktree
	// association without exercising task creation/source-workspace behavior.
	if _, err := store.db.ExecContext(ctx, `
UPDATE tasks
SET source_workspace_id = ?,
    managed_worktree_id = ?,
    execution_target_mode = ?,
    execution_target_requested_ref = ?,
    execution_target_commit_oid = ?,
    execution_target_provenance = ?
WHERE id = ?`,
		binding.WorkspaceID,
		worktreeID,
		string(workflow.ExecutionTargetModeHead),
		"HEAD",
		"fixture-commit",
		string(ExecutionTargetProvenanceResolved),
		string(task.ID),
	); err != nil {
		t.Fatalf("attach managed worktree to task: %v", err)
	}
	started := startTask(t, ctx, store, task.ID)

	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	if len(result.PlacementIDs) != 2 || len(result.RunIDs) != 2 {
		t.Fatalf("fanout result = %+v, want two branch placements and runs", result)
	}
	for _, runID := range result.RunIDs {
		input, err := store.GetRunStartContext(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunStartContext branch %s: %v", runID, err)
		}
		if !input.IsFanoutBranch {
			t.Fatalf("branch run context %s IsFanoutBranch=false, want true", runID)
		}
		if input.ExecutionRoot == nil ||
			input.ExecutionRoot.Managed == nil ||
			input.ExecutionRoot.Managed.WorktreeID != worktreeID ||
			input.ExecutionRoot.Managed.Root != worktreeRoot {
			t.Fatalf("branch run context %s execution root = %+v, want managed worktree %q at %q", runID, input.ExecutionRoot, worktreeID, worktreeRoot)
		}
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT id, parallel_batch_transition_id, parallel_branch_edge_id
FROM task_node_placements
WHERE id IN (?, ?)
ORDER BY parallel_branch_edge_id ASC`, string(result.PlacementIDs[0]), string(result.PlacementIDs[1]))
	if err != nil {
		t.Fatalf("query branch placements: %v", err)
	}
	defer func() { _ = rows.Close() }()
	branches := map[string]string{}
	for rows.Next() {
		var placementID string
		var batchID sql.NullString
		var branchEdgeID sql.NullString
		if err := rows.Scan(&placementID, &batchID, &branchEdgeID); err != nil {
			t.Fatalf("scan branch placement: %v", err)
		}
		if batchID.String != string(result.TransitionID) || !branchEdgeID.Valid || branchEdgeID.String == "" {
			t.Fatalf("branch placement %s batch=%+v branch=%+v, want batch transition and branch edge", placementID, batchID, branchEdgeID)
		}
		branches[branchEdgeID.String] = placementID
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("branch rows: %v", err)
	}
	if len(branches) != 2 || branches["edge-split-a-"+string(workflowID)] == "" || branches["edge-split-b-"+string(workflowID)] == "" {
		t.Fatalf("branch identities = %+v, want split edge ids", branches)
	}
}

func TestSerialCompletionDoesNotCreateParallelBranchPlacement(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if len(completed.PlacementIDs) != 1 {
		t.Fatalf("completion result = %+v, want one target placement", completed)
	}
	batchID, branchID := placementParallelIDs(t, ctx, store, completed.PlacementIDs[0])
	if batchID != "" || branchID != "" {
		t.Fatalf("serial placement parallel ids batch=%q branch=%q, want empty", batchID, branchID)
	}
}

func TestJoinWaitsForAllBranchesAndRoutesSelectedProvider(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	split, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	if err != nil {
		t.Fatalf("CompleteRun split: %v", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	branchRunsByNode := map[workflow.NodeID]workflow.RunID{}
	for _, run := range runs {
		if run.ID != started.RunID {
			branchRunsByNode[run.NodeID] = run.ID
		}
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")

	first, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRunsByNode[workflow.NodeIDOf(implB)], TransitionID: "join"})
	if err != nil {
		t.Fatalf("CompleteRun branch b: %v", err)
	}
	if len(first.PlacementIDs) != 0 || len(first.RunIDs) != 0 {
		t.Fatalf("first branch result = %+v, want join waiting for missing branch", first)
	}
	selectedProviderValue := "  branch a\n"
	second, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRunsByNode[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": selectedProviderValue}})
	if err != nil {
		t.Fatalf("CompleteRun branch a: %v", err)
	}
	if len(second.PlacementIDs) != 1 || len(second.RunIDs) != 1 {
		t.Fatalf("second branch result = %+v, want joined provider-routed agent run", second)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	joinTransition := transitions[len(transitions)-1]
	if joinTransition.TransitionID != "done" || joinTransition.OutputValues["joined"] != selectedProviderValue {
		t.Fatalf("join transition = %+v, want selected provider output", joinTransition)
	}
	input, err := store.GetRunStartContext(ctx, second.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext joined run: %v", err)
	}
	if input.InputValues["joined"] != selectedProviderValue {
		t.Fatalf("joined input = %+v, want selected provider value", input.InputValues)
	}
	if split.TransitionID == "" {
		t.Fatalf("split transition id missing")
	}
}

func TestJoinArrivalsKeepMultipleJoinEdgesForOneBranchPlacement(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRunsByNode := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	join := nodeByKey(t, def, "join")
	implA := nodeByKey(t, def, "impl_a")
	alternateProviderEdgeID := workflow.EdgeID("edge-join-a-alt-" + string(workflowID))
	// Bypass graph-edit policy here to model a historical transition snapshot
	// containing more than one applied join edge for the same branch placement.
	if err := store.queries.InsertWorkflowEdge(ctx, sqlitegen.InsertWorkflowEdgeParams{
		ID:                     string(alternateProviderEdgeID),
		TransitionGroupID:      "group-join-a-" + string(workflowID),
		EdgeKey:                "join_a_alt",
		TargetNodeID:           string(workflow.NodeIDOf(join)),
		ContextMode:            string(workflow.ContextModeNewSession),
		ContextSourceKind:      string(workflow.ContextSourceImmediateSource),
		PromptTemplate:         "",
		ParametersJson:         "[]",
		InputBindingsJson:      "[]",
		OutputRequirementsJson: "[]",
		SortOrder:              999,
	}); err != nil {
		t.Fatalf("insert alternate workflow edge: %v", err)
	}
	first, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRunsByNode[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": "from alternate edge"}})
	if err != nil {
		t.Fatalf("CompleteRun branch a: %v", err)
	}
	if len(first.PlacementIDs) != 0 || len(first.RunIDs) != 0 {
		t.Fatalf("first branch result = %+v, want join waiting for missing branch", first)
	}
	if err := insertTransitionEdgeSnapshotWithMetadata(ctx, store.queries, string(first.TransitionID), edgeContractSnapshot{
		ID:         alternateProviderEdgeID,
		Key:        "join_a_alt",
		TargetNode: nodeSnapshot(join),
	}, "", "applied", workflowRunMetadata{}); err != nil {
		t.Fatalf("insert alternate transition edge snapshot: %v", err)
	}
	var batchID string
	if err := store.db.QueryRowContext(ctx, `
SELECT p.parallel_batch_transition_id
FROM task_runs r
JOIN task_node_placements p ON p.id = r.placement_id
WHERE r.id = ?`, string(branchRunsByNode[workflow.NodeIDOf(implA)])).Scan(&batchID); err != nil {
		t.Fatalf("query branch batch id: %v", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	arrivals, err := joinArrivals(ctx, store.queries.WithTx(tx), batchID, workflow.NodeIDOf(join))
	if err != nil {
		t.Fatalf("joinArrivals: %v", err)
	}
	joinSnapshot := nodeSnapshot(join)
	joinSnapshot.JoinInputProviders = []workflow.JoinInputProvider{{InputName: "joined", ProviderEdgeID: alternateProviderEdgeID}}
	values, ready, err := selectedJoinOutputValues(joinSnapshot, edgeContractSnapshot{OutputRequirements: []workflow.OutputRequirement{{FieldName: "joined"}}}, arrivals)
	if err != nil {
		t.Fatalf("selectedJoinOutputValues: %v", err)
	}
	if !ready || values["joined"] != "from alternate edge" {
		t.Fatalf("selected join values ready=%t values=%+v arrivals=%+v task=%s", ready, values, arrivals, task.ID)
	}
}

func TestJoinDownstreamCanUseSelectedPriorContextSource(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	planNode := nodeByKey(t, def, "plan")
	synthNode := nodeByKey(t, def, "synth")
	if _, err := store.UpdateEdge(ctx, EdgeRecord{
		ID:                workflow.EdgeID("edge-join-synth-" + string(workflowID)),
		WorkflowID:        workflowID,
		TransitionGroupID: workflow.TransitionGroupID("group-join-synth-" + string(workflowID)),
		Key:               "synth",
		TargetNodeID:      workflow.NodeIDOf(synthNode),
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "plan"},
		PromptTemplate:    "Synthesize {{.Params.joined}}.",
	}); err != nil {
		t.Fatalf("UpdateEdge join synth: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimedPlan, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun plan: %v", err)
	}
	planSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, started.RunID, claimedPlan.Generation, planSessionID); err != nil {
		t.Fatalf("AttachRunSession plan: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns branches: %v", err)
	}
	branchRuns := map[workflow.NodeID]workflow.RunID{}
	for _, run := range runs {
		if run.NodeID != workflow.NodeIDOf(planNode) {
			branchRuns[run.NodeID] = run.ID
		}
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": "a"}})
	joined := completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implB)], TransitionID: "join"})
	if len(joined.RunIDs) != 1 {
		t.Fatalf("joined result = %+v, want synth run", joined)
	}
	input, err := store.GetRunStartContext(ctx, joined.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext synth: %v", err)
	}
	if input.SourceRunID != started.RunID || input.SourceSessionID != planSessionID || input.SourceNode.Key != "plan" {
		t.Fatalf("synth context source = run %q session %q node %q, want plan run %q session %q", input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, started.RunID, planSessionID)
	}
	if input.InputValues["joined"] != "a" {
		t.Fatalf("synth input values = %+v, want selected provider value", input.InputValues)
	}
}

func TestDuplicateBranchArrivalIsRejectedAndDoesNotDuplicateJoin(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	branchRunsByNode := map[workflow.NodeID]workflow.RunID{}
	for _, run := range runs {
		if run.ID != started.RunID {
			branchRunsByNode[run.NodeID] = run.ID
		}
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRunsByNode[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": "branch a"}})
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRunsByNode[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": "branch a again"}}); !errors.Is(err, ErrRunAlreadyCompleted) {
		t.Fatalf("duplicate branch completion error = %v, want run already completed", err)
	}
	joined := completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRunsByNode[workflow.NodeIDOf(implB)], TransitionID: "join"})
	if len(joined.PlacementIDs) != 1 || len(joined.RunIDs) != 1 {
		t.Fatalf("join result = %+v, want one downstream placement/run", joined)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	joinTransitions := 0
	for _, transition := range transitions {
		if transition.TransitionID == "done" && transition.State == "applied" {
			joinTransitions++
		}
	}
	if joinTransitions != 1 {
		t.Fatalf("join transition count = %d, transitions=%+v", joinTransitions, transitions)
	}
}

func TestUnrelatedFanoutBatchDoesNotSatisfyWaitingJoin(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	waitingTask, waitingRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	otherTask, otherRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	waitingFirst := completeRun(t, ctx, store, CompleteRunRequest{RunID: waitingRuns[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": "waiting a"}})
	if len(waitingFirst.PlacementIDs) != 0 || len(waitingFirst.RunIDs) != 0 {
		t.Fatalf("waiting branch result = %+v, want no join yet", waitingFirst)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: otherRuns[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": "other a"}})
	completeRun(t, ctx, store, CompleteRunRequest{RunID: otherRuns[workflow.NodeIDOf(implB)], TransitionID: "join"})
	transitions, err := store.ListTransitions(ctx, waitingTask.ID)
	if err != nil {
		t.Fatalf("ListTransitions waiting task: %v", err)
	}
	for _, transition := range transitions {
		if transition.TransitionID == "done" {
			t.Fatalf("waiting task transitions = %+v, unrelated batch satisfied join", transitions)
		}
	}
	joined := completeRun(t, ctx, store, CompleteRunRequest{RunID: waitingRuns[workflow.NodeIDOf(implB)], TransitionID: "join"})
	if len(joined.PlacementIDs) != 1 || len(joined.RunIDs) != 1 {
		t.Fatalf("waiting final join = %+v, want downstream run after own missing branch", joined)
	}
	_ = otherTask
}
