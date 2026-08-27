package workflowstore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/workflowcontract"
)

func TestPrepareManualMoveRejectsOversizedCommentary(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	target := startTask(t, ctx, store, task.ID).Mutation.Created[0].Reference.NodeID

	_, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: target,
		Commentary:   strings.Repeat("x", workflow.MaxCommentaryBytes+1),
	})
	var validation CompletionValidationError
	if !errors.As(err, &validation) || !validation.HasCode(CompletionCodeCommentaryTooLarge) {
		t.Fatalf("PrepareManualMove error = %T %v, want oversized commentary validation", err, err)
	}
}

func TestApplyManualMoveRejectsAgentPlacementWithoutAssignmentPreparation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if _, err := store.ApplyManualMove(ctx, prepared, noneManualMoveExecutionTargetCandidate(binding)); err == nil {
		t.Fatal("ApplyManualMove allowed Agent placement without assignment preparation")
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].Reference.NodeID == workflow.NodeIDOf(target) {
		t.Fatalf("Current Nodes after rejected Agent move = %+v, want unchanged origin", currentNodes)
	}
}

func TestManualMoveAssignmentPreparationIncludesCommentary(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Commentary:   "  operator handoff  ",
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	var observed []CurrentNodeStartContext
	if _, err := applyManualMoveForStoreTestWithPreparation(
		t,
		ctx,
		store,
		prepared,
		noneManualMoveExecutionTargetCandidate(binding),
		func(inputs []CurrentNodeStartContext) {
			observed = append([]CurrentNodeStartContext(nil), inputs...)
		},
	); err != nil {
		t.Fatalf("ApplyManualMoveWithTargetAssignments: %v", err)
	}
	if len(observed) != 1 ||
		observed[0].CurrentNode.CurrentInputValues[workflow.RuntimePromptParameterCommentary] != "operator handoff" {
		t.Fatalf("assignment preparation contexts = %+v, want trimmed operator commentary", observed)
	}
}

func TestManualMoveRejectsWorkflowVersionChangeAfterAssignmentPreparation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	origin := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	assignmentSessionID := createManualMoveAssignmentSession(t, ctx, store, binding)
	aborted := false
	_, err = store.ApplyManualMoveWithTargetAssignments(
		ctx,
		prepared,
		noneManualMoveExecutionTargetCandidate(binding),
		func(_ context.Context, inputs []CurrentNodeStartContext) (ManualMoveTargetAssignmentPreparation, error) {
			version, versionErr := store.incrementWorkflowVersion(ctx, store.queries, workflowID)
			if versionErr != nil {
				return ManualMoveTargetAssignmentPreparation{}, versionErr
			}
			if version != record.Version+1 {
				return ManualMoveTargetAssignmentPreparation{}, errors.New("workflow version did not advance")
			}
			preparation, assignmentErr := manualMoveTargetAssignmentsForSession(inputs, assignmentSessionID)
			if assignmentErr != nil {
				return ManualMoveTargetAssignmentPreparation{}, assignmentErr
			}
			preparation.Abort = func(err error) error {
				aborted = true
				return err
			}
			return preparation, nil
		},
	)
	if err == nil {
		t.Fatal("ApplyManualMoveWithTargetAssignments accepted changed Workflow Version")
	}
	if !aborted {
		t.Fatal("changed Workflow Version did not abort prepared assignments")
	}
	currentNodes, listErr := store.ListCurrentNodes(ctx, task.ID)
	if listErr != nil {
		t.Fatalf("ListCurrentNodes: %v", listErr)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(origin.Reference) {
		t.Fatalf("Current Nodes after Workflow edit = %+v, want origin %v", currentNodes, origin.Reference)
	}
}

