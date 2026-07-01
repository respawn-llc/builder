package workflowstore

import (
	"errors"
	"testing"
	"time"

	"core/server/workflow"
)

func TestRunStartContextUsesSelectedPriorNodeSession(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after plan: %v", err)
	}
	var implementationRun RunRecord
	for _, run := range runs {
		if run.NodeID == implementationNode.ID {
			implementationRun = run
		}
	}
	if implementationRun.ID == "" {
		t.Fatalf("implementation run not found: %+v", runs)
	}
	claimedImplementation, err := store.ClaimRun(ctx, implementationRun.ID, implementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun implementation: %v", err)
	}
	implementationSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, implementationRun.ID, claimedImplementation.Generation, implementationSessionID); err != nil {
		t.Fatalf("AttachRunSession implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	runs, err = store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after implementation: %v", err)
	}
	var acceptanceRun RunRecord
	for _, run := range runs {
		if run.NodeID == acceptanceNode.ID {
			acceptanceRun = run
		}
	}
	if acceptanceRun.ID == "" {
		t.Fatalf("acceptance run not found: %+v", runs)
	}
	claimedAcceptance, err := store.ClaimRun(ctx, acceptanceRun.ID, acceptanceRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun acceptance: %v", err)
	}
	acceptanceSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, acceptanceRun.ID, claimedAcceptance.Generation, acceptanceSessionID); err != nil {
		t.Fatalf("AttachRunSession acceptance: %v", err)
	}
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	if len(completed.RunIDs) != 1 {
		t.Fatalf("acceptance completion = %+v, want open_pr run", completed)
	}
	input, err := store.GetRunStartContext(ctx, completed.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext open_pr: %v", err)
	}
	if input.SourceRunID != implementationRun.ID || input.SourceSessionID != implementationSessionID || input.SourceNode.Key != "implementation" {
		t.Fatalf("open_pr context source = run %q session %q node %q, want implementation run %q session %q", input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, implementationRun.ID, implementationSessionID)
	}
	if input.AcceptedTransitionPath.SourceNodeDisplayName != "Acceptance" || input.AcceptedTransitionPath.TargetNodeDisplayName != "Open PR" {
		t.Fatalf("open_pr accepted transition path = %+v, want Acceptance -> Open PR", input.AcceptedTransitionPath)
	}
	if input.InputValues["acceptance_decision"] != "approved" {
		t.Fatalf("open_pr input values = %+v, want immediate acceptance output", input.InputValues)
	}
}

