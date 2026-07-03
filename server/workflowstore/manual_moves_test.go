package workflowstore

import (
	"errors"
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
	if len(placements) != 3 || placements[1].State != "completed" || placements[2].NodeID != workflow.NodeIDOf(done) || placements[2].State != "completed" {
		t.Fatalf("manual terminal placements = %+v", placements)
	}
}

func TestManualMoveRejectsStartedRun(t *testing.T) {
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
	done := nodeByKind(t, def, workflow.NodeKindTerminal)

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(done)})
	if !errors.Is(err, ErrManualMoveDuringActiveRun) {
		t.Fatalf("ManualMoveTask started run error = %v, want active-run rejection", err)
	}

	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].CompletedAt != 0 || runs[0].InterruptedAt != 0 {
		t.Fatalf("runs after rejected manual move = %+v, want original active run", runs)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 2 || placements[1].State != "active" {
		t.Fatalf("placements after rejected manual move = %+v, want original active placement", placements)
	}
}

func TestManualMoveFromTerminalToStartResetsTaskToBacklog(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
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
	if len(moved.ResolvedApprovalTransitionIDs) != 1 || moved.ResolvedApprovalTransitionIDs[0] != approval.TransitionID {
		t.Fatalf("resolved approval ids = %+v, want %s", moved.ResolvedApprovalTransitionIDs, approval.TransitionID)
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

func TestManualMoveRejectsSelectedContextSourceV1(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after plan: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	openPRNode := nodeByKey(t, def, "open_pr")
	var implementationRun RunRecord
	for _, run := range runs {
		if run.NodeID == workflow.NodeIDOf(implementationNode) {
			implementationRun = run
		}
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(openPRNode), OutputValues: map[string]string{"acceptance_decision": "approved"}})
	if !errors.Is(err, ErrManualMoveSelectedContextSource) {
		t.Fatalf("ManualMoveTask selected context source error = %v, want unsupported selected context source", err)
	}
}

func TestBackwardManualMoveRejectsHistoricalSelectedContextSourceV1(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	implementationRun := runForNode(t, ctx, store, task.ID, workflow.NodeIDOf(implementationNode))
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	acceptanceRun := runForNode(t, ctx, store, task.ID, workflow.NodeIDOf(acceptanceNode))
	completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(acceptanceNode), OutputValues: map[string]string{"summary": "needs recheck"}})
	if !errors.Is(err, ErrManualMoveSelectedContextSource) {
		t.Fatalf("backward ManualMoveTask selected context source error = %v, want unsupported selected context source", err)
	}
}

func TestManualMoveRejectsPreviousTargetContextSourceV1(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	addOutputFieldToNode(t, ctx, store, workflowID, acceptanceNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
	addPreviousTargetReworkEdge(t, ctx, store, workflowID, workflow.NodeIDOf(acceptanceNode), workflow.NodeIDOf(implementationNode), false)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	implementationRun := runForNode(t, ctx, store, task.ID, workflow.NodeIDOf(implementationNode))
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(implementationNode), OutputValues: map[string]string{"summary": "needs changes"}})
	if !errors.Is(err, ErrManualMovePreviousTargetContext) {
		t.Fatalf("ManualMoveTask previous target context source error = %v, want unsupported previous target context source", err)
	}
}

func TestManualMoveRejectsPreviousTargetOrNewContextSourceV1(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	reworkGroup := workflow.TransitionGroupID("group-previous-target-or-new-manual-rework-" + string(workflowID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: reworkGroup, WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(acceptanceNode), TransitionID: "rework", DisplayName: "Rework"}); err != nil {
		t.Fatalf("AddTransitionGroup rework: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-previous-target-or-new-manual-rework-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: reworkGroup, Key: "rework", TargetNodeID: workflow.NodeIDOf(implementationNode), ContextMode: workflow.ContextModeContinueSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}, PromptTemplate: "Rework."}); err != nil {
		t.Fatalf("AddEdge rework: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	implementationRun := runForNode(t, ctx, store, task.ID, workflow.NodeIDOf(implementationNode))
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(implementationNode)})
	if !errors.Is(err, ErrManualMovePreviousTargetContext) {
		t.Fatalf("ManualMoveTask previous target or new context source error = %v, want unsupported previous target context source", err)
	}
}

func TestBackwardManualMoveRejectsHistoricalPreviousTargetContextSourceV1(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	openPRNode := nodeByKey(t, def, "open_pr")
	addOutputFieldToNode(t, ctx, store, workflowID, openPRNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
	addPreviousTargetReworkEdge(t, ctx, store, workflowID, workflow.NodeIDOf(openPRNode), workflow.NodeIDOf(implementationNode), false)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	implementationRun := runForNode(t, ctx, store, task.ID, workflow.NodeIDOf(implementationNode))
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	acceptanceRun := runForNode(t, ctx, store, task.ID, workflow.NodeIDOf(acceptanceNode))
	completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	openPRRun := runForNode(t, ctx, store, task.ID, workflow.NodeIDOf(openPRNode))
	completeRun(t, ctx, store, CompleteRunRequest{RunID: openPRRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(openPRNode), OutputValues: map[string]string{"summary": "needs recheck"}})
	if !errors.Is(err, ErrManualMovePreviousTargetContext) {
		t.Fatalf("backward ManualMoveTask previous target context source error = %v, want unsupported previous target context source", err)
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

func TestManualMoveExecutableTargetRequiresApprovalBeforeAutomation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	impl := nodeByKey(t, def, "implement")

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(impl), OutputValues: map[string]string{"prior_summary": "done"}})
	if err != nil {
		t.Fatalf("ManualMoveTask executable: %v", err)
	}
	if moved.State != "pending_approval" || len(moved.PlacementIDs) != 0 || len(moved.RunIDs) != 0 {
		t.Fatalf("manual executable move = %+v, want pending approval without automation", moved)
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
