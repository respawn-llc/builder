package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/runtimeids"
)

func TestCompleteCurrentNodeWithoutApprovalDoesNotEmitQueryFailureDiagnostics(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	diagnostics := testsetup.CaptureSlog(t)

	if _, err := store.CompleteCurrentNode(metadata.WithQueryFailureDiagnostics(ctx), CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "completed"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("ordinary completion diagnostics = %q, want none", diagnostics.String())
	}
}

func TestCompleteCurrentNodeAtomicallyReplacesAgentAndReturnsSuccessorIntent(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v, want one source current node", started.Mutation)
	}
	source := started.Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	targetNode := nodeByKey(t, definition, "review")
	target, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(targetNode), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference target: %v", err)
	}

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if len(completed.Mutation.Removed) != 1 || !completed.Mutation.Removed[0].Equal(source.Reference) {
		t.Fatalf("completion removed = %+v, want source current node", completed.Mutation.Removed)
	}
	if len(completed.Mutation.Created) != 1 ||
		!completed.Mutation.Created[0].Reference.Equal(target) ||
		completed.Mutation.Created[0].Scheduling == nil ||
		completed.Mutation.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady ||
		completed.Mutation.Created[0].AgentExecutionSelection == nil ||
		completed.Mutation.Created[0].AgentExecutionSelection.Assignee != "coder" ||
		completed.Mutation.Created[0].AgentExecutionSelection.Origin != workflow.AssigneeOriginConfiguredFallback {
		t.Fatalf("completion created = %+v, want ready review current node", completed.Mutation.Created)
	}
	if completed.Handoff != (CompletionHandoff{SourceNodeDisplayName: "Plan", DestinationDisplayName: "Review"}) {
		t.Fatalf("completion handoff = %+v, want Plan -> Review", completed.Handoff)
	}
	if len(completed.AutomaticIntents) != 1 || !completed.AutomaticIntents[0].CurrentNode.Equal(target) {
		t.Fatalf("completion automatic intents = %+v, want review current node", completed.AutomaticIntents)
	}
	if completed.AutomaticIntents[0].NodeKind != workflow.NodeKindAgent {
		t.Fatalf("completion automatic intent kind = %q, want %q", completed.AutomaticIntents[0].NodeKind, workflow.NodeKindAgent)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after completion: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(target) ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("current nodes after completion = %+v, want only ready review", currentNodes)
	}

	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "stale"},
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale completion error = %v, want sql.ErrNoRows", err)
	}
	currentNodes, err = store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after stale completion: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(target) {
		t.Fatalf("current nodes after stale completion = %+v, want unchanged review", currentNodes)
	}
}