func TestSelectedContextSourceUsesLatestCompletedPriorNodeRun(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	reworkGroup := workflow.TransitionGroupID("group-rework-" + string(workflowID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: reworkGroup, WorkflowID: workflowID, SourceNodeID: acceptanceNode.ID, TransitionID: "rework", DisplayName: "Rework"}); err != nil {
		t.Fatalf("AddTransitionGroup rework: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-rework-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: reworkGroup, Key: "rework", TargetNodeID: implementationNode.ID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Rework summary."}}}); err != nil {
		t.Fatalf("AddEdge rework: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	firstImplementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	firstClaim, err := store.ClaimRun(ctx, firstImplementationRun.ID, firstImplementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun first implementation: %v", err)
	}
	firstSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, firstImplementationRun.ID, firstClaim.Generation, firstSessionID); err != nil {
		t.Fatalf("AttachRunSession first implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: firstImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "first implementation"}})
	firstAcceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: firstAcceptanceRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})
	secondImplementationRun := latestRunForNode(t, ctx, store, task.ID, implementationNode.ID)
	if secondImplementationRun.ID == firstImplementationRun.ID {
		t.Fatalf("second implementation run = first run %q", secondImplementationRun.ID)
	}
	secondClaim, err := store.ClaimRun(ctx, secondImplementationRun.ID, secondImplementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun second implementation: %v", err)
	}
	secondSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, secondImplementationRun.ID, secondClaim.Generation, secondSessionID); err != nil {
		t.Fatalf("AttachRunSession second implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: secondImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "second implementation"}})
	secondAcceptanceRun := latestRunForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: secondAcceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	input, err := store.GetRunStartContext(ctx, completed.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext open_pr: %v", err)
	}
	if input.SourceRunID != secondImplementationRun.ID || input.SourceSessionID != secondSessionID {
		t.Fatalf("open_pr source run/session = %q/%q, want latest implementation %q/%q; first session was %q", input.SourceRunID, input.SourceSessionID, secondImplementationRun.ID, secondSessionID, firstSessionID)
	}
}

func TestPreviousTargetContextSourceUsesLatestCompletedTargetRun(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
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
	firstImplementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	firstClaim, err := store.ClaimRun(ctx, firstImplementationRun.ID, firstImplementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun first implementation: %v", err)
	}
	firstSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, firstImplementationRun.ID, firstClaim.Generation, firstSessionID); err != nil {
		t.Fatalf("AttachRunSession first implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: firstImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "first implementation"}})
	firstAcceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	firstRework := completeRun(t, ctx, store, CompleteRunRequest{RunID: firstAcceptanceRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})
	if len(firstRework.RunIDs) != 1 {
		t.Fatalf("first rework result = %+v, want one implementation run", firstRework)
	}
	firstReworkInput, err := store.GetRunStartContext(ctx, firstRework.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext first rework: %v", err)
	}
	if firstReworkInput.SourceRunID != firstImplementationRun.ID || firstReworkInput.SourceSessionID != firstSessionID || firstReworkInput.SourceNode.Key != "implementation" {
		t.Fatalf("first rework source = run %q session %q node %q, want first implementation %q/%q", firstReworkInput.SourceRunID, firstReworkInput.SourceSessionID, firstReworkInput.SourceNode.Key, firstImplementationRun.ID, firstSessionID)
	}
	secondImplementationRun := latestRunForNode(t, ctx, store, task.ID, implementationNode.ID)
	secondClaim, err := store.ClaimRun(ctx, secondImplementationRun.ID, secondImplementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun second implementation: %v", err)
	}
	secondSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, secondImplementationRun.ID, secondClaim.Generation, secondSessionID); err != nil {
		t.Fatalf("AttachRunSession second implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: secondImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "second implementation"}})
	secondAcceptanceRun := latestRunForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	secondRework := completeRun(t, ctx, store, CompleteRunRequest{RunID: secondAcceptanceRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "still needs changes"}})
	if len(secondRework.RunIDs) != 1 {
		t.Fatalf("second rework result = %+v, want one implementation run", secondRework)
	}
	secondReworkInput, err := store.GetRunStartContext(ctx, secondRework.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext second rework: %v", err)
	}
	if secondReworkInput.SourceRunID != secondImplementationRun.ID || secondReworkInput.SourceSessionID != secondSessionID {
		t.Fatalf("second rework source run/session = %q/%q, want latest implementation %q/%q; first session was %q", secondReworkInput.SourceRunID, secondReworkInput.SourceSessionID, secondImplementationRun.ID, secondSessionID, firstSessionID)
	}
}

func TestPreviousTargetOrNewContextSourceFallsBackThenContinuesTargetRun(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	acceptEdge := edgeByKey(t, def, "accept")
	if _, err := store.UpdateEdge(ctx, EdgeRecord{ID: acceptEdge.ID, WorkflowID: workflowID, TransitionGroupID: acceptEdge.TransitionGroupID, Key: acceptEdge.Key, TargetNodeID: acceptEdge.TargetNodeID, ContextMode: workflow.ContextModeContinueSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}, PromptTemplate: acceptEdge.PromptTemplate, Parameters: acceptEdge.Parameters}); err != nil {
		t.Fatalf("UpdateEdge accept context source: %v", err)
	}
	reworkGroup := workflow.TransitionGroupID("group-previous-target-or-new-rework-" + string(workflowID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: reworkGroup, WorkflowID: workflowID, SourceNodeID: acceptanceNode.ID, TransitionID: "rework", DisplayName: "Rework"}); err != nil {
		t.Fatalf("AddTransitionGroup rework: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-previous-target-or-new-rework-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: reworkGroup, Key: "rework", TargetNodeID: implementationNode.ID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Rework."}); err != nil {
		t.Fatalf("AddEdge rework: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	firstImplementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: firstImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "first implementation"}})
	firstAcceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	firstAcceptanceInput, err := store.GetRunStartContext(ctx, firstAcceptanceRun.ID)
	if err != nil {
		t.Fatalf("GetRunStartContext first acceptance: %v", err)
	}
	if firstAcceptanceInput.ContextMode != workflow.ContextModeNewSession || firstAcceptanceInput.SourceRunID != "" || firstAcceptanceInput.SourceSessionID != "" {
		t.Fatalf("first acceptance context = mode %q source %q/%q, want new session without source", firstAcceptanceInput.ContextMode, firstAcceptanceInput.SourceRunID, firstAcceptanceInput.SourceSessionID)
	}
	firstAcceptanceClaim, err := store.ClaimRun(ctx, firstAcceptanceRun.ID, firstAcceptanceRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun first acceptance: %v", err)
	}
	firstAcceptanceSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, firstAcceptanceRun.ID, firstAcceptanceClaim.Generation, firstAcceptanceSessionID); err != nil {
		t.Fatalf("AttachRunSession first acceptance: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: firstAcceptanceRun.ID, TransitionID: "rework"})
	secondImplementationRun := latestRunForNode(t, ctx, store, task.ID, implementationNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: secondImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "second implementation"}})
	secondAcceptanceRun := latestRunForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	if secondAcceptanceRun.ID == firstAcceptanceRun.ID {
		t.Fatalf("second acceptance run = first run %q", secondAcceptanceRun.ID)
	}
	secondAcceptanceInput, err := store.GetRunStartContext(ctx, secondAcceptanceRun.ID)
	if err != nil {
		t.Fatalf("GetRunStartContext second acceptance: %v", err)
	}
	if secondAcceptanceInput.ContextMode != workflow.ContextModeContinueSession || secondAcceptanceInput.SourceRunID != firstAcceptanceRun.ID || secondAcceptanceInput.SourceSessionID != firstAcceptanceSessionID {
		t.Fatalf("second acceptance context = mode %q source %q/%q, want continue first acceptance %q/%q", secondAcceptanceInput.ContextMode, secondAcceptanceInput.SourceRunID, secondAcceptanceInput.SourceSessionID, firstAcceptanceRun.ID, firstAcceptanceSessionID)
	}
}

func TestPendingApprovalFreezesPreviousTargetOrNewFallbackToNew(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	acceptEdge := edgeByKey(t, def, "accept")
	if _, err := store.UpdateEdge(ctx, EdgeRecord{ID: acceptEdge.ID, WorkflowID: workflowID, TransitionGroupID: acceptEdge.TransitionGroupID, Key: acceptEdge.Key, TargetNodeID: acceptEdge.TargetNodeID, ContextMode: workflow.ContextModeContinueSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}, RequiresApproval: true, PromptTemplate: acceptEdge.PromptTemplate, Parameters: acceptEdge.Parameters}); err != nil {
		t.Fatalf("UpdateEdge accept context source: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	implementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	pending := completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "first implementation"}})
	if pending.State != "pending_approval" {
		t.Fatalf("accept completion = %+v, want pending approval", pending)
	}
	competingSessionID := createTestSession(t, ctx, store, binding, cfg)
	insertCompletedRunForNodeAfterTransition(t, ctx, store, task.ID, acceptanceNode.ID, implementationRun.ID, competingSessionID, pending.TransitionID)
	approved, err := store.ApproveTransition(ctx, pending.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approval result = %+v, want one acceptance run", approved)
	}
	input, err := store.GetRunStartContext(ctx, approved.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext approved acceptance: %v", err)
	}
	if input.ContextMode != workflow.ContextModeNewSession || input.SourceRunID != "" || input.SourceSessionID != "" {
		t.Fatalf("approved fallback context = mode %q source %q/%q, want frozen new session without source; competing session was %q", input.ContextMode, input.SourceRunID, input.SourceSessionID, competingSessionID)
	}
}

func TestPendingApprovalFreezesPreviousTargetOrNewPriorTargetRun(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	acceptEdge := edgeByKey(t, def, "accept")
	if _, err := store.UpdateEdge(ctx, EdgeRecord{ID: acceptEdge.ID, WorkflowID: workflowID, TransitionGroupID: acceptEdge.TransitionGroupID, Key: acceptEdge.Key, TargetNodeID: acceptEdge.TargetNodeID, ContextMode: workflow.ContextModeContinueSession, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}, RequiresApproval: true, PromptTemplate: acceptEdge.PromptTemplate, Parameters: acceptEdge.Parameters}); err != nil {
		t.Fatalf("UpdateEdge accept context source: %v", err)
	}
	reworkGroup := workflow.TransitionGroupID("group-previous-target-or-new-approval-rework-" + string(workflowID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: reworkGroup, WorkflowID: workflowID, SourceNodeID: acceptanceNode.ID, TransitionID: "rework", DisplayName: "Rework"}); err != nil {
		t.Fatalf("AddTransitionGroup rework: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-previous-target-or-new-approval-rework-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: reworkGroup, Key: "rework", TargetNodeID: implementationNode.ID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Rework."}); err != nil {
		t.Fatalf("AddEdge rework: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	firstImplementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	firstPending := completeRun(t, ctx, store, CompleteRunRequest{RunID: firstImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "first implementation"}})
	firstApproved, err := store.ApproveTransition(ctx, firstPending.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition first acceptance: %v", err)
	}
	firstAcceptanceRunID := firstApproved.RunIDs[0]
	firstAcceptanceRun, err := store.GetRun(ctx, firstAcceptanceRunID)
	if err != nil {
		t.Fatalf("GetRun first acceptance: %v", err)
	}
	firstClaim, err := store.ClaimRun(ctx, firstAcceptanceRunID, firstAcceptanceRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun first acceptance: %v", err)
	}
	firstAcceptanceSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, firstAcceptanceRunID, firstClaim.Generation, firstAcceptanceSessionID); err != nil {
		t.Fatalf("AttachRunSession first acceptance: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: firstAcceptanceRunID, TransitionID: "rework"})
	secondImplementationRun := latestRunForNode(t, ctx, store, task.ID, implementationNode.ID)
	secondPending := completeRun(t, ctx, store, CompleteRunRequest{RunID: secondImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "second implementation"}})
	competingSessionID := createTestSession(t, ctx, store, binding, cfg)
	insertCompletedRunForNodeAfterTransition(t, ctx, store, task.ID, acceptanceNode.ID, firstAcceptanceRunID, competingSessionID, secondPending.TransitionID)
	secondApproved, err := store.ApproveTransition(ctx, secondPending.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition second acceptance: %v", err)
	}
	input, err := store.GetRunStartContext(ctx, secondApproved.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext second approved acceptance: %v", err)
	}
	if input.ContextMode != workflow.ContextModeContinueSession || input.SourceRunID != firstAcceptanceRunID || input.SourceSessionID != firstAcceptanceSessionID {
		t.Fatalf("second approved context = mode %q source %q/%q, want frozen first acceptance %q/%q; competing session was %q", input.ContextMode, input.SourceRunID, input.SourceSessionID, firstAcceptanceRunID, firstAcceptanceSessionID, competingSessionID)
	}
}

func TestPendingApprovalResolvesPreviousTargetContextSourceOnCompletion(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	addOutputFieldToNode(t, ctx, store, workflowID, acceptanceNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
	addPreviousTargetReworkEdge(t, ctx, store, workflowID, acceptanceNode.ID, implementationNode.ID, true)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	implementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	claimedImplementation, err := store.ClaimRun(ctx, implementationRun.ID, implementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun implementation: %v", err)
	}
	implementationSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, implementationRun.ID, claimedImplementation.Generation, implementationSessionID); err != nil {
		t.Fatalf("AttachRunSession implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	acceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	pending := completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})
	if pending.State != "pending_approval" {
		t.Fatalf("rework completion = %+v, want pending approval", pending)
	}
	competingSessionID := createTestSession(t, ctx, store, binding, cfg)
	insertCompletedRunForNodeAfterTransition(t, ctx, store, task.ID, implementationNode.ID, implementationRun.ID, competingSessionID, pending.TransitionID)
	approved, err := store.ApproveTransition(ctx, pending.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approval result = %+v, want one implementation run", approved)
	}
	input, err := store.GetRunStartContext(ctx, approved.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext approved rework: %v", err)
	}
	if input.SourceRunID != implementationRun.ID || input.SourceSessionID != implementationSessionID || input.SourceNode.Key != "implementation" {
		t.Fatalf("approved rework context source = run %q session %q node %q, want implementation run %q session %q", input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, implementationRun.ID, implementationSessionID)
	}
	if input.SourceSessionID == competingSessionID {
		t.Fatalf("approved rework used competing implementation session %q completed after approval wait started", competingSessionID)
	}
}

func TestPreviousTargetContextSourceStaysInParallelBatch(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	fixedNow := time.UnixMilli(1_000_000)
	store.now = func() time.Time { return fixedNow }
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, _ := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	currentRun := runForNode(t, ctx, store, task.ID, implA.ID)
	mutateRunStartSnapshot(t, ctx, store, currentRun.ID, func(t *testing.T, snapshot *runStartSnapshot) {
		target := nodeSnapshotByID(t, *snapshot, implA.ID)
		target.OutputFields = append(target.OutputFields, workflow.OutputField{Name: "summary", Description: "Summary."})
		snapshot.Node = target
		for index := range snapshot.Nodes {
			if snapshot.Nodes[index].ID == implA.ID {
				snapshot.Nodes[index] = target
			}
		}
		snapshot.TransitionGroups = append(snapshot.TransitionGroups, transitionContractSnapshot{
			ID:           workflow.TransitionGroupID("snapshot-group-redo-a"),
			SourceNodeID: implA.ID,
			TransitionID: "redo",
			DisplayName:  "Redo A",
			Edges: []edgeContractSnapshot{{
				Key:                "redo",
				TargetNode:         target,
				ContextMode:        workflow.ContextModeContinueSession,
				ContextSource:      workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
				InputBindings:      []workflow.InputBinding{{Name: "summary", Source: workflow.BindingSourceTransitionOutput, Field: "summary"}},
				OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}},
			}},
		})
	})
	claimedCurrent, err := store.ClaimRun(ctx, currentRun.ID, currentRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun current branch: %v", err)
	}
	currentSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, currentRun.ID, claimedCurrent.Generation, currentSessionID); err != nil {
		t.Fatalf("AttachRunSession current branch: %v", err)
	}
	competingSessionID := createTestSession(t, ctx, store, binding, cfg)
	currentBatchID, _ := placementParallelIDs(t, ctx, store, currentRun.PlacementID)
	competingBatchID := taskTransitionIDOtherThan(t, ctx, store, task.ID, currentBatchID)
	competingRunID := insertCompletedRunForNodeInBatch(t, ctx, store, task.ID, implA.ID, currentRun.ID, competingSessionID, string(competingBatchID), fixedNow.UnixMilli())

	redo := completeRun(t, ctx, store, CompleteRunRequest{RunID: currentRun.ID, TransitionID: "redo", OutputValues: map[string]string{"summary": "redo current branch"}})
	if len(redo.RunIDs) != 1 {
		t.Fatalf("redo result = %+v, want one branch rerun", redo)
	}
	input, err := store.GetRunStartContext(ctx, redo.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext redo: %v", err)
	}
	if input.SourceRunID != currentRun.ID || input.SourceSessionID != currentSessionID {
		t.Fatalf("redo context source = run %q session %q, want current batch run %q session %q; competing run was %q session %q", input.SourceRunID, input.SourceSessionID, currentRun.ID, currentSessionID, competingRunID, competingSessionID)
	}
}

func TestPendingApprovalResolvesSelectedContextSourceOnApproval(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	openPREdge := edgeByKey(t, def, "open_pr")
	if _, err := store.UpdateEdge(ctx, EdgeRecord{ID: openPREdge.ID, WorkflowID: workflowID, TransitionGroupID: openPREdge.TransitionGroupID, Key: openPREdge.Key, TargetNodeID: openPREdge.TargetNodeID, ContextMode: openPREdge.ContextMode, ContextSource: openPREdge.ContextSource, PromptTemplate: openPREdge.PromptTemplate, Parameters: openPREdge.Parameters, RequiresApproval: true}); err != nil {
		t.Fatalf("UpdateEdge open_pr approval: %v", err)
	}
	def, _, err = store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition updated: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after plan: %v", err)
	}
	var implementationRun RunRecord
	for _, run := range runs {
		if run.NodeID == implementationNode.ID {
			implementationRun = run
		}
	}
	claimedImplementation, err := store.ClaimRun(ctx, implementationRun.ID, implementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun implementation: %v", err)
	}
	implementationSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, implementationRun.ID, claimedImplementation.Generation, implementationSessionID); err != nil {
		t.Fatalf("AttachRunSession implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	runs, err = store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after implementation: %v", err)
	}
	var acceptanceRun RunRecord
	for _, run := range runs {
		if run.NodeID == acceptanceNode.ID {
			acceptanceRun = run
		}
	}
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	if completed.State != "pending_approval" {
		t.Fatalf("acceptance completion = %+v, want pending approval", completed)
	}
	competingSessionID := createTestSession(t, ctx, store, binding, cfg)
	insertCompletedRunForNodeAfterTransition(t, ctx, store, task.ID, implementationNode.ID, implementationRun.ID, competingSessionID, completed.TransitionID)
	approved, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approval result = %+v, want open_pr run", approved)
	}
	input, err := store.GetRunStartContext(ctx, approved.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext open_pr: %v", err)
	}
	if input.SourceRunID != implementationRun.ID || input.SourceSessionID != implementationSessionID || input.SourceNode.Key != "implementation" {
		t.Fatalf("approved open_pr context source = run %q session %q node %q, want implementation run %q session %q", input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, implementationRun.ID, implementationSessionID)
	}
	if input.SourceSessionID == competingSessionID {
		t.Fatalf("approved open_pr used competing implementation session %q completed after approval wait started", competingSessionID)
	}
}