func TestManualMoveRetainedAssignmentBlocksWorkflowSaveUntilMoveCommits(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	definition, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	assignmentSessionID := createManualMoveAssignmentSession(t, ctx, store, binding)
	assignmentPrepared := make(chan struct{})
	releaseAssignment := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseAssignment) })
	})
	moveDone := make(chan error, 1)
	go func() {
		_, moveErr := store.ApplyManualMoveWithTargetAssignments(
			ctx,
			prepared,
			noneManualMoveExecutionTargetCandidate(binding),
			func(_ context.Context, inputs []CurrentNodeStartContext) (ManualMoveTargetAssignmentPreparation, error) {
				close(assignmentPrepared)
				<-releaseAssignment
				return manualMoveTargetAssignmentsForSession(inputs, assignmentSessionID)
			},
		)
		moveDone <- moveErr
	}()
	select {
	case <-assignmentPrepared:
	case <-time.After(time.Second):
		t.Fatal("Manual Move did not reach assignment preparation")
	}
	updated := definition
	updated.Edges = append([]workflow.Edge(nil), definition.Edges...)
	for index := range updated.Edges {
		if updated.Edges[index].TargetNodeID == workflow.NodeIDOf(target) {
			updated.Edges[index].PromptTemplate = "Updated assignment instructions."
			break
		}
	}
	saveDone := make(chan WorkflowGraphSaveResult, 1)
	saveErr := make(chan error, 1)
	go func() {
		saved, err := store.SaveWorkflowGraph(ctx, NewWorkflowGraphSaveRequest(updated, record.Version))
		if err != nil {
			saveErr <- err
			return
		}
		saveDone <- saved
	}()
	select {
	case err := <-saveErr:
		t.Fatalf("SaveWorkflowGraph returned while Manual Move assignment was pending: %v", err)
	case saved := <-saveDone:
		t.Fatalf("SaveWorkflowGraph returned while Manual Move assignment was pending: %+v", saved)
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(releaseAssignment) })
	if err := <-moveDone; err != nil {
		t.Fatalf("ApplyManualMoveWithTargetAssignments: %v", err)
	}
	select {
	case err := <-saveErr:
		t.Fatalf("SaveWorkflowGraph after Manual Move: %v", err)
	case saved := <-saveDone:
		if !saved.Saved || saved.Version != record.Version+1 {
			t.Fatalf("SaveWorkflowGraph after Manual Move = %+v, want version %d", saved, record.Version+1)
		}
	case <-time.After(time.Second):
		t.Fatal("SaveWorkflowGraph remained blocked after Manual Move")
	}
}

func TestManualMoveAssignmentWaitDoesNotBlockAnotherTaskInSameWorkflow(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	firstTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	secondTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, firstTask.ID)
	startTask(t, ctx, store, secondTask.ID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	prepare := func(taskID workflow.TaskID) ManualMovePreparation {
		prepared, prepareErr := store.PrepareManualMove(ctx, ManualMoveRequest{
			TaskID:       taskID,
			TargetNodeID: workflow.NodeIDOf(target),
			Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
		})
		if prepareErr != nil {
			t.Fatalf("PrepareManualMove %q: %v", taskID, prepareErr)
		}
		return prepared
	}
	firstPrepared := prepare(firstTask.ID)
	secondPrepared := prepare(secondTask.ID)
	firstAssignmentSessionID := createManualMoveAssignmentSession(t, ctx, store, binding)
	secondAssignmentSessionID := createManualMoveAssignmentSession(t, ctx, store, binding)
	firstWaiting := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseFirst) })
	})
	firstDone := make(chan error, 1)
	go func() {
		_, moveErr := store.ApplyManualMoveWithTargetAssignments(
			ctx,
			firstPrepared,
			noneManualMoveExecutionTargetCandidate(binding),
			func(_ context.Context, inputs []CurrentNodeStartContext) (ManualMoveTargetAssignmentPreparation, error) {
				close(firstWaiting)
				<-releaseFirst
				return manualMoveTargetAssignmentsForSession(inputs, firstAssignmentSessionID)
			},
		)
		firstDone <- moveErr
	}()
	select {
	case <-firstWaiting:
	case <-time.After(time.Second):
		t.Fatal("first Manual Move did not reach assignment wait")
	}
	secondDone := make(chan error, 1)
	go func() {
		_, moveErr := store.ApplyManualMoveWithTargetAssignments(
			ctx,
			secondPrepared,
			noneManualMoveExecutionTargetCandidate(binding),
			func(_ context.Context, inputs []CurrentNodeStartContext) (ManualMoveTargetAssignmentPreparation, error) {
				return manualMoveTargetAssignmentsForSession(inputs, secondAssignmentSessionID)
			},
		)
		secondDone <- moveErr
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second Manual Move: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Task Manual Move blocked on first Task assignment wait")
	}
	releaseOnce.Do(func() { close(releaseFirst) })
	if err := <-firstDone; err != nil {
		t.Fatalf("first Manual Move: %v", err)
	}
}

