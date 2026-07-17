package workflowstore

import (
	"context"
	"errors"
	"sync"
	"testing"

	"core/server/workflow"
)

type pendingAgentApprovalFixture struct {
	ctx          context.Context
	store        *Store
	workflowID   workflow.WorkflowID
	taskID       workflow.TaskID
	sourceRunID  workflow.RunID
	transitionID workflow.TransitionID
	candidate    *ExecutionTargetCandidate
}

func newPendingAgentApprovalFixture(t *testing.T) pendingAgentApprovalFixture {
	t.Helper()
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	pending := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "done"}})
	return pendingAgentApprovalFixture{
		ctx:          ctx,
		store:        store,
		workflowID:   workflowID,
		taskID:       task.ID,
		sourceRunID:  started.RunID,
		transitionID: pending.TransitionID,
		candidate:    sourceExecutionTargetCandidate(binding.WorkspaceID, binding.CanonicalRoot),
	}
}

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
	fixture := newPendingAgentApprovalFixture(t)

	approved, err := fixture.store.ApproveTransition(fixture.ctx, fixture.transitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	again, err := fixture.store.ApproveTransition(fixture.ctx, fixture.transitionID)
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

func TestApproveTransitionWithExecutionTargetContracts(t *testing.T) {
	noneMode := workflow.ExecutionTargetModeNone
	headMode := workflow.ExecutionTargetModeHead
	for _, tc := range []struct {
		name             string
		initialMode      *workflow.ExecutionTargetMode
		provideCandidate bool
		wantErr          error
		wantSnapshot     *workflow.ExecutionTargetMode
		wantRun          bool
		wantPending      bool
	}{
		{name: "unlocked candidate locks none and creates run", provideCandidate: true, wantSnapshot: &noneMode, wantRun: true},
		{name: "unlocked missing candidate remains pending", wantErr: ErrExecutionTargetRequired, wantPending: true},
		{name: "locked none rejects candidate", initialMode: &noneMode, provideCandidate: true, wantErr: ErrExecutionTargetAlreadyLocked, wantSnapshot: &noneMode},
		{name: "locked managed rejects candidate and preserves snapshot", initialMode: &headMode, provideCandidate: true, wantErr: ErrExecutionTargetAlreadyLocked, wantSnapshot: &headMode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newPendingAgentApprovalFixture(t)
			if tc.initialMode != nil {
				setTaskExecutionTargetFixture(t, fixture.ctx, fixture.store, fixture.taskID, *tc.initialMode, nil)
			}
			var candidate *ExecutionTargetCandidate
			if tc.provideCandidate {
				candidate = fixture.candidate
			}

			approved, err := fixture.store.ApproveTransitionWithExecutionTarget(fixture.ctx, fixture.transitionID, candidate)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ApproveTransitionWithExecutionTarget error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantRun && len(approved.RunIDs) != 1 {
				t.Fatalf("approved = %+v, want executable target run", approved)
			}
			if tc.wantPending {
				transitions, err := fixture.store.ListTransitions(fixture.ctx, fixture.taskID)
				if err != nil {
					t.Fatalf("ListTransitions: %v", err)
				}
				if len(transitions) != 2 || transitions[1].State != "pending_approval" {
					t.Fatalf("transitions = %+v, want unchanged pending approval", transitions)
				}
			}
			_, snapshot := executionTargetFactsForTask(t, fixture.ctx, fixture.store, fixture.taskID)
			if tc.wantSnapshot == nil {
				if snapshot != nil {
					t.Fatalf("snapshot = %+v, want unlocked task", snapshot)
				}
			} else if snapshot == nil || snapshot.Mode != *tc.wantSnapshot {
				t.Fatalf("snapshot = %+v, want mode %q", snapshot, *tc.wantSnapshot)
			}
		})
	}
}

func TestApprovePendingAgentTransitionUsesFrozenTargetSnapshotAfterSourceSnapshotMutation(t *testing.T) {
	fixture := newPendingAgentApprovalFixture(t)
	def, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	plan := nodeByKey(t, def, "plan")
	legacySnapshot := runStartSnapshot{WorkflowID: fixture.workflowID, WorkflowRevisionSeen: currentWorkflowRevision(t, fixture.ctx, fixture.store, fixture.workflowID), Node: nodeSnapshot(plan)}
	updateRunStartSnapshot(t, fixture.ctx, fixture.store, fixture.sourceRunID, legacySnapshot)

	approved, err := fixture.store.ApproveTransition(fixture.ctx, fixture.transitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approved run ids = %+v, want one", approved.RunIDs)
	}
	input, err := fixture.store.GetRunStartContext(fixture.ctx, approved.RunIDs[0])
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

func TestApprovePendingJoinWaitsForBranchCompletionAndApproval(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		completeSecondBeforeFirstApproval bool
	}{
		{name: "first approval precedes second completion"},
		{name: "both completions precede first approval", completeSecondBeforeFirstApproval: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			var second CompleteRunResult
			if tc.completeSecondBeforeFirstApproval {
				second = completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implB)], TransitionID: "join"})
			}

			firstApproved, err := store.ApproveTransition(ctx, first.TransitionID)
			if err != nil {
				t.Fatalf("ApproveTransition branch a: %v", err)
			}
			if len(firstApproved.PlacementIDs) != 0 || len(firstApproved.RunIDs) != 0 {
				t.Fatalf("first approval result = %+v, want join waiting", firstApproved)
			}
			if !tc.completeSecondBeforeFirstApproval {
				second = completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRuns[workflow.NodeIDOf(implB)], TransitionID: "join"})
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
			doneCount := 0
			joined := ""
			for _, transition := range transitions {
				if transition.TransitionID == "done" {
					doneCount++
					joined = transition.OutputValues["joined"]
				}
			}
			if doneCount != 1 || joined != "branch a" {
				t.Fatalf("approved join transitions = %+v, want one done carrying branch a", transitions)
			}
		})
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
	candidate := sourceExecutionTargetCandidate(binding.WorkspaceID, binding.CanonicalRoot)

	if _, err := store.ApproveTransitionWithExecutionTarget(ctx, first.TransitionID, candidate); err != nil {
		t.Fatalf("ApproveTransitionWithExecutionTarget first: %v", err)
	}
	secondApproved, err := store.ApproveTransitionWithExecutionTarget(ctx, second.TransitionID, nil)
	if err != nil {
		t.Fatalf("ApproveTransitionWithExecutionTarget second: %v", err)
	}
	if len(secondApproved.RunIDs) != 1 {
		t.Fatalf("second approved = %+v, want joined executable run", secondApproved)
	}
	_, snapshot := executionTargetFactsForTask(t, ctx, store, task.ID)
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
