package workflowstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/workflow"
	"core/shared/config"
)

type selectedContextSourceFixture struct {
	ctx                context.Context
	store              *Store
	binding            metadata.Binding
	cfg                config.App
	workflowID         workflow.WorkflowID
	implementationNode workflow.Node
	acceptanceNode     workflow.Node
	acceptEdgeID       workflow.EdgeID
	openPREdgeID       workflow.EdgeID
	taskID             workflow.TaskID
	startRunID         workflow.RunID
}

func newSelectedContextSourceFixture(t *testing.T, configure func(*selectedContextSourceFixture)) selectedContextSourceFixture {
	t.Helper()
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	fixture := selectedContextSourceFixture{
		ctx:                ctx,
		store:              store,
		binding:            binding,
		cfg:                cfg,
		workflowID:         workflowID,
		implementationNode: nodeByKey(t, def, "implementation"),
		acceptanceNode:     nodeByKey(t, def, "acceptance"),
		acceptEdgeID:       edgeByKey(t, def, "accept").ID,
		openPREdgeID:       edgeByKey(t, def, "open_pr").ID,
	}
	if configure != nil {
		configure(&fixture)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	fixture.taskID = task.ID
	fixture.startRunID = startTask(t, ctx, store, task.ID).RunID
	return fixture
}

func (f selectedContextSourceFixture) updateEdge(t *testing.T, edgeID workflow.EdgeID, update func(*EdgeRecord)) {
	t.Helper()
	saveWorkflowGraphFixture(t, f.ctx, f.store, f.workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		update(workflowGraphSaveEdgeRecord(t, req.Edges, edgeID))
	})
}

func (f selectedContextSourceFixture) addNewSessionRework(t *testing.T) {
	t.Helper()
	groupID := workflow.TransitionGroupID("group-new-session-rework-" + string(f.workflowID))
	saveWorkflowGraphFixture(t, f.ctx, f.store, f.workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{ID: groupID, WorkflowID: f.workflowID, SourceNodeID: workflow.NodeIDOf(f.acceptanceNode), TransitionID: "rework", DisplayName: "Rework"})
		req.Edges = append(req.Edges, EdgeRecord{ID: workflow.EdgeID("edge-new-session-rework-" + string(f.workflowID)), WorkflowID: f.workflowID, TransitionGroupID: groupID, Key: "rework", TargetNodeID: workflow.NodeIDOf(f.implementationNode), ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Rework."})
	})
}

func (f selectedContextSourceFixture) completePlan(t *testing.T) RunRecord {
	t.Helper()
	completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: f.startRunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	return runForNode(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(f.implementationNode))
}

func (f selectedContextSourceFixture) completeImplementation(t *testing.T, run RunRecord, summary string) RunRecord {
	t.Helper()
	completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: run.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": summary}})
	return latestRunForNode(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(f.acceptanceNode))
}

func (f selectedContextSourceFixture) attachSession(t *testing.T, run RunRecord) string {
	t.Helper()
	claimed := claimRunFixture(t, f.ctx, f.store, run.ID, run.Generation)
	return createAndAttachRunSessionFixture(t, f.ctx, f.store, f.binding, f.cfg, run.ID, claimed.Generation)
}

func (f selectedContextSourceFixture) startContext(t *testing.T, runID workflow.RunID) RunStartContext {
	t.Helper()
	input, err := f.store.GetRunStartContext(f.ctx, runID)
	if err != nil {
		t.Fatalf("GetRunStartContext %s: %v", runID, err)
	}
	return input
}

func (f selectedContextSourceFixture) approve(t *testing.T, transitionID workflow.TransitionID) CompleteRunResult {
	t.Helper()
	approved, err := f.store.ApproveTransition(f.ctx, transitionID)
	if err != nil {
		t.Fatalf("ApproveTransition %s: %v", transitionID, err)
	}
	return approved
}

func singleStartedRun(t *testing.T, result CompleteRunResult) workflow.RunID {
	t.Helper()
	if len(result.RunIDs) != 1 {
		t.Fatalf("transition result = %+v, want one run", result)
	}
	return result.RunIDs[0]
}