func TestCompleteCurrentNodeWaitsForConcurrentWriterWithoutLosingItsSnapshot(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	writer, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin concurrent writer: %v", err)
	}
	defer func() { _ = writer.Rollback() }()
	if _, err := writer.ExecContext(
		ctx,
		"UPDATE tasks SET updated_at_unix_ms = updated_at_unix_ms WHERE id = ?",
		task.ID,
	); err != nil {
		t.Fatalf("hold concurrent writer: %v", err)
	}

	type completionOutcome struct {
		result CurrentNodeCompletionResult
		err    error
	}
	completed := make(chan completionOutcome, 1)
	go func() {
		result, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: "review",
			OutputValues: map[string]string{"summary": "completed after contention"},
		})
		completed <- completionOutcome{result: result, err: err}
	}()

	select {
	case outcome := <-completed:
		t.Fatalf("completion returned before the concurrent writer committed: result=%+v err=%v", outcome.result, outcome.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writer.Commit(); err != nil {
		t.Fatalf("commit concurrent writer: %v", err)
	}

	select {
	case outcome := <-completed:
		if outcome.err != nil {
			t.Fatalf("CompleteCurrentNode after concurrent writer: %v", outcome.err)
		}
		if len(outcome.result.Mutation.Created) != 1 {
			t.Fatalf("completion mutation = %+v, want one successor", outcome.result.Mutation)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CompleteCurrentNode did not finish after the concurrent writer committed")
	}
}

func TestCompleteCurrentNodeInfersOnlyOutgoingFanoutTransition(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode without transition ID: %v", err)
	}
	if len(completed.Mutation.Created) != 2 {
		t.Fatalf("completion mutation = %+v, want two fan-out branches", completed.Mutation)
	}
	branches := map[workflow.TransitionBranchKey]bool{}
	for _, currentNode := range completed.Mutation.Created {
		branchKey, present := currentNode.Reference.TransitionBranchKey()
		if !present {
			t.Fatalf("completion Current Node = %+v, want branch scope", currentNode)
		}
		branches[branchKey] = true
	}
	if !branches["split_a"] || !branches["split_b"] {
		t.Fatalf("completion branches = %+v, want split_a and split_b", branches)
	}
	if len(completed.AutomaticIntents) != 2 {
		t.Fatalf("completion automatic intents = %+v, want both fan-out branches", completed.AutomaticIntents)
	}
	for _, intent := range completed.AutomaticIntents {
		if intent.NodeKind != workflow.NodeKindAgent {
			t.Fatalf("fan-out automatic intent = %+v, want Agent Node kind", intent)
		}
	}
}

func TestCompleteCurrentNodeFanoutPendingApprovalCarriesCommentary(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "split_a")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		OutputValues: map[string]string{"summary": "plan complete"},
		Commentary:   "  Both branches are ready for review.  ",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("fan-out completion did not create a pending Approval")
	}
	if completed.PendingApproval.Commentary != "Both branches are ready for review." {
		t.Fatalf("pending Approval commentary = %q", completed.PendingApproval.Commentary)
	}
}

func TestCompleteCurrentNodeJoinContinuationReturnsTargetNodeKind(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := edgeByKey(t, def, "join_a")
		record := workflowGraphSaveEdgeRecord(t, req.Edges, edge.ID)
		record.Parameters = append(
			record.Parameters,
			workflow.Parameter{Key: "agent_role", Purpose: workflow.ParameterPurposeTargetAssignee},
		)
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	split, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode split: %v", err)
	}
	if len(split.Mutation.Created) != 2 {
		t.Fatalf("split mutation = %+v, want two branches", split.Mutation)
	}
	for _, intent := range split.AutomaticIntents {
		if intent.NodeKind != workflow.NodeKindAgent {
			t.Fatalf("split automatic intent = %+v, want Agent Node kind", intent)
		}
	}

	first, second := split.Mutation.Created[0], split.Mutation.Created[1]
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       first.Reference,
		TransitionID: "join_a",
		OutputValues: map[string]string{"joined": "branch complete"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode first join arrival: %v", err)
	}
	joined, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       second.Reference,
		TransitionID: "join_b",
		OutputValues: map[string]string{},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode second join arrival: %v", err)
	}
	if len(joined.AutomaticIntents) != 1 {
		t.Fatalf("join continuation automatic intents = %+v, want one synth successor", joined.AutomaticIntents)
	}
	if joined.AutomaticIntents[0].NodeKind != workflow.NodeKindAgent {
		t.Fatalf("join continuation automatic intent = %+v, want Agent Node kind", joined.AutomaticIntents[0])
	}
}