func TestSelectedContextSourceMissingPriorRunFailsClearly(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	mutateRunStartSnapshot(t, ctx, store, started.RunID, func(t *testing.T, snapshot *runStartSnapshot) {
		mutateSnapshotTransition(t, snapshot, "implement", func(group *transitionContractSnapshot) {
			group.Edges[0].ContextMode = workflow.ContextModeContinueSession
			group.Edges[0].ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}
		})
	})
	_, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	var selectedErr ContextSourceNoCompletedRunError
	if !errors.As(err, &selectedErr) || selectedErr.Kind != ContextSourceKindSelected || selectedErr.NodeKey != "implementation" {
		t.Fatalf("CompleteRun selected source error = %v, want missing completed run", err)
	}
}

func TestPreviousTargetContextSourceMissingPriorRunFailsClearly(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	mutateRunStartSnapshot(t, ctx, store, started.RunID, func(t *testing.T, snapshot *runStartSnapshot) {
		mutateSnapshotTransition(t, snapshot, "implement", func(group *transitionContractSnapshot) {
			group.Edges[0].ContextMode = workflow.ContextModeContinueSession
			group.Edges[0].ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}
		})
	})
	_, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	var previousTargetErr ContextSourceNoCompletedRunError
	if !errors.As(err, &previousTargetErr) || previousTargetErr.Kind != ContextSourceKindPreviousTarget || previousTargetErr.NodeKey != "implementation" {
		t.Fatalf("CompleteRun previous target source error = %v, want missing completed run", err)
	}
}