func TestRunStartContextUsesSelectedPriorNodeSession(t *testing.T) {
	f := newSelectedContextSourceFixture(t, nil)
	implementationRun := f.completePlan(t)
	implementationSessionID := f.attachSession(t, implementationRun)
	acceptanceRun := f.completeImplementation(t, implementationRun, "implemented")
	f.attachSession(t, acceptanceRun)

	completed := completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	input := f.startContext(t, singleStartedRun(t, completed))
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
	f := newSelectedContextSourceFixture(t, func(f *selectedContextSourceFixture) {
		f.addNewSessionRework(t)
	})
	firstImplementationRun := f.completePlan(t)
	firstSessionID := f.attachSession(t, firstImplementationRun)
	firstAcceptanceRun := f.completeImplementation(t, firstImplementationRun, "first implementation")
	completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: firstAcceptanceRun.ID, TransitionID: "rework"})

	secondImplementationRun := latestRunForNode(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(f.implementationNode))
	if secondImplementationRun.ID == firstImplementationRun.ID {
		t.Fatalf("second implementation run = first run %q", secondImplementationRun.ID)
	}
	secondSessionID := f.attachSession(t, secondImplementationRun)
	secondAcceptanceRun := f.completeImplementation(t, secondImplementationRun, "second implementation")
	completed := completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: secondAcceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	input := f.startContext(t, singleStartedRun(t, completed))
	if input.SourceRunID != secondImplementationRun.ID || input.SourceSessionID != secondSessionID {
		t.Fatalf("open_pr source run/session = %q/%q, want latest implementation %q/%q; first session was %q", input.SourceRunID, input.SourceSessionID, secondImplementationRun.ID, secondSessionID, firstSessionID)
	}
}

func TestPreviousTargetContextSourceUsesLatestCompletedTargetRun(t *testing.T) {
	f := newSelectedContextSourceFixture(t, func(f *selectedContextSourceFixture) {
		addOutputFieldToNode(t, f.ctx, f.store, f.workflowID, f.acceptanceNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
		addTargetHistoryReworkEdge(t, f.ctx, f.store, f.workflowID, workflow.NodeIDOf(f.acceptanceNode), workflow.NodeIDOf(f.implementationNode), workflow.ContextSourcePreviousTarget, false)
	})
	firstImplementationRun := f.completePlan(t)
	firstSessionID := f.attachSession(t, firstImplementationRun)
	firstAcceptanceRun := f.completeImplementation(t, firstImplementationRun, "first implementation")
	firstRework := completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: firstAcceptanceRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})
	firstReworkInput := f.startContext(t, singleStartedRun(t, firstRework))
	if firstReworkInput.SourceRunID != firstImplementationRun.ID || firstReworkInput.SourceSessionID != firstSessionID || firstReworkInput.SourceNode.Key != "implementation" {
		t.Fatalf("first rework source = run %q session %q node %q, want first implementation %q/%q", firstReworkInput.SourceRunID, firstReworkInput.SourceSessionID, firstReworkInput.SourceNode.Key, firstImplementationRun.ID, firstSessionID)
	}

	secondImplementationRun := latestRunForNode(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(f.implementationNode))
	secondSessionID := f.attachSession(t, secondImplementationRun)
	secondAcceptanceRun := f.completeImplementation(t, secondImplementationRun, "second implementation")
	secondRework := completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: secondAcceptanceRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "still needs changes"}})
	secondReworkInput := f.startContext(t, singleStartedRun(t, secondRework))
	if secondReworkInput.SourceRunID != secondImplementationRun.ID || secondReworkInput.SourceSessionID != secondSessionID {
		t.Fatalf("second rework source run/session = %q/%q, want latest implementation %q/%q; first session was %q", secondReworkInput.SourceRunID, secondReworkInput.SourceSessionID, secondImplementationRun.ID, secondSessionID, firstSessionID)
	}
}

