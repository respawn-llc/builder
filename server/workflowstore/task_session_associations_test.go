package workflowstore

import (
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestAssociateTaskSessionBindsFreshSessionToCurrentNode(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()

	association, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  started.Mutation.Created[0].Reference,
		AssociatedAt: associatedAt,
	})
	if err != nil {
		t.Fatalf("AssociateTaskSession: %v", err)
	}
	if !association.CurrentNode.Equal(started.Mutation.Created[0].Reference) ||
		association.SessionID != sessionID ||
		!association.AssociatedAt.Equal(associatedAt) {
		t.Fatalf("association = %+v, want session bound to started current node", association)
	}
	count, err := store.CountTaskSessions(ctx, task.ID)
	if err != nil {
		t.Fatalf("CountTaskSessions: %v", err)
	}
	if count != 1 {
		t.Fatalf("task session count = %d, want 1", count)
	}
	latest, err := store.LatestTaskSessionForNode(ctx, started.Mutation.Created[0].Reference)
	if err != nil {
		t.Fatalf("LatestTaskSessionForNode: %v", err)
	}
	if latest != association {
		t.Fatalf("latest node association = %+v, want %+v", latest, association)
	}
}

func TestAssociateTaskSessionUpsertsRepeatedSerialAssociation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	firstAt := time.UnixMilli(1_700_000_000_000).UTC()
	secondAt := firstAt.Add(time.Second)
	first, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  started.Mutation.Created[0].Reference,
		AssociatedAt: firstAt,
	})
	if err != nil {
		t.Fatalf("first AssociateTaskSession: %v", err)
	}
	second, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  started.Mutation.Created[0].Reference,
		AssociatedAt: secondAt,
	})
	if err != nil {
		t.Fatalf("second AssociateTaskSession: %v", err)
	}
	if !first.CurrentNode.Equal(second.CurrentNode) || !second.AssociatedAt.Equal(secondAt) {
		t.Fatalf("repeated association = %+v, want same key with updated time", second)
	}
	var rowCount int
	if err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM session_workflow_node_associations
WHERE session_id = ?
  AND node_id = ?
  AND transition_branch_key IS NULL`,
		sessionID.String(),
		string(started.Mutation.Created[0].Reference.NodeID),
	).Scan(&rowCount); err != nil {
		t.Fatalf("count serial associations: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("serial association rows = %d, want 1", rowCount)
	}
}

func TestAssociateTaskSessionKeepsOneAssociationPerBranch(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	branchA := workflow.TransitionBranchKey("branch-a")
	branchB := workflow.TransitionBranchKey("branch-b")
	branchAReference, err := workflow.NewCurrentNodeReference(task.ID, started.Mutation.Created[0].Reference.NodeID, &branchA)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference branch A: %v", err)
	}
	branchBReference, err := workflow.NewCurrentNodeReference(task.ID, started.Mutation.Created[0].Reference.NodeID, &branchB)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference branch B: %v", err)
	}
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	for _, currentNode := range []workflow.CurrentNodeReference{branchAReference, branchAReference, branchBReference} {
		if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  currentNode,
			AssociatedAt: associatedAt,
		}); err != nil {
			t.Fatalf("AssociateTaskSession %v: %v", currentNode, err)
		}
	}
	for _, currentNode := range []workflow.CurrentNodeReference{branchAReference, branchBReference} {
		latest, err := store.LatestTaskSessionForNode(ctx, currentNode)
		if err != nil {
			t.Fatalf("LatestTaskSessionForNode %v: %v", currentNode, err)
		}
		if latest.SessionID != sessionID || !latest.CurrentNode.Equal(currentNode) {
			t.Fatalf("latest branch association = %+v, want session %q at %v", latest, sessionID, currentNode)
		}
	}
	var rowCount int
	if err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM session_workflow_node_associations
WHERE session_id = ?
  AND node_id = ?
  AND transition_branch_key IS NOT NULL`,
		sessionID.String(),
		string(started.Mutation.Created[0].Reference.NodeID),
	).Scan(&rowCount); err != nil {
		t.Fatalf("count branch associations: %v", err)
	}
	if rowCount != 2 {
		t.Fatalf("branch association rows = %d, want 2", rowCount)
	}
}

