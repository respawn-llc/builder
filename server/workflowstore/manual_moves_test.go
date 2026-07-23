package workflowstore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"core/server/workflow"
)

func TestManualMoveToTerminalArchivesWithoutOutputValues(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(done)})
	if err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}
	if moved.State != "applied" || len(moved.PlacementIDs) != 1 || len(moved.RunIDs) != 0 {
		t.Fatalf("manual move result = %+v, want applied terminal placement", moved)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].ID == "" || transitions[1].TransitionID != "manual_done" || len(transitions[1].OutputValues) != 0 {
		t.Fatalf("manual move transition = %+v", transitions)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 3 || placements[1].State != "completed" || placements[2].NodeID != workflow.NodeIDOf(done) || placements[2].State != "active" {
		t.Fatalf("manual terminal placements = %+v, want an active terminal sink", placements)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	for _, run := range runs {
		if run.PlacementID == moved.PlacementIDs[0] {
			t.Fatalf("terminal placement unexpectedly owns run %+v", run)
		}
	}
}

func TestManualMoveSupersedesDurableStartedRunAtomically(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.ClaimRun(ctx, started.RunID, 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)

	placementsBefore, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements before restart: %v", err)
	}
	runsBefore, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns before restart: %v", err)
	}
	transitionsBefore, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions before restart: %v", err)
	}

	preparation, err := store.PrepareManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(start)})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER fail_interrupted_manual_move_transition
BEFORE INSERT ON task_transitions
BEGIN
    SELECT RAISE(ABORT, 'forced manual move failure');
END`); err != nil {
		t.Fatalf("create forced manual move failure: %v", err)
	}
	failed, err := store.ApplyManualMove(ctx, preparation, nil)
	if err == nil {
		t.Fatal("ApplyManualMove succeeded with forced transition failure")
	}
	if failed.TransitionID != "" ||
		len(failed.ResolvedApprovalTransitionProjections) != 0 ||
		len(failed.ResolvedInterruptedRunProjections) != 0 {
		t.Fatalf("failed manual move exposed resolution projections: %+v", failed)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_interrupted_manual_move_transition`); err != nil {
		t.Fatalf("drop forced manual move failure: %v", err)
	}
	placementsAfterFailure, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements after failed move: %v", err)
	}
	runsAfterFailure, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after failed move: %v", err)
	}
	transitionsAfterFailure, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions after failed move: %v", err)
	}
	if !reflect.DeepEqual(placementsAfterFailure, placementsBefore) ||
		!reflect.DeepEqual(runsAfterFailure, runsBefore) ||
		!reflect.DeepEqual(transitionsAfterFailure, transitionsBefore) {
		t.Fatalf("failed manual move changed durable state")
	}
	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(start)})
	if err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}
	if moved.State != "applied" || len(moved.PlacementIDs) != 1 || len(moved.RunIDs) != 0 {
		t.Fatalf("restart result = %+v, want applied start placement without a run", moved)
	}
	if len(moved.ResolvedInterruptedRunProjections) != 0 {
		t.Fatalf("resolved interrupted-run projections = %+v, want none for user supersession", moved.ResolvedInterruptedRunProjections)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after move: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != started.RunID || runs[0].InterruptedAt == nil || runs[0].InterruptionReason == nil || *runs[0].InterruptionReason != "user_interrupt" {
		t.Fatalf("source run after manual move = %+v, want atomically user-interrupted", runs)
	}
}

