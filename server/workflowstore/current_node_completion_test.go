package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/runtimeids"
)

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
		completed.Mutation.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("completion created = %+v, want ready review current node", completed.Mutation.Created)
	}
	if completed.Handoff != (CompletionHandoff{SourceNodeDisplayName: "Plan", DestinationDisplayName: "Review"}) {
		t.Fatalf("completion handoff = %+v, want Plan -> Review", completed.Handoff)
	}
	if len(completed.AutomaticIntents) != 1 || !completed.AutomaticIntents[0].Equal(target) {
		t.Fatalf("completion automatic intents = %+v, want review current node", completed.AutomaticIntents)
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
	ctx     context.Context
	store   *Store
	binding metadata.Binding
	cfg     config.App
	review  workflow.CurrentNode
	audit   workflow.CurrentNode
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
	reworkGroupID := workflow.TransitionGroupID("group-rework-" + string(workflowID))
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		auditRecord := workflowGraphSaveNodeRecord(t, req.Nodes, workflow.NodeIDOf(audit))
		auditRecord.OutputFields = append(auditRecord.OutputFields, workflow.OutputField{Name: "summary", Description: "Rework summary."})
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
			ID:           reworkGroupID,
			WorkflowID:   workflowID,
			SourceNodeID: workflow.NodeIDOf(audit),
			TransitionID: "rework",
			DisplayName:  "Rework",
		})
		req.Edges = append(req.Edges, EdgeRecord{
			ID:                workflow.EdgeID("edge-rework-" + string(workflowID)),
			WorkflowID:        workflowID,
			TransitionGroupID: reworkGroupID,
			Key:               "rework",
			TargetNodeID:      workflow.NodeIDOf(review),
			ContextMode:       workflow.ContextModeContinueSession,
			ContextSource:     workflow.ContextSource{Kind: contextSource},
			PromptTemplate:    "Review {{.Inputs.summary}}.",
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
		ctx:     ctx,
		store:   store,
		binding: binding,
		cfg:     cfg,
		review:  reviewResult.Mutation.Created[0],
		audit:   auditResult.Mutation.Created[0],
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