func createManualMoveAssignmentSession(
	t *testing.T,
	ctx context.Context,
	store *Store,
	binding metadata.Binding,
) runtimeids.SessionID {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, config.App{
		PersistenceRoot: store.metadata.PersistenceRoot(),
		WorkspaceRoot:   binding.CanonicalRoot,
	}))
	if err != nil {
		t.Fatalf("parse test Session ID: %v", err)
	}
	return sessionID
}

func TestApplyManualMoveRejectsScriptDestinationWithAgentFanoutWithoutAssignmentPreparation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMixedExecutableFanoutWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	script := nodeByKey(t, definition, "script")
	transition := workflow.TransitionID("split")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(script),
		TransitionKey: &transition,
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if _, err := store.ApplyManualMove(ctx, prepared, noneManualMoveExecutionTargetCandidate(binding)); err == nil {
		t.Fatal("ApplyManualMove allowed mixed executable fan-out without assignment preparation")
	}
	var observed []CurrentNodeStartContext
	moved, err := applyManualMoveForStoreTestWithPreparation(
		t,
		ctx,
		store,
		prepared,
		noneManualMoveExecutionTargetCandidate(binding),
		func(inputs []CurrentNodeStartContext) {
			observed = append([]CurrentNodeStartContext(nil), inputs...)
		},
	)
	if err != nil {
		t.Fatalf("ApplyManualMoveWithTargetAssignments: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied || len(moved.Mutation.Created) != 2 {
		t.Fatalf("mixed fan-out Manual Move = %+v, want two applied targets", moved)
	}
	var agentContexts int
	var scriptContexts int
	for _, input := range observed {
		switch input.Node.Kind {
		case workflow.NodeKindAgent:
			agentContexts++
		case workflow.NodeKindScript:
			scriptContexts++
		}
	}
	if agentContexts != 1 || scriptContexts != 1 {
		t.Fatalf("mixed fan-out assignment contexts = Agent %d Script %d, want one each", agentContexts, scriptContexts)
	}
}