func TestManualMoveFromTerminalToStartResetsTaskToBacklog(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.RecordProtocolViolation(ctx, RecordProtocolViolationRequest{
		RunID:    started.RunID,
		Kind:     ProtocolViolationInvalidCompletion,
		MaxCount: 2,
		Detail:   `{"detail":"first attempt"}`,
	}); err != nil {
		t.Fatalf("RecordProtocolViolation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(start)})
	if err != nil {
		t.Fatalf("ManualMoveTask reset: %v", err)
	}
	if moved.State != "applied" || len(moved.PlacementIDs) != 1 || len(moved.RunIDs) != 0 {
		t.Fatalf("reset move = %+v, want applied start placement without automation", moved)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 4 || placements[2].State != "superseded" || placements[3].NodeID != workflow.NodeIDOf(start) || placements[3].State != "active" {
		t.Fatalf("reset placements = %+v, want active start placement after superseded terminal", placements)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs after reset = %+v, want no new automation", runs)
	}
	restarted, err := store.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask after reset: %v", err)
	}
	if restarted.RunID == "" || restarted.RunID == started.RunID {
		t.Fatalf("restart result = %+v, want second run", restarted)
	}
	runs, err = store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after restart: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs after restart = %+v, want second automation run", runs)
	}
	secondAttempt, err := store.RecordProtocolViolation(ctx, RecordProtocolViolationRequest{
		RunID:    restarted.RunID,
		Kind:     ProtocolViolationInvalidCompletion,
		MaxCount: 2,
		Detail:   `{"detail":"second attempt"}`,
	})
	if err != nil {
		t.Fatalf("RecordProtocolViolation after manual restart: %v", err)
	}
	if secondAttempt.Count != 1 || secondAttempt.Interrupted {
		t.Fatalf("violation after manual restart = %+v, want count 1 active", secondAttempt)
	}
}

func TestManualMoveBackwardReusesStoredOutputValues(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "reused"}})
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	plan := nodeByKey(t, def, "plan")

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(plan)})
	if err != nil {
		t.Fatalf("ManualMoveTask backward: %v", err)
	}
	if moved.State != "pending_approval" || len(moved.PlacementIDs) != 0 || len(moved.RunIDs) != 0 {
		t.Fatalf("backward move = %+v, want pending approval before executable target automation", moved)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 3 || transitions[2].OutputValues["prior_summary"] != "reused" {
		t.Fatalf("backward transition outputs = %+v, want reused prior_summary", transitions)
	}
}

func TestManualMoveFromPendingApprovalToBacklogDiscardsApproval(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "carry"}})
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	plan := nodeByKey(t, def, "plan")
	start := nodeByKind(t, def, workflow.NodeKindStart)

	// A backward move onto an agent node parks the task in pending approval
	// with no active placement: its source placement is already completed.
	approval, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(plan)})
	if err != nil {
		t.Fatalf("ManualMoveTask to approval: %v", err)
	}
	if approval.State != "pending_approval" {
		t.Fatalf("setup move state = %q, want pending_approval", approval.State)
	}

	// Moving the awaiting-approval task back to Backlog must succeed, discard
	// the pending approval, and land a single active placement at the start node.
	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(start), AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("ManualMoveTask from approval to backlog: %v", err)
	}
	if moved.State != "applied" || len(moved.PlacementIDs) != 1 {
		t.Fatalf("approval-to-backlog move = %+v, want applied with one placement", moved)
	}
	if len(moved.ResolvedApprovalTransitionProjections) != 1 {
		t.Fatalf("resolved approval projections = %+v, want one", moved.ResolvedApprovalTransitionProjections)
	}
	projection := moved.ResolvedApprovalTransitionProjections[0]
	if projection.TransitionID != approval.TransitionID || projection.ProjectID != binding.ProjectID || projection.WorkflowID != string(workflowID) || projection.TaskID != task.ID {
		t.Fatalf("resolved approval projection = %+v", projection)
	}

	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	rejected := 0
	for _, transition := range transitions {
		if transition.ID == approval.TransitionID {
			if transition.State != "rejected" {
				t.Fatalf("approval transition state = %q, want rejected", transition.State)
			}
			rejected++
		}
	}
	if rejected != 1 {
		t.Fatalf("expected the pending approval to be rejected exactly once, transitions = %+v", transitions)
	}

	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	active := 0
	for _, placement := range placements {
		if placement.State == "active" {
			active++
			if placement.NodeID != workflow.NodeIDOf(start) {
				t.Fatalf("active placement node = %q, want start node %q", placement.NodeID, workflow.NodeIDOf(start))
			}
		}
	}
	if active != 1 {
		t.Fatalf("expected exactly one active placement at the start node, placements = %+v", placements)
	}
}

func TestManualMoveContinueSessionRequiresSourceSession(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeContinueSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	impl := nodeByKey(t, def, "implement")

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(impl), OutputValues: map[string]string{"prior_summary": "done"}})
	if !errors.Is(err, ErrManualMoveContinueSessionNeedsSource) {
		t.Fatalf("ManualMoveTask continue_session error = %v, want source session requirement", err)
	}
}