func TestManualMovePreviousTargetContextSourceUsesLatestCompletedTargetRun(t *testing.T) {
	f := newSelectedContextSourceFixture(t, func(f *selectedContextSourceFixture) {
		addOutputFieldToNode(t, f.ctx, f.store, f.workflowID, f.acceptanceNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
		addTargetHistoryReworkEdge(t, f.ctx, f.store, f.workflowID, workflow.NodeIDOf(f.acceptanceNode), workflow.NodeIDOf(f.implementationNode), workflow.ContextSourcePreviousTarget, false)
	})
	implementationRun := f.completePlan(t)
	implementationSessionID := f.attachSession(t, implementationRun)
	f.completeImplementation(t, implementationRun, "implemented")

	moved, err := f.store.ManualMoveTask(f.ctx, ManualMoveRequest{
		TaskID:       f.taskID,
		TargetNodeID: workflow.NodeIDOf(f.implementationNode),
		OutputValues: map[string]string{"summary": "manual rework"},
		AutoApprove:  true,
		ExecutionTarget: sourceExecutionTargetCandidate(
			f.binding.WorkspaceID,
			f.binding.CanonicalRoot,
		),
	})
	if err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}
	if len(moved.RunIDs) != 1 {
		t.Fatalf("manual move = %+v, want one run", moved)
	}
	input := f.startContext(t, moved.RunIDs[0])
	if input.SourceRunID != implementationRun.ID || input.SourceSessionID != implementationSessionID || input.SourceNode.Key != "implementation" {
		t.Fatalf("manual move context source = run %q session %q node %q, want implementation run %q session %q", input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, implementationRun.ID, implementationSessionID)
	}
}

func TestManualMoveBackwardPreviousTargetContextSourceUsesPriorTargetRun(t *testing.T) {
	var openPRNode workflow.Node
	f := newSelectedContextSourceFixture(t, func(f *selectedContextSourceFixture) {
		def, _, err := f.store.GetDefinition(f.ctx, f.workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		openPRNode = nodeByKey(t, def, "open_pr")
		addOutputFieldToNode(t, f.ctx, f.store, f.workflowID, openPRNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
		addTargetHistoryReworkEdge(t, f.ctx, f.store, f.workflowID, workflow.NodeIDOf(openPRNode), workflow.NodeIDOf(f.implementationNode), workflow.ContextSourcePreviousTarget, false)
	})
	implementationRun := f.completePlan(t)
	f.attachSession(t, implementationRun)
	acceptanceRun := f.completeImplementation(t, implementationRun, "implemented")
	completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	openPRRun := runForNode(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(openPRNode))
	openPRSessionID := f.attachSession(t, openPRRun)
	completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: openPRRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})

	moved, err := f.store.ManualMoveTask(f.ctx, ManualMoveRequest{
		TaskID:       f.taskID,
		TargetNodeID: workflow.NodeIDOf(openPRNode),
		AutoApprove:  true,
		ExecutionTarget: sourceExecutionTargetCandidate(
			f.binding.WorkspaceID,
			f.binding.CanonicalRoot,
		),
	})
	if err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}
	if len(moved.RunIDs) != 1 {
		t.Fatalf("manual move = %+v, want one run", moved)
	}
	input := f.startContext(t, moved.RunIDs[0])
	if input.SourceRunID != openPRRun.ID || input.SourceSessionID != openPRSessionID || input.SourceNode.Key != "open_pr" {
		t.Fatalf("backward manual move context source = run %q session %q node %q, want open PR run %q session %q", input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, openPRRun.ID, openPRSessionID)
	}
}

func TestPreviousTargetOrNewContextSourceFallsBackThenContinuesTargetRun(t *testing.T) {
	f := newSelectedContextSourceFixture(t, func(f *selectedContextSourceFixture) {
		f.updateEdge(t, f.acceptEdgeID, func(edge *EdgeRecord) {
			edge.ContextMode = workflow.ContextModeContinueSession
			edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
		})
		f.addNewSessionRework(t)
	})
	firstImplementationRun := f.completePlan(t)
	firstAcceptanceRun := f.completeImplementation(t, firstImplementationRun, "first implementation")
	firstAcceptanceInput := f.startContext(t, firstAcceptanceRun.ID)
	if firstAcceptanceInput.ContextMode != workflow.ContextModeNewSession || firstAcceptanceInput.SourceRunID != "" || firstAcceptanceInput.SourceSessionID != "" {
		t.Fatalf("first acceptance context = mode %q source %q/%q, want new session without source", firstAcceptanceInput.ContextMode, firstAcceptanceInput.SourceRunID, firstAcceptanceInput.SourceSessionID)
	}

	firstAcceptanceSessionID := f.attachSession(t, firstAcceptanceRun)
	completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: firstAcceptanceRun.ID, TransitionID: "rework"})
	secondImplementationRun := latestRunForNode(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(f.implementationNode))
	secondAcceptanceRun := f.completeImplementation(t, secondImplementationRun, "second implementation")
	if secondAcceptanceRun.ID == firstAcceptanceRun.ID {
		t.Fatalf("second acceptance run = first run %q", secondAcceptanceRun.ID)
	}
	secondAcceptanceInput := f.startContext(t, secondAcceptanceRun.ID)
	if secondAcceptanceInput.ContextMode != workflow.ContextModeContinueSession || secondAcceptanceInput.SourceRunID != firstAcceptanceRun.ID || secondAcceptanceInput.SourceSessionID != firstAcceptanceSessionID {
		t.Fatalf("second acceptance context = mode %q source %q/%q, want continue first acceptance %q/%q", secondAcceptanceInput.ContextMode, secondAcceptanceInput.SourceRunID, secondAcceptanceInput.SourceSessionID, firstAcceptanceRun.ID, firstAcceptanceSessionID)
	}
}