func TestManualMoveForwardExecutableAgentReplacesSerialCurrentNode(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	edge := edgeByKey(t, definition, "next")

	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Commentary:   "  manual note  ",
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if !prepared.RequiresExecutionTarget() {
		t.Fatal("forward executable move did not require execution-target selection")
	}
	moved, err := applyManualMoveForStoreTest(t, ctx, store, prepared, &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflowcontract.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	})
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied {
		t.Fatalf("manual move outcome = %q, want applied", moved.Outcome)
	}
	if len(moved.Mutation.Removed) != 1 || !moved.Mutation.Removed[0].Equal(source.Reference) {
		t.Fatalf("manual move removed = %+v, want source current node", moved.Mutation.Removed)
	}
	if len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) ||
		moved.Mutation.Created[0].EnteredByEdgeID == nil ||
		*moved.Mutation.Created[0].EnteredByEdgeID != edge.ID ||
		moved.Mutation.Created[0].Scheduling == nil ||
		moved.Mutation.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady ||
		moved.Mutation.Created[0].CurrentInputValues["prior_summary"] != "manual plan" ||
		moved.Mutation.Created[0].CurrentInputValues[workflow.RuntimePromptParameterCommentary] != "manual note" {
		t.Fatalf("manual move created = %+v, want ready materialized target", moved.Mutation.Created)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(moved.Mutation.Created[0].Reference) {
		t.Fatalf("current nodes after manual move = %+v, want target only", currentNodes)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil || targetContext.Task.ExecutionTarget.Mode != workflowcontract.ExecutionTargetModeNone {
		t.Fatalf("execution target after manual move = %+v, want locked none target", targetContext.Task.ExecutionTarget)
	}
}

func TestManualMoveRepairsCurrentNodeWhoseEnteringEdgeWasRetargetedToRequestedTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	gateNodeID := testNodeID("node-manual-move-repair-gate-" + workflowID.String())
	gateGroupID := testTransitionGroupID("group-manual-move-repair-gate-" + workflowID.String())
	continueGroupID := testTransitionGroupID("group-manual-move-repair-continue-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(definition workflow.Definition, request *WorkflowGraphSaveRequest) {
		plan := nodeByKey(t, definition, "plan")
		implementation := nodeByKey(t, definition, "implement")
		request.Nodes = append(request.Nodes, NodeRecord{
			ID:           gateNodeID,
			WorkflowID:   workflowID,
			Key:          "repair_gate",
			Kind:         workflow.NodeKindAgent,
			DisplayName:  "Repair Gate",
			SubagentRole: "coder",
		})
		request.TransitionGroups = append(request.TransitionGroups,
			TransitionGroupRecord{
				ID:           gateGroupID,
				WorkflowID:   workflowID,
				SourceNodeID: workflow.NodeIDOf(plan),
				TransitionID: "gate",
				DisplayName:  "Gate",
			},
			TransitionGroupRecord{
				ID:           continueGroupID,
				WorkflowID:   workflowID,
				SourceNodeID: gateNodeID,
				TransitionID: "continue",
				DisplayName:  "Continue",
			},
		)
		request.Edges = append(request.Edges,
			EdgeRecord{
				ID:                testEdgeID("edge-manual-move-repair-gate-" + workflowID.String()),
				WorkflowID:        workflowID,
				TransitionGroupID: gateGroupID,
				Key:               "gate",
				TargetNodeID:      gateNodeID,
				ContextMode:       workflow.ContextModeNewSession,
				PromptTemplate:    "Check the plan.",
				AssigneeSelection: workflow.AssigneeSelectionConfigured,
				ThinkingSelection: workflow.ThinkingSelectionConfigured,
			},
			EdgeRecord{
				ID:                testEdgeID("edge-manual-move-repair-continue-" + workflowID.String()),
				WorkflowID:        workflowID,
				TransitionGroupID: continueGroupID,
				Key:               "continue",
				TargetNodeID:      workflow.NodeIDOf(implementation),
				ContextMode:       workflow.ContextModeNewSession,
				PromptTemplate:    "Implement the checked plan.",
				AssigneeSelection: workflow.AssigneeSelectionConfigured,
				ThinkingSelection: workflow.ThinkingSelectionConfigured,
			},
		)
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	implementationResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "approved plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	implementation := implementationResult.Mutation.Created[0]
	if implementation.EnteredByEdgeID == nil {
		t.Fatal("implementation Current Node has no entering Edge")
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	gate := nodeByKey(t, definition, "repair_gate")
	if _, err := store.db.ExecContext(ctx, `
UPDATE workflow_edges
SET target_node_id = ?
WHERE id = ?`,
		string(workflow.NodeIDOf(gate)),
		string(*implementation.EnteredByEdgeID),
	); err != nil {
		t.Fatalf("retarget entering Edge: %v", err)
	}

	if _, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: plan.Reference.NodeID,
	}); err == nil {
		t.Fatal("manual move to unrelated target accepted retargeted entering Edge")
	}

	transition := workflow.TransitionID("next")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(gate),
		TransitionKey: &transition,
		Values: map[workflow.ModelKey]map[string]string{
			"plan": {"prior_summary": "approved plan"},
		},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove to retargeted entering Edge target: %v", err)
	}
	moved, err := applyManualMoveForStoreTest(
		t,
		ctx,
		store,
		prepared,
		noneManualMoveExecutionTargetCandidate(binding),
	)
	if err != nil {
		t.Fatalf("ApplyManualMove to retargeted entering Edge target: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied ||
		len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(gate) ||
		moved.Mutation.Created[0].EnteredByEdgeID == nil ||
		*moved.Mutation.Created[0].EnteredByEdgeID != *implementation.EnteredByEdgeID {
		t.Fatalf("manual move repair = %+v, want exact retargeted entering Edge destination", moved)
	}
}

func TestPrepareManualMoveValidatesExecutableCompletionShapeWithoutMutation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	_ = started
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	before, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes before rejected move: %v", err)
	}
	_, err = store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values: map[workflow.ModelKey]map[string]string{
			"plan": {"unknown": "value"},
		},
	})
	if !errors.Is(err, ErrManualMoveValuesInvalid) {
		t.Fatalf("PrepareManualMove error = %T %v, want ErrManualMoveValuesInvalid", err, err)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != len(before) {
		t.Fatalf("current nodes after rejected move = %+v, before = %+v", currentNodes, before)
	}
	for index := range before {
		if !currentNodes[index].Reference.Equal(before[index].Reference) {
			t.Fatalf("current nodes after rejected move = %+v, before = %+v", currentNodes, before)
		}
	}
}