func TestManualMoveRejectsSelectedContextSource(t *testing.T) {
	type completion struct {
		nodeKey      string
		transitionID string
		outputValues map[string]string
	}
	completions := []completion{
		{nodeKey: "plan", transitionID: "implement", outputValues: map[string]string{"summary": "plan done"}},
		{nodeKey: "implementation", transitionID: "accept", outputValues: map[string]string{"summary": "implemented"}},
		{nodeKey: "acceptance", transitionID: "open_pr", outputValues: map[string]string{"acceptance_decision": "approved"}},
	}
	cases := []struct {
		name          string
		completedRuns int
		targetNodeKey string
		moveOutputKey string
	}{
		{
			name:          "selected node forward",
			completedRuns: 2,
			targetNodeKey: "open_pr",
			moveOutputKey: "acceptance_decision",
		},
		{
			name:          "selected node historical backward",
			completedRuns: 3,
			targetNodeKey: "acceptance",
			moveOutputKey: "summary",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, binding := newTestStoreContext(t)
			workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
			def, _, err := store.GetDefinition(ctx, workflowID)
			if err != nil {
				t.Fatalf("GetDefinition: %v", err)
			}
			linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
			task := createDefaultTask(t, ctx, store, binding.ProjectID)
			startTask(t, ctx, store, task.ID)
			for _, completed := range completions[:tc.completedRuns] {
				node := nodeByKey(t, def, completed.nodeKey)
				run := runForNode(t, ctx, store, task.ID, workflow.NodeIDOf(node))
				completeRun(t, ctx, store, CompleteRunRequest{RunID: run.ID, TransitionID: completed.transitionID, OutputValues: completed.outputValues})
			}

			targetNode := nodeByKey(t, def, tc.targetNodeKey)
			_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(targetNode), OutputValues: map[string]string{tc.moveOutputKey: "manual"}})
			if !errors.Is(err, ErrManualMoveSelectedContextSource) {
				t.Fatalf("ManualMoveTask context source error = %v, want %v", err, ErrManualMoveSelectedContextSource)
			}
		})
	}
}

func TestManualMovePendingApprovalRequiresSourceRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(agent)})
	if !errors.Is(err, ErrManualMoveApprovalNeedsSourceRun) {
		t.Fatalf("ManualMoveTask missing source run error = %v, want source run requirement", err)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 1 || placements[0].State != "active" {
		t.Fatalf("placements after rejected manual move = %+v, want original active placement", placements)
	}
}

type manualMoveExecutableFixture struct {
	ctx          context.Context
	store        *Store
	taskID       workflow.TaskID
	targetNodeID workflow.NodeID
	candidate    *ExecutionTargetCandidate
}