func TestCompleteCurrentNodeFanoutPreviousTargetOrNewRetainsBranchSessions(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	branches := []struct {
		edgeKey   string
		targetKey string
	}{
		{edgeKey: "split_a", targetKey: "impl_a"},
		{edgeKey: "split_b", targetKey: "impl_b"},
	}
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		for _, branch := range branches {
			edge := edgeByKey(t, definition, branch.edgeKey)
			record := workflowGraphSaveEdgeRecord(t, req.Edges, edge.ID)
			record.ContextMode = workflow.ContextModeContinueSession
			record.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew}
		}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	expectedSessions := make(map[workflow.TransitionBranchKey]runtimeids.SessionID)
	for index, branch := range branches {
		target := nodeByKey(t, definition, branch.targetKey)
		branchKey := workflow.TransitionBranchKey(branch.edgeKey)
		targetReference, err := workflow.NewCurrentNodeReference(
			task.ID,
			workflow.NodeIDOf(target),
			&branchKey,
		)
		if err != nil {
			t.Fatalf("NewCurrentNodeReference %s: %v", branch.edgeKey, err)
		}
		expectedSessions[branchKey] = associateTaskSessionForTest(
			t,
			ctx,
			store,
			binding,
			cfg,
			targetReference,
			time.UnixMilli(1_700_000_000_000+int64(index)).UTC(),
		)
	}

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "split",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if len(completed.Mutation.Created) != len(expectedSessions) {
		t.Fatalf("completion created = %+v, want one Current Node per retained branch session", completed.Mutation.Created)
	}
	for _, currentNode := range completed.Mutation.Created {
		branchKey, present := currentNode.Reference.TransitionBranchKey()
		if !present {
			t.Fatalf("completion Current Node = %+v, want branch scope", currentNode)
		}
		expectedSessionID, exists := expectedSessions[branchKey]
		if !exists {
			t.Fatalf("completion branch = %q, want one of %+v", branchKey, expectedSessions)
		}
		if currentNode.SessionID == nil || *currentNode.SessionID != expectedSessionID {
			t.Fatalf("completion branch %q session = %v, want retained session %q", branchKey, currentNode.SessionID, expectedSessionID)
		}
	}
}

func TestCompleteCurrentNodeRequiresTransitionIDForSeveralOutgoingTransitions(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		source := nodeByKey(t, def, "plan")
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		groupID := workflow.TransitionGroupID("group-alternate-" + workflowID.String())
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           groupID,
			WorkflowID:   workflowID,
			SourceNodeID: workflow.NodeIDOf(source),
			TransitionID: "alternate",
			DisplayName:  "Alternate",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                workflow.EdgeID("edge-alternate-" + workflowID.String()),
			WorkflowID:        workflowID,
			TransitionGroupID: groupID,
			Key:               "alternate",
			TargetNodeID:      workflow.NodeIDOf(done),
			ContextMode:       workflow.ContextModeNewSession,
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	_, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	var validation CompletionValidationError
	if !errors.As(err, &validation) ||
		len(validation.Issues) != 1 ||
		validation.Issues[0].Code != CompletionCodeTransitionIDRequired {
		t.Fatalf("completion error = %v, want transition-required validation", err)
	}
}

