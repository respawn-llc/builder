package workflowstore

import (
	"context"
	"errors"
	"sync"
	"testing"

	"core/server/workflow"
)

func TestCompleteRunCreatesPendingApprovalTransition(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if result.State != "pending_approval" || !result.RequiresApproval || len(result.PlacementIDs) != 0 || len(result.RunIDs) != 0 {
		t.Fatalf("completion result = %+v, want pending approval without target placement/run", result)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].State != "pending_approval" || len(transitions[1].OutputValues) != 0 {
		t.Fatalf("transitions after approval completion = %+v", transitions)
	}
	edges, err := store.ListTransitionEdges(ctx, result.TransitionID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(edges) != 1 || edges[0].State != "pending" || edges[0].TargetPlacementID != "" {
		t.Fatalf("approval edge snapshots = %+v, want pending edge without placement", edges)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].CompletedAt == nil {
		t.Fatalf("runs after pending approval = %+v, want source run completed", runs)
	}
}

func TestApprovePendingTransitionStartsStoredTargetEdgeSnapshot(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})

	approved, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if approved.State != "approved" || len(approved.PlacementIDs) != 1 || len(approved.RunIDs) != 0 {
		t.Fatalf("approved result = %+v, want approved terminal placement without run", approved)
	}
	again, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition duplicate: %v", err)
	}
	if again.State != "approved" || len(again.PlacementIDs) != 1 || again.PlacementIDs[0] != approved.PlacementIDs[0] {
		t.Fatalf("duplicate approval = %+v, want idempotent same placement", again)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].State != "approved" {
		t.Fatalf("transitions after approval = %+v", transitions)
	}
	edges, err := store.ListTransitionEdges(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(edges) != 1 || edges[0].State != "applied" || edges[0].TargetPlacementID == "" {
		t.Fatalf("approval edges = %+v, want applied edge with target placement", edges)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 3 || placements[2].ID != approved.PlacementIDs[0] || placements[2].State != "active" {
		t.Fatalf("placements after approval = %+v, want active terminal sink", placements)
	}
}

func TestApprovePendingAgentTransitionRetryPreservesRunIDs(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "done"}})

	approved, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	again, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition duplicate: %v", err)
	}
	if len(approved.RunIDs) != 1 || len(again.RunIDs) != 1 || approved.RunIDs[0] != again.RunIDs[0] {
		t.Fatalf("duplicate approval run ids approved=%+v again=%+v", approved.RunIDs, again.RunIDs)
	}
	if len(approved.PlacementIDs) != 1 || len(again.PlacementIDs) != 1 || approved.PlacementIDs[0] != again.PlacementIDs[0] {
		t.Fatalf("duplicate approval placements approved=%+v again=%+v", approved.PlacementIDs, again.PlacementIDs)
	}
}

func TestApproveTransitionWithExecutionTargetLocksNoneWithExecutableTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	pending := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "done"}})
	candidate := ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: ExecutionTargetProvenanceResolved},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}

	approved, err := store.ApproveTransitionWithExecutionTarget(ctx, pending.TransitionID, &candidate)
	if err != nil {
		t.Fatalf("ApproveTransitionWithExecutionTarget: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approved = %+v, want executable target run", approved)
	}
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	snapshot, err := executionTargetSnapshotFromTask(row)
	if err != nil {
		t.Fatalf("executionTargetSnapshotFromTask: %v", err)
	}
	if snapshot == nil || snapshot.Mode != workflow.ExecutionTargetModeNone {
		t.Fatalf("snapshot = %+v, want locked none", snapshot)
	}
}

func TestApproveTransitionWithExecutionTargetLeavesPendingApprovalUnchangedWithoutCandidate(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	pending := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "done"}})

	_, err := store.ApproveTransitionWithExecutionTarget(ctx, pending.TransitionID, nil)
	if !errors.Is(err, ErrExecutionTargetRequired) {
		t.Fatalf("ApproveTransitionWithExecutionTarget error = %v, want ErrExecutionTargetRequired", err)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].State != "pending_approval" {
		t.Fatalf("transitions = %+v, want unchanged pending approval", transitions)
	}
}

func TestApproveTransitionWithExecutionTargetRejectsCandidateAfterTargetIsLocked(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	pending := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "done"}})
	if _, err := store.db.ExecContext(ctx, `
UPDATE tasks
SET execution_target_mode = ?, execution_target_provenance = ?
WHERE id = ?`,
		string(workflow.ExecutionTargetModeNone),
		string(ExecutionTargetProvenanceResolved),
		string(task.ID)); err != nil {
		t.Fatalf("lock none target fixture: %v", err)
	}
	candidate := ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: ExecutionTargetProvenanceResolved},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}

	_, err := store.ApproveTransitionWithExecutionTarget(ctx, pending.TransitionID, &candidate)
	if !errors.Is(err, ErrExecutionTargetAlreadyLocked) {
		t.Fatalf("ApproveTransitionWithExecutionTarget error = %v, want ErrExecutionTargetAlreadyLocked", err)
	}
}