func TestManualMovePreviousTargetOrNewContextSourceFallsBackToNewSession(t *testing.T) {
	f := newSelectedContextSourceFixture(t, func(f *selectedContextSourceFixture) {
		def, _, err := f.store.GetDefinition(f.ctx, f.workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		f.updateEdge(t, edgeByKey(t, def, "implement").ID, func(edge *EdgeRecord) {
			edge.ContextMode = workflow.ContextModeContinueSession
			edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
		})
	})

	moved, err := f.store.ManualMoveTask(f.ctx, ManualMoveRequest{
		TaskID:       f.taskID,
		TargetNodeID: workflow.NodeIDOf(f.implementationNode),
		OutputValues: map[string]string{"summary": "manual implementation"},
		AutoApprove:  true,
		ExecutionTarget: sourceExecutionTargetCandidate(
			f.binding.WorkspaceID,
			f.binding.CanonicalRoot,
		),
	})
	if err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}
	if len(moved.RunIDs) != 1 {
		t.Fatalf("manual move = %+v, want one run", moved)
	}
	input := f.startContext(t, moved.RunIDs[0])
	if input.ContextMode != workflow.ContextModeNewSession || input.SourceRunID != "" || input.SourceSessionID != "" {
		t.Fatalf("manual move context = mode %q source %q/%q, want new session without source", input.ContextMode, input.SourceRunID, input.SourceSessionID)
	}
}

func TestPendingApprovalFreezesPreviousTargetOrNewResolution(t *testing.T) {
	for _, tc := range []struct {
		name           string
		createPriorRun bool
	}{
		{name: "fallback to new"},
		{name: "prior target run", createPriorRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectedContextSourceFixture(t, func(f *selectedContextSourceFixture) {
				f.updateEdge(t, f.acceptEdgeID, func(edge *EdgeRecord) {
					edge.ContextMode = workflow.ContextModeContinueSession
					edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
					edge.RequiresApproval = true
				})
				f.addNewSessionRework(t)
			})
			implementationRun := f.completePlan(t)
			var priorAcceptanceRun RunRecord
			var priorAcceptanceSessionID string
			if tc.createPriorRun {
				firstPending := completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "first implementation"}})
				firstApproved := f.approve(t, firstPending.TransitionID)
				priorAcceptanceRun = runForNode(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(f.acceptanceNode))
				if priorAcceptanceRun.ID != singleStartedRun(t, firstApproved) {
					t.Fatalf("first approved run = %q, want acceptance run %q", firstApproved.RunIDs[0], priorAcceptanceRun.ID)
				}
				priorAcceptanceSessionID = f.attachSession(t, priorAcceptanceRun)
				completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: priorAcceptanceRun.ID, TransitionID: "rework"})
				implementationRun = latestRunForNode(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(f.implementationNode))
			}

			pending := completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "latest implementation"}})
			if pending.State != "pending_approval" {
				t.Fatalf("accept completion = %+v, want pending approval", pending)
			}
			competingSessionID := createTestSession(t, f.ctx, f.store, f.binding, f.cfg)
			insertCompletedRunForNodeAfterTransition(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(f.acceptanceNode), implementationRun.ID, competingSessionID, pending.TransitionID)
			approved := f.approve(t, pending.TransitionID)
			input := f.startContext(t, singleStartedRun(t, approved))
			if tc.createPriorRun {
				if input.ContextMode != workflow.ContextModeContinueSession || input.SourceRunID != priorAcceptanceRun.ID || input.SourceSessionID != priorAcceptanceSessionID {
					t.Fatalf("approved context = mode %q source %q/%q, want frozen prior acceptance %q/%q; competing session was %q", input.ContextMode, input.SourceRunID, input.SourceSessionID, priorAcceptanceRun.ID, priorAcceptanceSessionID, competingSessionID)
				}
			} else if input.ContextMode != workflow.ContextModeNewSession || input.SourceRunID != "" || input.SourceSessionID != "" {
				t.Fatalf("approved fallback context = mode %q source %q/%q, want frozen new session without source; competing session was %q", input.ContextMode, input.SourceRunID, input.SourceSessionID, competingSessionID)
			}
		})
	}
}