func TestCompleteCurrentNodeCreatesFrozenPendingApprovalAndRetainsSource(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition before approval edit: %v", err)
	}
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	reviewNode := nodeByKey(t, definition, "review")
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID)
		edge.RequiresApproval = true
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}
	})
	_, workflowRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after approval edit: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	sourceSessionID := associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, source.Reference)

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "frozen plan"},
		Commentary:   "  Ready to merge after approval.  ",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if len(completed.Mutation.Removed) != 0 || len(completed.Mutation.Created) != 0 || len(completed.AutomaticIntents) != 0 {
		t.Fatalf("pending approval completion mutation = %+v, want source retained without successor intent", completed)
	}
	if completed.PendingApproval == nil {
		t.Fatal("pending approval completion omitted approval projection")
	}
	approval := *completed.PendingApproval
	if err := approval.ID.Validate(); err != nil {
		t.Fatalf("approval id = %q, want UUID v4: %v", approval.ID, err)
	}
	if !approval.Source.Equal(source.Reference) || approval.SourceSessionID == nil || *approval.SourceSessionID != sourceSessionID {
		t.Fatalf("approval source = %+v, want current source with session %q", approval, sourceSessionID)
	}
	if approval.WorkflowVersion != workflowRecord.Version || approval.Transition.Group.TransitionID != "review" {
		t.Fatalf("approval transition snapshot = %+v, want workflow version %d and review transition", approval, workflowRecord.Version)
	}
	if approval.OutputValues["summary"] != "frozen plan" || len(approval.Branches) != 1 {
		t.Fatalf("approval materialized values/branches = %+v, want one frozen target", approval)
	}
	if approval.Commentary != "Ready to merge after approval." {
		t.Fatalf("approval commentary = %q, want trimmed frozen commentary", approval.Commentary)
	}
	branch := approval.Branches[0]
	if branch.Target.CurrentNode.CurrentInputValues["summary"] != "frozen plan" ||
		branch.Target.CurrentNode.SessionID == nil ||
		*branch.Target.CurrentNode.SessionID != sourceSessionID ||
		branch.ContextSourceResolution.SessionID == nil ||
		*branch.ContextSourceResolution.SessionID != sourceSessionID {
		t.Fatalf("approval branch snapshot = %+v, want frozen immediate-source target session and values", branch)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes while pending: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(source.Reference) ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("current nodes while pending = %+v, want retained ready source with no waiting scheduling state", currentNodes)
	}
	eligible, err := store.IsCurrentNodeExecutionEligible(ctx, source.Reference)
	if err != nil {
		t.Fatalf("IsCurrentNodeExecutionEligible: %v", err)
	}
	if eligible {
		t.Fatal("pending approval source was eligible for execution")
	}
	currentDefinition, currentRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition before frozen edge removal: %v", err)
	}
	edgeRemoval := workflowGraphSaveRequestFromDefinition(workflowID, currentRecord.Version, true, currentDefinition)
	edgeRemoval.Edges = removeWorkflowGraphSaveEdge(edgeRemoval.Edges, reviewEdgeID)
	preview, err := store.PreviewWorkflowGraphSave(ctx, edgeRemoval)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave frozen target edge removal: %v", err)
	}
	if preview.Impact.EdgeTaskReferenceCount != 1 || workflowGraphSaveBlockerCount(preview.Blockers, "edge_task_references") != 1 {
		t.Fatalf("frozen target edge removal preview = %+v, want one protected approval edge", preview)
	}
	saved, err := store.SaveWorkflowGraph(ctx, edgeRemoval)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph frozen target edge removal: %v", err)
	}
	if saved.Saved || workflowGraphSaveBlockerCount(saved.Blockers, "edge_task_references") != 1 {
		t.Fatalf("frozen target edge removal save = %+v, want blocked save", saved)
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "must stay pending"},
	}); !errors.Is(err, ErrCurrentNodePendingApproval) {
		t.Fatalf("CompleteCurrentNode while pending = %v, want ErrCurrentNodePendingApproval", err)
	}
	afterRestart, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals after restart: %v", err)
	}
	if len(afterRestart) != 1 ||
		afterRestart[0].ID != approval.ID ||
		!afterRestart[0].Source.Equal(source.Reference) ||
		afterRestart[0].Commentary != "Ready to merge after approval." ||
		afterRestart[0].Branches[0].Target.CurrentNode.CurrentInputValues["summary"] != "frozen plan" {
		t.Fatalf("pending approvals after restart = %+v, want frozen approval", afterRestart)
	}

	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID)
		edge.PromptTemplate = "Changed after approval."
	})
	frozenAfterEdit, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals after graph edit: %v", err)
	}
	if len(frozenAfterEdit) != 1 ||
		frozenAfterEdit[0].Commentary != "Ready to merge after approval." ||
		frozenAfterEdit[0].Branches[0].EffectiveEdge.ContextMode != workflow.ContextModeContinueSession ||
		frozenAfterEdit[0].Branches[0].EffectiveEdge.TargetNodeID != workflow.NodeIDOf(reviewNode) {
		t.Fatalf("approval after graph edit = %+v, want frozen edge configuration", frozenAfterEdit)
	}
	applied, err := store.ApplyPendingApproval(ctx, approval.ID)
	if err != nil {
		t.Fatalf("ApplyPendingApproval: %v", err)
	}
	if applied.ResolvedApproval.ID != approval.ID ||
		len(applied.Mutation.Removed) != 1 ||
		!applied.Mutation.Removed[0].Equal(source.Reference) ||
		len(applied.Mutation.Created) != 1 {
		t.Fatalf("applied pending approval = %+v, want source replacement", applied)
	}
	target := applied.Mutation.Created[0]
	if target.Reference.NodeID != workflow.NodeIDOf(reviewNode) ||
		target.SessionID == nil ||
		*target.SessionID != sourceSessionID ||
		target.CurrentInputValues["summary"] != "frozen plan" {
		t.Fatalf("applied target = %+v, want frozen context and materialized values", target)
	}
	remaining, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals after apply: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("pending approvals after apply = %+v, want none", remaining)
	}
}