func TestApproveTransitionWithExecutionTargetRejectsCandidateForLockedManagedTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	pending := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "done"}})
	if _, err := store.db.ExecContext(ctx, `
UPDATE tasks
SET
    execution_target_mode = ?,
    execution_target_requested_ref = ?,
    execution_target_commit_oid = ?,
    execution_target_provenance = ?
WHERE id = ?`,
		string(workflow.ExecutionTargetModeHead),
		"HEAD",
		"0123456789abcdef",
		string(ExecutionTargetProvenanceResolved),
		string(task.ID)); err != nil {
		t.Fatalf("lock managed target fixture: %v", err)
	}
	candidate := ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: ExecutionTargetProvenanceResolved},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}

	if _, err := store.ApproveTransitionWithExecutionTarget(ctx, pending.TransitionID, &candidate); !errors.Is(err, ErrExecutionTargetAlreadyLocked) {
		t.Fatalf("ApproveTransitionWithExecutionTarget error = %v, want ErrExecutionTargetAlreadyLocked", err)
	}
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	snapshot, err := executionTargetSnapshotFromTask(row)
	if err != nil {
		t.Fatalf("executionTargetSnapshotFromTask: %v", err)
	}
	if snapshot == nil || snapshot.Mode != workflow.ExecutionTargetModeHead {
		t.Fatalf("snapshot after rejected candidate = %+v, want original head target", snapshot)
	}
}

func TestApprovePendingAgentTransitionUsesFrozenTargetSnapshotAfterSourceSnapshotMutation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	plan := nodeByKey(t, def, "plan")
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "done"}})
	legacySnapshot := runStartSnapshot{WorkflowID: workflowID, WorkflowRevisionSeen: currentWorkflowRevision(t, ctx, store, workflowID), Node: nodeSnapshot(plan)}
	updateRunStartSnapshot(t, ctx, store, started.RunID, legacySnapshot)

	approved, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approved run ids = %+v, want one", approved.RunIDs)
	}
	input, err := store.GetRunStartContext(ctx, approved.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext approved target: %v", err)
	}
	if input.Node.Key != "implement" || len(input.TransitionIDs) != 1 || input.TransitionIDs[0] != "done" {
		t.Fatalf("approved target context = node %s transitions %+v, want frozen implement snapshot", input.Node.Key, input.TransitionIDs)
	}
}

func TestApprovePendingTransitionIsConcurrentIdempotent(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	var wg sync.WaitGroup
	results := make([]CompleteRunResult, 2)
	errs := make([]error, 2)
	for index := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = store.ApproveTransition(context.Background(), completed.TransitionID)
		}(index)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("ApproveTransition[%d]: %v", index, err)
		}
	}
	if results[0].State != "approved" || results[1].State != "approved" || len(results[0].PlacementIDs) != 1 || len(results[1].PlacementIDs) != 1 || results[0].PlacementIDs[0] != results[1].PlacementIDs[0] {
		t.Fatalf("concurrent approval results = %+v", results)
	}
}

func TestApprovePendingJoinEdgesProgressesJoin(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_a")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_b")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	first, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": "branch a"}})
	if err != nil {
		t.Fatalf("CompleteRun branch a: %v", err)
	}
	if first.State != "pending_approval" {
		t.Fatalf("first branch result = %+v, want pending approval", first)
	}
	firstApproved, err := store.ApproveTransition(ctx, first.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition branch a: %v", err)
	}
	if len(firstApproved.PlacementIDs) != 0 || len(firstApproved.RunIDs) != 0 {
		t.Fatalf("first approval result = %+v, want join still waiting", firstApproved)
	}
	second, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implB)], TransitionID: "join"})
	if err != nil {
		t.Fatalf("CompleteRun branch b: %v", err)
	}
	secondApproved, err := store.ApproveTransition(ctx, second.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition branch b: %v", err)
	}
	if len(secondApproved.PlacementIDs) != 1 || len(secondApproved.RunIDs) != 1 {
		t.Fatalf("second approval result = %+v, want joined downstream run", secondApproved)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if transitions[len(transitions)-1].TransitionID != "done" || transitions[len(transitions)-1].OutputValues["joined"] != "branch a" {
		t.Fatalf("transitions after approved join = %+v", transitions)
	}
}

func TestApproveTransitionWithExecutionTargetLocksNoneForExecutableJoinContinuation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_a")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_b")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	first := completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": "branch a"}})
	second := completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implB)], TransitionID: "join"})
	candidate := ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: ExecutionTargetProvenanceResolved},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}

	if _, err := store.ApproveTransitionWithExecutionTarget(ctx, first.TransitionID, &candidate); err != nil {
		t.Fatalf("ApproveTransitionWithExecutionTarget first: %v", err)
	}
	secondApproved, err := store.ApproveTransitionWithExecutionTarget(ctx, second.TransitionID, nil)
	if err != nil {
		t.Fatalf("ApproveTransitionWithExecutionTarget second: %v", err)
	}
	if len(secondApproved.RunIDs) != 1 {
		t.Fatalf("second approved = %+v, want joined executable run", secondApproved)
	}
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	snapshot, err := executionTargetSnapshotFromTask(row)
	if err != nil {
		t.Fatalf("executionTargetSnapshotFromTask: %v", err)
	}
	if snapshot == nil || snapshot.Mode != workflow.ExecutionTargetModeNone {
		t.Fatalf("snapshot = %+v, want locked none", snapshot)
	}
}