func TestManualMoveForwardExecutableReplacesApprovalWithoutStartingTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "automatic proposal"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("CompleteCurrentNode returned no pending Approval")
	}
	supersededApprovalID := completed.PendingApproval.ID
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")

	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Commentary:   "  Manual proposal is ready.  ",
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual proposal"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := applyManualMoveForStoreTest(t, ctx, store, prepared, &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflowcontract.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	})
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied {
		t.Fatalf("manual move outcome = %q, want applied", moved.Outcome)
	}
	if len(moved.TaskAttentionResolution.Approvals) != 1 ||
		moved.TaskAttentionResolution.Approvals[0].ApprovalID != supersededApprovalID {
		t.Fatalf("manual move attention resolution = %+v, want superseded Approval", moved.TaskAttentionResolution)
	}
	if len(moved.Mutation.Removed) != 1 || !moved.Mutation.Removed[0].Equal(source.Reference) ||
		len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) ||
		moved.Mutation.Created[0].CurrentInputValues["prior_summary"] != "manual proposal" ||
		moved.Mutation.Created[0].CurrentInputValues[workflow.RuntimePromptParameterCommentary] != "Manual proposal is ready." {
		t.Fatalf("manual move mutation = %+v, want immediate approved replacement", moved.Mutation)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes before Approval: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(moved.Mutation.Created[0].Reference) {
		t.Fatalf("current nodes after manual Approval = %+v, want target only", currentNodes)
	}
	approvals, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("pending Approvals = %+v, want none after manual Approval", approvals)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil || targetContext.Task.ExecutionTarget.Mode != workflowcontract.ExecutionTargetModeNone {
		t.Fatalf("execution target after manual move = %+v, want locked none target", targetContext.Task.ExecutionTarget)
	}

}

func TestManualMoveForwardExecutableScriptValidatesAndMaterializesTarget(t *testing.T) {
	fixture := newScriptExecutionFixture(t, "scripts/complete", []byte("#!/bin/sh\nprintf '{}'\n"))
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, definition, workflow.NodeKindStart)
	source, err := workflow.NewCurrentNodeReference(fixture.task.ID, workflow.NodeIDOf(start), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}

	prepared, err := fixture.store.PrepareManualMove(fixture.ctx, ManualMoveRequest{
		TaskID:       fixture.task.ID,
		TargetNodeID: fixture.scriptID,
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if !prepared.RequiresExecutionTarget() {
		t.Fatal("script move did not require an execution target")
	}
	moved, err := applyManualMoveForStoreTest(t, fixture.ctx, fixture.store, prepared, nil)
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied ||
		len(moved.Mutation.Removed) != 1 || !moved.Mutation.Removed[0].Equal(source) ||
		len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != fixture.scriptID ||
		moved.Mutation.Created[0].Scheduling == nil ||
		moved.Mutation.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("script move mutation = %+v, want ready script target", moved)
	}
}

func TestManualMoveExecutableRejectsMissingBackwardAndParallelPaths(t *testing.T) {
	t.Run("missing direct edge", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		task := createDefaultTask(t, ctx, store, binding.ProjectID)
		definition, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		target := nodeByKey(t, definition, "implement")

		preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
		if err != nil || preview.Outcome != ManualMovePreviewOutcomeTransition {
			t.Fatalf("missing-edge manual move preview = %+v, error = %v, want transition choice", preview, err)
		}
	})

	t.Run("backward executable target", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		task := createDefaultTask(t, ctx, store, binding.ProjectID)
		source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
		if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: "next",
			OutputValues: map[string]string{"prior_summary": "plan"},
		}); err != nil {
			t.Fatalf("CompleteCurrentNode: %v", err)
		}
		definition, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		target := nodeByKey(t, definition, "plan")

		preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
		if err != nil || preview.Outcome != ManualMovePreviewOutcomeTransition {
			t.Fatalf("backward manual move preview = %+v, error = %v, want transition choice", preview, err)
		}
	})

	t.Run("parallel current nodes", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createFanoutJoinWorkflow(t, ctx, store)
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		task := createDefaultTask(t, ctx, store, binding.ProjectID)
		source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
		if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: "split",
			OutputValues: map[string]string{"summary": "plan"},
		}); err != nil {
			t.Fatalf("CompleteCurrentNode split: %v", err)
		}
		definition, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		target := nodeByKey(t, definition, "impl_a")

		preview, err := store.PreviewManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(target)})
		if err != nil || preview.Outcome != ManualMovePreviewOutcomeNoOp {
			t.Fatalf("parallel manual move preview = %+v, error = %v, want same-current no-op", preview, err)
		}
	})
}