func TestDeleteTaskRemovesPendingApprovalBeforeCurrentNodeCascade(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID).RequiresApproval = true
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "delete me"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("completion did not create pending approval")
	}

	deleted, err := store.DeleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if deleted.ID != task.ID {
		t.Fatalf("deleted task = %+v, want %q", deleted, task.ID)
	}
	remaining, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals after deletion: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("pending approvals after task deletion = %+v, want none", remaining)
	}
}

func TestDeleteWorkflowRemovesPendingApprovalsBeforeCurrentNodeCascade(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID).RequiresApproval = true
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "delete workflow"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("completion did not create pending approval")
	}
	impact, err := store.PreviewWorkflowDelete(ctx, workflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	deleted, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(impact, false))
	if err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	}
	if deleted.Deleted || len(deleted.Blockers) == 0 {
		t.Fatalf("workflow deletion = %+v, want quiescence blockers", deleted)
	}
}

func TestDeleteProjectRemovesPendingApprovalsBeforeCurrentNodeCascade(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID).RequiresApproval = true
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "delete project"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("completion did not create pending approval")
	}
	blockers, err := store.DeleteProject(ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		Artifacts: projectDeleteArtifactsNoop{},
	})
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if len(blockers) == 0 {
		t.Fatalf("project deletion blockers = %+v, want pending Approval blocker", blockers)
	}
}

func TestCompleteCurrentNodeNewSessionTargetDoesNotRetainSourceSession(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	source := started.Mutation.Created[0]
	associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, source.Reference)

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if len(completed.Mutation.Created) != 1 || completed.Mutation.Created[0].SessionID != nil {
		t.Fatalf("new-session target = %+v, want an unbound current node", completed.Mutation.Created)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].SessionID != nil {
		t.Fatalf("persisted new-session target = %+v, want an unbound current node", currentNodes)
	}
}

func TestCompleteCurrentNodeContinueSessionUsesImmediateSourceSession(t *testing.T) {
	fixture := newImmediateContextCompletionFixture(t, workflow.ContextModeContinueSession)

	completed, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if len(completed.Mutation.Created) != 1 ||
		completed.Mutation.Created[0].SessionID == nil ||
		*completed.Mutation.Created[0].SessionID != fixture.sessionID {
		t.Fatalf("continue-session target = %+v, want source session %q", completed.Mutation.Created, fixture.sessionID)
	}
}

func TestCompleteCurrentNodeRetainedSuccessorCommitsExactSessionProvenance(t *testing.T) {
	fixture := newImmediateContextCompletionFixture(t, workflow.ContextModeContinueSession)

	completed, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if len(completed.Mutation.Created) != 1 {
		t.Fatalf("created Current Nodes = %+v, want one retained successor", completed.Mutation.Created)
	}
	successor := completed.Mutation.Created[0]
	if successor.SessionID == nil || *successor.SessionID != fixture.sessionID {
		t.Fatalf("retained successor = %+v, want Session %q", successor, fixture.sessionID)
	}
	if err := fixture.store.ValidateCurrentNodeSessionBinding(
		fixture.ctx,
		fixture.sessionID,
		successor.Reference,
	); err != nil {
		t.Fatalf(
			"retained successor committed without exact Session provenance: %v",
			err,
		)
	}
}

func TestCompleteCurrentNodeCompactAndContinueSessionUsesImmediateSourceSession(t *testing.T) {
	fixture := newImmediateContextCompletionFixture(t, workflow.ContextModeCompactAndContinueSession)

	completed, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if len(completed.Mutation.Created) != 1 ||
		completed.Mutation.Created[0].SessionID == nil ||
		*completed.Mutation.Created[0].SessionID != fixture.sessionID {
		t.Fatalf("compact-and-continue target = %+v, want source session %q", completed.Mutation.Created, fixture.sessionID)
	}
}