func TestPendingTransitionTargetsExecutableNodeIncludesJoinContinuation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_a")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_b")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	_, branchRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	transitions := []CompleteRunResult{
		completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": "branch a"}}),
		completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implB)], TransitionID: "join"}),
	}

	for _, transition := range transitions {
		requiresTarget, err := store.PendingTransitionTargetsExecutableNode(ctx, transition.TransitionID)
		if err != nil {
			t.Fatalf("PendingTransitionTargetsExecutableNode %s: %v", transition.TransitionID, err)
		}
		if !requiresTarget {
			t.Fatalf("transition %s targeting executable join continuation did not require an execution target", transition.TransitionID)
		}
	}
}

func TestApprovePendingJoinWaitsForAllBranchApprovals(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_a")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_b")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	first, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implA)], TransitionID: "join", OutputValues: map[string]string{"joined": "branch a"}})
	if err != nil {
		t.Fatalf("CompleteRun branch a: %v", err)
	}
	second, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implB)], TransitionID: "join"})
	if err != nil {
		t.Fatalf("CompleteRun branch b: %v", err)
	}
	firstApproved, err := store.ApproveTransition(ctx, first.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition branch a: %v", err)
	}
	if len(firstApproved.PlacementIDs) != 0 || len(firstApproved.RunIDs) != 0 {
		t.Fatalf("first approval result = %+v, want join waiting for second approval", firstApproved)
	}
	secondApproved, err := store.ApproveTransition(ctx, second.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition branch b: %v", err)
	}
	if len(secondApproved.PlacementIDs) != 1 || len(secondApproved.RunIDs) != 1 {
		t.Fatalf("second approval result = %+v, want joined downstream run", secondApproved)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	doneTransitions := 0
	for _, transition := range transitions {
		if transition.TransitionID == "done" {
			doneTransitions++
		}
	}
	if doneTransitions != 1 {
		t.Fatalf("done transition count = %d, transitions=%+v", doneTransitions, transitions)
	}
}

func TestApprovalTransitionGroupWaitsAsWholeWhenAnyEdgeRequiresApproval(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	mutateRunStartSnapshot(t, ctx, store, started.RunID, func(t *testing.T, snapshot *runStartSnapshot) {
		mutateSnapshotTransition(t, snapshot, "done", func(group *transitionContractSnapshot) {
			second := group.Edges[0]
			second.Key = "second"
			second.RequiresApproval = false
			group.Edges = append(group.Edges, second)
		})
	})

	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if result.State != "pending_approval" || len(result.PlacementIDs) != 0 {
		t.Fatalf("completion result = %+v, want whole group pending approval", result)
	}
	edges, err := store.ListTransitionEdges(ctx, result.TransitionID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(edges) != 2 || edges[0].State != "pending" || edges[1].State != "pending" {
		t.Fatalf("transition edges = %+v, want both edges pending", edges)
	}
}

func TestApprovalUsesStoredEdgeSnapshotAfterGraphEdit(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	originalDone := nodeByKind(t, def, workflow.NodeKindTerminal)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	archiveID := workflow.NodeID("node-archive-" + string(workflowID))
	forceWorkflowGraphRowsForSnapshotTest(t, ctx, store, workflowID, []NodeRecord{{ID: archiveID, WorkflowID: workflowID, Key: "archive", Kind: workflow.NodeKindTerminal, DisplayName: "Archive"}}, nil, nil)
	// Intentional graph-edit fixture: mutate the live workflow edge after a
	// pending approval exists to prove approval uses the stored edge snapshot.
	if _, err := store.db.ExecContext(ctx, `
UPDATE workflow_edges
SET target_node_id = ?
WHERE edge_key = 'done'
  AND EXISTS (
      SELECT 1
      FROM workflow_transition_groups tg
      JOIN workflow_nodes source ON source.id = tg.source_node_id
      WHERE tg.id = workflow_edges.transition_group_id
        AND source.workflow_id = ?
  )`, string(archiveID), string(workflowID)); err != nil {
		t.Fatalf("edit workflow edge target: %v", err)
	}

	approved, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 3 || placements[2].ID != approved.PlacementIDs[0] || placements[2].NodeID != workflow.NodeIDOf(originalDone) {
		t.Fatalf("placements after graph edit approval = %+v, want snapshotted original target %s", placements, workflow.NodeIDOf(originalDone))
	}
}