func TestAssociateTaskSessionRetainsVisitsAcrossNodes(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	firstAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  started.Mutation.Created[0].Reference,
		AssociatedAt: firstAt,
	}); err != nil {
		t.Fatalf("AssociateTaskSession plan: %v", err)
	}
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       started.Mutation.Created[0].Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode plan: %v", err)
	}
	reviewReference := completed.Mutation.Created[0].Reference
	if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  reviewReference,
		AssociatedAt: firstAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("AssociateTaskSession review: %v", err)
	}
	count, err := store.CountTaskSessions(ctx, task.ID)
	if err != nil {
		t.Fatalf("CountTaskSessions: %v", err)
	}
	if count != 1 {
		t.Fatalf("task session count = %d, want 1", count)
	}
	for _, currentNode := range []workflow.CurrentNodeReference{started.Mutation.Created[0].Reference, reviewReference} {
		latest, err := store.LatestTaskSessionForNode(ctx, currentNode)
		if err != nil {
			t.Fatalf("LatestTaskSessionForNode %v: %v", currentNode, err)
		}
		if latest.SessionID != sessionID {
			t.Fatalf("latest association = %+v, want reused session %q", latest, sessionID)
		}
	}
}

func TestAssociateTaskSessionRejectsCrossTaskOwnership(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	firstTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	secondTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	firstCurrentNode := startTask(t, ctx, store, firstTask.ID).Mutation.Created[0].Reference
	secondCurrentNode := startTask(t, ctx, store, secondTask.ID).Mutation.Created[0].Reference
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  firstCurrentNode,
		AssociatedAt: associatedAt,
	}); err != nil {
		t.Fatalf("AssociateTaskSession first task: %v", err)
	}
	if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  secondCurrentNode,
		AssociatedAt: associatedAt,
	}); err == nil {
		t.Fatal("AssociateTaskSession cross task succeeded")
	}
	firstCount, err := store.CountTaskSessions(ctx, firstTask.ID)
	if err != nil {
		t.Fatalf("CountTaskSessions first: %v", err)
	}
	secondCount, err := store.CountTaskSessions(ctx, secondTask.ID)
	if err != nil {
		t.Fatalf("CountTaskSessions second: %v", err)
	}
	if firstCount != 1 || secondCount != 0 {
		t.Fatalf("task session counts = %d, %d; want 1, 0", firstCount, secondCount)
	}
	if _, err := store.LatestTaskSessionForNode(ctx, secondCurrentNode); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second task association error = %v, want sql.ErrNoRows", err)
	}
}

func TestLatestTaskSessionForNodeBreaksAssociationTimeTiesBySessionID(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	currentNode := startTask(t, ctx, store, task.ID).Mutation.Created[0].Reference
	firstSessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID first: %v", err)
	}
	secondSessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID second: %v", err)
	}
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	for _, sessionID := range []runtimeids.SessionID{firstSessionID, secondSessionID} {
		if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  currentNode,
			AssociatedAt: associatedAt,
		}); err != nil {
			t.Fatalf("AssociateTaskSession %q: %v", sessionID, err)
		}
	}
	ids := []string{firstSessionID.String(), secondSessionID.String()}
	sort.Strings(ids)
	latest, err := store.LatestTaskSessionForNode(ctx, currentNode)
	if err != nil {
		t.Fatalf("LatestTaskSessionForNode: %v", err)
	}
	if latest.SessionID.String() != ids[len(ids)-1] {
		t.Fatalf("latest session = %q, want greatest equal-time id %q", latest.SessionID, ids[len(ids)-1])
	}
}