func TestCompleteCurrentNodeSelectedNodeContextUsesLatestAssociatedSession(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	auditEdgeID := edgeByKey(t, definition, "audit").ID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, auditEdgeID)
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{
			Kind:    workflow.ContextSourceSelectedNode,
			NodeKey: "plan",
		}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	plan := started.Mutation.Created[0]
	associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, plan.Reference)
	reviewResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode plan: %v", err)
	}
	review := reviewResult.Mutation.Created[0]
	latestPlanSessionID := associateTaskSessionForTest(
		t,
		ctx,
		store,
		binding,
		cfg,
		plan.Reference,
		time.UnixMilli(1_700_000_000_001).UTC(),
	)
	associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, review.Reference)

	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       review.Reference,
		TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode review: %v", err)
	}
	if len(auditResult.Mutation.Created) != 1 ||
		auditResult.Mutation.Created[0].SessionID == nil ||
		*auditResult.Mutation.Created[0].SessionID != latestPlanSessionID {
		t.Fatalf("selected-node target = %+v, want latest plan session %q", auditResult.Mutation.Created, latestPlanSessionID)
	}
}

func TestCompleteCurrentNodePreviousTargetContextUsesLatestAssociatedSession(t *testing.T) {
	fixture := newReworkContextCompletionFixture(t, workflow.ContextSourcePreviousTarget)
	associateTaskSessionForTest(
		t,
		fixture.ctx,
		fixture.store,
		fixture.binding,
		fixture.cfg,
		fixture.review.Reference,
		time.UnixMilli(1_700_000_000_000).UTC(),
	)
	reviewSessionID := associateTaskSessionForTest(
		t,
		fixture.ctx,
		fixture.store,
		fixture.binding,
		fixture.cfg,
		fixture.review.Reference,
		time.UnixMilli(1_700_000_000_001).UTC(),
	)
	reworkResult, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.audit.Reference,
		TransitionID: "rework",
		OutputValues: map[string]string{"summary": "review again"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode audit: %v", err)
	}
	if len(reworkResult.Mutation.Created) != 1 ||
		reworkResult.Mutation.Created[0].SessionID == nil ||
		*reworkResult.Mutation.Created[0].SessionID != reviewSessionID {
		t.Fatalf("previous-target current node = %+v, want review session %q", reworkResult.Mutation.Created, reviewSessionID)
	}
}

func TestCompleteCurrentNodePreviousTargetContextFailsWithoutAssociatedSession(t *testing.T) {
	fixture := newReworkContextCompletionFixture(t, workflow.ContextSourcePreviousTarget)

	if _, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.audit.Reference,
		TransitionID: "rework",
		OutputValues: map[string]string{"summary": "review again"},
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("CompleteCurrentNode error = %v, want sql.ErrNoRows", err)
	}
	currentNodes, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.audit.Reference.TaskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(fixture.audit.Reference) {
		t.Fatalf("current nodes after rejected previous target = %+v, want unchanged audit node", currentNodes)
	}
}

func TestCompleteCurrentNodePreviousTargetOrNewContextFallsBackToNewSession(t *testing.T) {
	fixture := newReworkContextCompletionFixture(t, workflow.ContextSourcePreviousTargetOrNew)

	reworkResult, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.audit.Reference,
		TransitionID: "rework",
		OutputValues: map[string]string{"summary": "review again"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode audit: %v", err)
	}
	if len(reworkResult.Mutation.Created) != 1 || reworkResult.Mutation.Created[0].SessionID != nil {
		t.Fatalf("previous-target-or-new current node = %+v, want an unbound target", reworkResult.Mutation.Created)
	}
}

