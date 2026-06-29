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

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: done.ID})
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
	if len(placements) != 3 || placements[1].State != "completed" || placements[2].NodeID != done.ID || placements[2].State != "active" {
		t.Fatalf("manual terminal placements = %+v", placements)
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

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: start.ID})
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
	if len(placements) != 4 || placements[2].State != "completed" || placements[3].NodeID != start.ID || placements[3].State != "active" {
		t.Fatalf("reset placements = %+v, want active start placement after completed terminal", placements)
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

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: plan.ID})
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
	approval, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: plan.ID})
	if err != nil {
		t.Fatalf("ManualMoveTask to approval: %v", err)
	}
	if approval.State != "pending_approval" {
		t.Fatalf("setup move state = %q, want pending_approval", approval.State)
	}

	// Moving the awaiting-approval task back to Backlog must succeed, discard
	// the pending approval, and land a single active placement at the start node.
	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: start.ID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("ManualMoveTask from approval to backlog: %v", err)
	}
	if moved.State != "applied" || len(moved.PlacementIDs) != 1 {
		t.Fatalf("approval-to-backlog move = %+v, want applied with one placement", moved)
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
			if placement.NodeID != start.ID {
				t.Fatalf("active placement node = %q, want start node %q", placement.NodeID, start.ID)
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

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: impl.ID, OutputValues: map[string]string{"prior_summary": "done"}})
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
		if run.NodeID == implementationNode.ID {
			implementationRun = run
		}
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: openPRNode.ID, OutputValues: map[string]string{"acceptance_decision": "approved"}})
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
	implementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	acceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: acceptanceNode.ID, OutputValues: map[string]string{"summary": "needs recheck"}})
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
	addPreviousTargetReworkEdge(t, ctx, store, workflowID, acceptanceNode.ID, implementationNode.ID, false)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	implementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: implementationNode.ID, OutputValues: map[string]string{"summary": "needs changes"}})
	if !errors.Is(err, ErrManualMovePreviousTargetContext) {
		t.Fatalf("ManualMoveTask previous target context source error = %v, want unsupported previous target context source", err)
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
	addPreviousTargetReworkEdge(t, ctx, store, workflowID, openPRNode.ID, implementationNode.ID, false)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	implementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	acceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	openPRRun := runForNode(t, ctx, store, task.ID, openPRNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: openPRRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: openPRNode.ID, OutputValues: map[string]string{"summary": "needs recheck"}})
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

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: agent.ID})
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

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: impl.ID, OutputValues: map[string]string{"prior_summary": "done"}})
	if err != nil {
		t.Fatalf("ManualMoveTask executable: %v", err)
	}
	if moved.State != "pending_approval" || len(moved.PlacementIDs) != 0 || len(moved.RunIDs) != 0 {
		t.Fatalf("manual executable move = %+v, want pending approval without automation", moved)
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
	if _, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: join.ID, OutputValues: map[string]string{"summary": "manual"}}); !errors.Is(err, ErrManualMoveDuringParallelBatch) {
		t.Fatalf("ManualMoveTask active parallel error = %v, want active parallel rejection", err)
	}
	if len(branchRuns) != 2 {
		t.Fatalf("branch runs = %+v, want two active branches", branchRuns)
	}
}