func newManualMoveExecutableFixture(t *testing.T, requiresApproval bool) manualMoveExecutableFixture {
	t.Helper()
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	if requiresApproval {
		requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	return manualMoveExecutableFixture{
		ctx:          ctx,
		store:        store,
		taskID:       task.ID,
		targetNodeID: workflow.NodeIDOf(nodeByKey(t, def, "implement")),
		candidate:    sourceExecutionTargetCandidate(binding.WorkspaceID, binding.CanonicalRoot),
	}
}

func (f manualMoveExecutableFixture) request() ManualMoveRequest {
	return ManualMoveRequest{
		TaskID:       f.taskID,
		TargetNodeID: f.targetNodeID,
		OutputValues: map[string]string{"prior_summary": "done"},
		AutoApprove:  true,
	}
}

func (f manualMoveExecutableFixture) snapshot(t *testing.T) *ExecutionTargetSnapshot {
	t.Helper()
	_, snapshot := executionTargetFactsForTask(t, f.ctx, f.store, f.taskID)
	return snapshot
}

func TestManualMoveAutoApprovedExecutableTargetLocksNoneAndCreatesRunAtomically(t *testing.T) {
	fixture := newManualMoveExecutableFixture(t, false)
	preparation, err := fixture.store.PrepareManualMove(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if !preparation.RequiresExecutionTarget() {
		t.Fatal("auto-approved executable move does not require an execution target")
	}
	moved, err := fixture.store.ApplyManualMove(fixture.ctx, preparation, fixture.candidate)
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if moved.State != "applied" || len(moved.PlacementIDs) != 1 || len(moved.RunIDs) != 1 {
		t.Fatalf("manual auto-approved executable move = %+v, want applied placement and run", moved)
	}
	transitions, err := fixture.store.ListTransitions(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].State != "applied" {
		t.Fatalf("transitions = %+v, want no intermediate pending transition", transitions)
	}
	snapshot := fixture.snapshot(t)
	if snapshot == nil || snapshot.Mode != workflow.ExecutionTargetModeNone {
		t.Fatalf("snapshot = %+v, want locked none", snapshot)
	}
}

func TestManualMoveApprovalRequiredExecutableTargetDefersTargetLock(t *testing.T) {
	fixture := newManualMoveExecutableFixture(t, true)
	moved, err := fixture.store.ManualMoveTask(fixture.ctx, fixture.request())
	if err != nil {
		t.Fatalf("ManualMoveTask approval-required executable: %v", err)
	}
	if moved.State != "pending_approval" || len(moved.PlacementIDs) != 0 || len(moved.RunIDs) != 0 || !moved.RequiresApproval {
		t.Fatalf("manual approval-required executable move = %+v, want pending approval without automation", moved)
	}
	snapshot := fixture.snapshot(t)
	if snapshot != nil {
		t.Fatalf("snapshot = %+v, want target to remain unlocked until approval", snapshot)
	}
}

func TestManualMoveAutoApprovedExecutableTargetFailureLeavesSourceUntouched(t *testing.T) {
	fixture := newManualMoveExecutableFixture(t, false)
	placementsBefore, err := fixture.store.ListPlacements(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatalf("ListPlacements before move: %v", err)
	}
	transitionsBefore, err := fixture.store.ListTransitions(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatalf("ListTransitions before move: %v", err)
	}

	_, err = fixture.store.ManualMoveTask(fixture.ctx, fixture.request())
	if !errors.Is(err, ErrExecutionTargetRequired) {
		t.Fatalf("ManualMoveTask missing execution target error = %v, want ErrExecutionTargetRequired", err)
	}
	placementsAfter, err := fixture.store.ListPlacements(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatalf("ListPlacements after move: %v", err)
	}
	transitionsAfter, err := fixture.store.ListTransitions(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatalf("ListTransitions after move: %v", err)
	}
	if !reflect.DeepEqual(placementsAfter, placementsBefore) || !reflect.DeepEqual(transitionsAfter, transitionsBefore) {
		t.Fatalf("failed auto-approved move mutated task: placements=%+v transitions=%+v", placementsAfter, transitionsAfter)
	}
}

func TestManualMoveAutoApprovedExecutableRejectsCandidateForLockedManagedTarget(t *testing.T) {
	fixture := newManualMoveExecutableFixture(t, false)
	setTaskExecutionTargetFixture(t, fixture.ctx, fixture.store, fixture.taskID, workflow.ExecutionTargetModeHead, nil)

	req := fixture.request()
	req.ExecutionTarget = fixture.candidate
	_, err := fixture.store.ManualMoveTask(fixture.ctx, req)
	if !errors.Is(err, ErrExecutionTargetAlreadyLocked) {
		t.Fatalf("ManualMoveTask error = %v, want ErrExecutionTargetAlreadyLocked", err)
	}
	snapshot := fixture.snapshot(t)
	if snapshot == nil || snapshot.Mode != workflow.ExecutionTargetModeHead {
		t.Fatalf("snapshot = %+v, want original head target", snapshot)
	}
}

func TestManualMoveRejectsMissingEdgeExecutableTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	impl := nodeByKey(t, def, "implement")

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(impl), AllowMissingEdge: true})
	if !errors.Is(err, ErrManualMoveExecutableTargetNeedsEdge) {
		t.Fatalf("ManualMoveTask missing executable edge error = %v, want executable edge requirement", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs after rejected missing-edge executable move = %+v, want none", runs)
	}
}

func TestManualMoveRejectsActiveParallelBatch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	join := nodeByKey(t, def, "join")
	if _, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(join), OutputValues: map[string]string{"summary": "manual"}}); !errors.Is(err, ErrManualMoveDuringParallelBatch) {
		t.Fatalf("ManualMoveTask active parallel error = %v, want active parallel rejection", err)
	}
	if len(branchRuns) != 2 {
		t.Fatalf("branch runs = %+v, want two active branches", branchRuns)
	}
}