func TestManualMovePendingApprovalFreezesPreviousTargetResolution(t *testing.T) {
	f := newSelectedContextSourceFixture(t, func(f *selectedContextSourceFixture) {
		addOutputFieldToNode(t, f.ctx, f.store, f.workflowID, f.acceptanceNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
		addTargetHistoryReworkEdge(t, f.ctx, f.store, f.workflowID, workflow.NodeIDOf(f.acceptanceNode), workflow.NodeIDOf(f.implementationNode), workflow.ContextSourcePreviousTarget, true)
	})
	implementationRun := f.completePlan(t)
	implementationSessionID := f.attachSession(t, implementationRun)
	f.completeImplementation(t, implementationRun, "implemented")

	pending, err := f.store.ManualMoveTask(f.ctx, ManualMoveRequest{
		TaskID:       f.taskID,
		TargetNodeID: workflow.NodeIDOf(f.implementationNode),
		OutputValues: map[string]string{"summary": "manual rework"},
		AutoApprove:  true,
	})
	if err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}
	if pending.State != "pending_approval" {
		t.Fatalf("manual move = %+v, want pending approval", pending)
	}

	var transitionCreatedAt int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT created_at_unix_ms FROM task_transitions WHERE id = ?`, string(pending.TransitionID)).Scan(&transitionCreatedAt); err != nil {
		t.Fatalf("query transition created_at: %v", err)
	}
	competingSessionID := createTestSession(t, f.ctx, f.store, f.binding, f.cfg)
	competingRunID := insertCompletedRunForNodeInBatch(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(f.implementationNode), implementationRun.ID, competingSessionID, "", transitionCreatedAt)

	approved := f.approve(t, pending.TransitionID)
	input := f.startContext(t, singleStartedRun(t, approved))
	if input.SourceRunID != implementationRun.ID || input.SourceSessionID != implementationSessionID {
		t.Fatalf("approved manual move source = run %q session %q, want frozen implementation %q/%q; competing run was %q/%q", input.SourceRunID, input.SourceSessionID, implementationRun.ID, implementationSessionID, competingRunID, competingSessionID)
	}
}

func TestPendingApprovalFreezesResolvedContextSource(t *testing.T) {
	for _, tc := range []struct {
		name           string
		transitionID   string
		outputValues   map[string]string
		configureGraph func(*selectedContextSourceFixture)
	}{
		{
			name:         "previous target",
			transitionID: "rework",
			outputValues: map[string]string{"summary": "needs changes"},
			configureGraph: func(f *selectedContextSourceFixture) {
				addOutputFieldToNode(t, f.ctx, f.store, f.workflowID, f.acceptanceNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
				addTargetHistoryReworkEdge(t, f.ctx, f.store, f.workflowID, workflow.NodeIDOf(f.acceptanceNode), workflow.NodeIDOf(f.implementationNode), workflow.ContextSourcePreviousTarget, true)
			},
		},
		{
			name:         "selected node",
			transitionID: "open_pr",
			outputValues: map[string]string{"acceptance_decision": "approved"},
			configureGraph: func(f *selectedContextSourceFixture) {
				f.updateEdge(t, f.openPREdgeID, func(edge *EdgeRecord) {
					edge.RequiresApproval = true
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectedContextSourceFixture(t, tc.configureGraph)
			implementationRun := f.completePlan(t)
			implementationSessionID := f.attachSession(t, implementationRun)
			acceptanceRun := f.completeImplementation(t, implementationRun, "implemented")
			pending := completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: tc.transitionID, OutputValues: tc.outputValues})
			if pending.State != "pending_approval" {
				t.Fatalf("%s completion = %+v, want pending approval", tc.transitionID, pending)
			}

			competingSessionID := createTestSession(t, f.ctx, f.store, f.binding, f.cfg)
			insertCompletedRunForNodeAfterTransition(t, f.ctx, f.store, f.taskID, workflow.NodeIDOf(f.implementationNode), implementationRun.ID, competingSessionID, pending.TransitionID)
			approved := f.approve(t, pending.TransitionID)
			input := f.startContext(t, singleStartedRun(t, approved))
			if input.SourceRunID != implementationRun.ID || input.SourceSessionID != implementationSessionID || input.SourceNode.Key != "implementation" {
				t.Fatalf("approved %s context source = run %q session %q node %q, want implementation run %q session %q", tc.transitionID, input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, implementationRun.ID, implementationSessionID)
			}
			if input.SourceSessionID == competingSessionID {
				t.Fatalf("approved %s used competing implementation session %q completed after approval wait started", tc.transitionID, competingSessionID)
			}
		})
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
	currentRun := runForNode(t, ctx, store, task.ID, workflow.NodeIDOf(implA))
	mutateRunStartSnapshot(t, ctx, store, currentRun.ID, func(t *testing.T, snapshot *runStartSnapshot) {
		target := nodeSnapshotByID(t, *snapshot, workflow.NodeIDOf(implA))
		target.OutputFields = append(target.OutputFields, workflow.OutputField{Name: "summary", Description: "Summary."})
		snapshot.Node = target
		for index := range snapshot.Nodes {
			if snapshot.Nodes[index].ID == workflow.NodeIDOf(implA) {
				snapshot.Nodes[index] = target
			}
		}
		snapshot.TransitionGroups = append(snapshot.TransitionGroups, transitionContractSnapshot{
			ID:           workflow.TransitionGroupID("snapshot-group-redo-a"),
			SourceNodeID: workflow.NodeIDOf(implA),
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
	competingRunID := insertCompletedRunForNodeInBatch(t, ctx, store, task.ID, workflow.NodeIDOf(implA), currentRun.ID, competingSessionID, string(competingBatchID), fixedNow.UnixMilli())

	redo := completeRun(t, ctx, store, CompleteRunRequest{RunID: currentRun.ID, TransitionID: "redo", OutputValues: map[string]string{"summary": "redo current branch"}})
	input, err := store.GetRunStartContext(ctx, singleStartedRun(t, redo))
	if err != nil {
		t.Fatalf("GetRunStartContext redo: %v", err)
	}
	if input.SourceRunID != currentRun.ID || input.SourceSessionID != currentSessionID {
		t.Fatalf("redo context source = run %q session %q, want current batch run %q session %q; competing run was %q session %q", input.SourceRunID, input.SourceSessionID, currentRun.ID, currentSessionID, competingRunID, competingSessionID)
	}
}

func assertMissingPriorRunError(t *testing.T, source workflow.ContextSource, wantKind ContextSourceKind) {
	t.Helper()
	f := newSelectedContextSourceFixture(t, nil)
	mutateRunStartSnapshot(t, f.ctx, f.store, f.startRunID, func(t *testing.T, snapshot *runStartSnapshot) {
		mutateSnapshotTransition(t, snapshot, "implement", func(group *transitionContractSnapshot) {
			group.Edges[0].ContextMode = workflow.ContextModeContinueSession
			group.Edges[0].ContextSource = source
		})
	})
	_, err := f.store.CompleteRun(f.ctx, CompleteRunRequest{RunID: f.startRunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	var sourceErr ContextSourceNoCompletedRunError
	if !errors.As(err, &sourceErr) || sourceErr.Kind != wantKind || sourceErr.NodeKey != "implementation" {
		t.Fatalf("CompleteRun context source error = %v, want %s missing implementation run", err, wantKind)
	}
}

func TestSelectedContextSourceMissingPriorRunFailsClearly(t *testing.T) {
	assertMissingPriorRunError(t, workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}, ContextSourceKindSelected)
}

func TestPreviousTargetContextSourceMissingPriorRunFailsClearly(t *testing.T) {
	assertMissingPriorRunError(t, workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}, ContextSourceKindPreviousTarget)
}