func TestManualMoveToNonExecutableSupersedesPendingApproval(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "plan"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	approvals, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals before move: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("pending approvals before move = %+v, want one", approvals)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, definition, workflow.NodeKindStart)

	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(start)})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := applyManualMoveForStoreTest(t, ctx, store, prepared, nil)
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied ||
		len(moved.Mutation.Created) != 1 || moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(start) {
		t.Fatalf("manual move target = %+v, want start current node", moved.Mutation.Created)
	}
	approvals, err = store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals after move: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("pending approvals after move = %+v, want none", approvals)
	}
}

func TestManualMoveFanoutTransitionReplacesTaskWithEveryBranch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "impl_a")
	transition := workflow.TransitionID("split")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &transition,
		Values:        map[workflow.ModelKey]map[string]string{"plan": {"summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := applyManualMoveForStoreTest(t, ctx, store, prepared, &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflowcontract.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	})
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied ||
		len(moved.Mutation.Removed) != 1 ||
		!moved.Mutation.Removed[0].Equal(source.Reference) ||
		len(moved.Mutation.Created) != 2 {
		t.Fatalf("fan-out manual move = %+v, want task-wide branch replacement", moved)
	}
	branches := make(map[workflow.TransitionBranchKey]bool)
	for _, currentNode := range moved.Mutation.Created {
		branch, ok := currentNode.Reference.TransitionBranchKey()
		if !ok {
			t.Fatalf("created fan-out node = %+v, want branch scope", currentNode)
		}
		branches[branch] = true
	}
	if !branches["split_a"] || !branches["split_b"] {
		t.Fatalf("created branches = %+v, want split_a and split_b", branches)
	}
}

func TestManualMoveFromPartiallyArrivedFanoutReplacesTheWholeTaskGroup(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	split, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode split: %v", err)
	}
	branches := make(map[workflow.TransitionBranchKey]workflow.CurrentNode)
	for _, currentNode := range split.Mutation.Created {
		branch, ok := currentNode.Reference.TransitionBranchKey()
		if !ok {
			t.Fatalf("split current node = %+v, want branch scope", currentNode)
		}
		branches[branch] = currentNode
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       branches["split_a"].Reference,
		TransitionID: "join_a",
		OutputValues: map[string]string{"joined": "branch A"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode partial join: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "synth")
	transition := workflow.TransitionID("synthesize")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &transition,
		Values:        map[workflow.ModelKey]map[string]string{"impl_a": {"joined": "manual branch A"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := applyManualMoveForStoreTest(t, ctx, store, prepared, &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflowcontract.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	})
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeApplied || len(moved.Mutation.Created) != 1 ||
		moved.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) {
		t.Fatalf("manual move from fan-out = %+v, want one post-Join target", moved)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(moved.Mutation.Created[0].Reference) {
		t.Fatalf("current nodes = %+v, want only post-Join target", currentNodes)
	}
}

func TestManualMoveFinalRevalidationReturnsNoOpWithoutExecutionTargetMutation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "automatic plan"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode before final apply: %v", err)
	}
	moved, err := applyManualMoveForStoreTest(t, ctx, store, prepared, &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflowcontract.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	})
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if moved.Outcome != ManualMoveResultOutcomeNoOp || len(moved.CurrentNodes) != 1 ||
		moved.CurrentNodes[0].Reference.NodeID != workflow.NodeIDOf(target) {
		t.Fatalf("final revalidation result = %+v, want authoritative no-op", moved)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("execution target after final no-op = %+v, want unchanged", targetContext.Task.ExecutionTarget)
	}
}

func TestManualMoveScriptValidationRollsBackReplacement(t *testing.T) {
	fixture := newScriptExecutionFixture(t, "scripts/missing", nil)
	prepared, err := fixture.store.PrepareManualMove(fixture.ctx, ManualMoveRequest{
		TaskID:       fixture.task.ID,
		TargetNodeID: fixture.scriptID,
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	if _, err := applyManualMoveForStoreTest(t, fixture.ctx, fixture.store, prepared, nil); err == nil {
		t.Fatal("ApplyManualMove: want invalid script error")
	}
	currentNodes, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].Reference.NodeID == fixture.scriptID {
		t.Fatalf("current nodes after invalid script move = %+v, want unchanged source", currentNodes)
	}
}