func TestCompleteCurrentNodePreviousTargetOrNewContextUsesLatestAssociatedSession(t *testing.T) {
	fixture := newReworkContextCompletionFixture(t, workflow.ContextSourcePreviousTargetOrNew)
	reviewSessionID := associateTaskSessionForTest(
		t,
		fixture.ctx,
		fixture.store,
		fixture.binding,
		fixture.cfg,
		fixture.review.Reference,
		time.UnixMilli(1_700_000_000_000).UTC(),
	)

	reworkResult, err := fixture.store.CompleteCurrentNode(fixture.ctx, CurrentNodeCompletionRequest{
		Source:       fixture.audit.Reference,
		TransitionID: "rework",
		OutputValues: map[string]string{"summary": "review again"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode audit: %v", err)
	}
	if len(reworkResult.Mutation.Created) != 1 ||
		reworkResult.Mutation.Created[0].SessionID == nil ||
		*reworkResult.Mutation.Created[0].SessionID != reviewSessionID {
		t.Fatalf("previous-target-or-new current node = %+v, want review session %q", reworkResult.Mutation.Created, reviewSessionID)
	}
}

type reworkContextCompletionFixture struct {
	ctx        context.Context
	store      *Store
	binding    metadata.Binding
	cfg        config.App
	workflowID runtimeids.WorkflowID
	review     workflow.CurrentNode
	audit      workflow.CurrentNode
}

func newReworkContextCompletionFixture(t *testing.T, contextSource workflow.ContextSourceKind) reworkContextCompletionFixture {
	t.Helper()
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	audit := nodeByKey(t, definition, "audit")
	review := nodeByKey(t, definition, "review")
	reworkGroupID := workflow.TransitionGroupID("group-rework-" + workflowID.String())
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           reworkGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: workflow.NodeIDOf(audit),
			TransitionID: "rework",
			DisplayName:  "Rework",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                workflow.EdgeID("edge-rework-" + workflowID.String()),
			WorkflowID:        workflowID,
			TransitionGroupID: reworkGroupID,
			Key:               "rework",
			TargetNodeID:      workflow.NodeIDOf(review),
			ContextMode:       workflow.ContextModeContinueSession,
			ContextSource:     workflow.ContextSource{Kind: contextSource},
			PromptTemplate:    "Review {{.Params.summary}}.",
			Parameters: []workflow.Parameter{{
				Key:         "summary",
				Description: "Rework summary.",
			}},
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	reviewResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode plan: %v", err)
	}
	auditResult, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       reviewResult.Mutation.Created[0].Reference,
		TransitionID: "audit",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode review: %v", err)
	}
	return reworkContextCompletionFixture{
		ctx:        ctx,
		store:      store,
		binding:    binding,
		cfg:        cfg,
		workflowID: workflowID,
		review:     reviewResult.Mutation.Created[0],
		audit:      auditResult.Mutation.Created[0],
	}
}

type immediateContextCompletionFixture struct {
	ctx       context.Context
	store     *Store
	source    workflow.CurrentNode
	sessionID runtimeids.SessionID
}

func newImmediateContextCompletionFixture(t *testing.T, contextMode workflow.ContextMode) immediateContextCompletionFixture {
	t.Helper()
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID)
		edge.ContextMode = contextMode
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	source := started.Mutation.Created[0]
	sessionID := associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, source.Reference)
	return immediateContextCompletionFixture{
		ctx:       ctx,
		store:     store,
		source:    source,
		sessionID: sessionID,
	}
}

func associateAndBindCurrentNodeSessionForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	binding metadata.Binding,
	cfg config.App,
	currentNode workflow.CurrentNodeReference,
) runtimeids.SessionID {
	t.Helper()
	sessionID := associateTaskSessionForTest(t, ctx, store, binding, cfg, currentNode, time.UnixMilli(1_700_000_000_000).UTC())
	if _, err := store.db.ExecContext(ctx, `UPDATE task_current_nodes
SET session_id = ?
WHERE task_id = ?
  AND node_id = ?
  AND transition_branch_key IS NULL`,
		sessionID.String(),
		string(currentNode.TaskID),
		string(currentNode.NodeID),
	); err != nil {
		t.Fatalf("bind current node session: %v", err)
	}
	return sessionID
}

func associateTaskSessionForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	binding metadata.Binding,
	cfg config.App,
	currentNode workflow.CurrentNodeReference,
	associatedAt time.Time,
) runtimeids.SessionID {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  currentNode,
		AssociatedAt: associatedAt,
	}); err != nil {
		t.Fatalf("AssociateTaskSession: %v", err)
	}
	return sessionID
}
