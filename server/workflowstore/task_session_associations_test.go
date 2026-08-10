package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
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

func TestResolveCurrentNodeStartContextDefersImmediateSourceForThinkingContract(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	store.roleResolver = completionTargetCatalog{
		roles: map[string]workflow.TargetAgentRole{
			"coder": {
				Identity:         "coder",
				QuestionsEnabled: true,
				Thinking: workflow.ThinkingCapability{
					ReasoningCapable: true,
					Finite:           true,
					Levels:           []string{"low", "high"},
				},
			},
		},
	}
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, edgeByKey(t, definition, "review").ID)
		edge.ContextMode = workflow.ContextModeContinueSession
		edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}
		edge.ThinkingSelection = workflow.ThinkingSelectionPreviousNode
		edge.Parameters = append(edge.Parameters, workflow.Parameter{
			Key:     "thinking",
			Purpose: workflow.ParameterPurposeTargetThinking,
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]

	if _, err := store.ResolveCurrentNodeStartContext(ctx, started.Reference); err != nil {
		t.Fatalf("ResolveCurrentNodeStartContext: %v", err)
	}
}

func TestBindSessionToCurrentNodeEstablishesLiveBindingAndProvenance(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}

	association, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  started.Mutation.Created[0].Reference,
			AssociatedAt: time.UnixMilli(1_700_000_000_000).UTC(),
		},
	})
	if err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].SessionID == nil || *currentNodes[0].SessionID != sessionID {
		t.Fatalf("current nodes = %+v, want one node bound to %q", currentNodes, sessionID)
	}
	if association.SessionID != sessionID || !association.CurrentNode.Equal(started.Mutation.Created[0].Reference) {
		t.Fatalf("live binding association = %+v", association)
	}
	latest, err := store.LatestTaskSessionForNode(ctx, started.Mutation.Created[0].Reference)
	if err != nil {
		t.Fatalf("LatestTaskSessionForNode: %v", err)
	}
	if latest.SessionID != sessionID {
		t.Fatalf("latest association = %+v, want %q", latest, sessionID)
	}
	if count, err := store.CountTaskSessions(ctx, task.ID); err != nil || count != 1 {
		t.Fatalf("CountTaskSessions = %d, %v, want 1", count, err)
	}
	if err := store.ValidateCurrentNodeSessionBinding(ctx, sessionID, started.Mutation.Created[0].Reference); err != nil {
		t.Fatalf("ValidateCurrentNodeSessionBinding: %v", err)
	}
}

func TestBindSessionToBranchCurrentNodeReplacesExpectedFanoutSourceSession(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	sourceSessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("parse source Session ID: %v", err)
	}
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sourceSessionID,
			CurrentNode:  started.Reference,
			AssociatedAt: time.UnixMilli(1_700_000_000_000).UTC(),
		},
	}); err != nil {
		t.Fatalf("bind source Session: %v", err)
	}

	branchKey := workflow.TransitionBranchKey("qa")
	if _, err := store.db.ExecContext(ctx, `DELETE FROM task_current_nodes WHERE task_id = ?`, string(task.ID)); err != nil {
		t.Fatalf("delete serial Current Node: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO task_active_fanouts (task_id) VALUES (?)`, string(task.ID)); err != nil {
		t.Fatalf("insert active fan-out: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO task_active_fanout_branches (
    task_id, transition_branch_key, arrival_state, arrival_values_json
) VALUES (?, ?, 'pending', NULL)`, string(task.ID), string(branchKey)); err != nil {
		t.Fatalf("insert active fan-out branch: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO task_current_nodes (
    task_id, node_id, transition_branch_key, current_input_values_json,
    prior_node_values_json, session_id, scheduling_state, entered_by_edge_id,
    effective_assignee, assignee_origin
) VALUES (?, ?, ?, '{}', '{"transition_parameters":{}}', ?, 'ready', ?, ?, ?)`,
		string(task.ID),
		string(started.Reference.NodeID),
		string(branchKey),
		sourceSessionID.String(),
		string(*started.EnteredByEdgeID),
		started.AgentExecutionSelection.Assignee,
		string(started.AgentExecutionSelection.Origin),
	); err != nil {
		t.Fatalf("insert retained fan-out Current Node: %v", err)
	}
	branchReference, err := workflow.NewCurrentNodeReference(task.ID, started.Reference.NodeID, &branchKey)
	if err != nil {
		t.Fatalf("create branch Current Node reference: %v", err)
	}
	cloneSessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("parse clone Session ID: %v", err)
	}

	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    cloneSessionID,
			CurrentNode:  branchReference,
			AssociatedAt: time.UnixMilli(1_700_000_001_000).UTC(),
		},
		ExpectedCurrentSessionID: &sourceSessionID,
	}); err != nil {
		t.Fatalf("replace fan-out source Session with clone: %v", err)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("list branch Current Node: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].SessionID == nil ||
		*currentNodes[0].SessionID != cloneSessionID {
		t.Fatalf("branch Current Nodes = %+v, want clone Session %q", currentNodes, cloneSessionID)
	}
	if err := store.ValidateCurrentNodeSessionBinding(ctx, cloneSessionID, branchReference); err != nil {
		t.Fatalf("validate clone Session binding: %v", err)
	}
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    cloneSessionID,
			CurrentNode:  branchReference,
			AssociatedAt: time.UnixMilli(1_700_000_002_000).UTC(),
		},
		ExpectedCurrentSessionID: &sourceSessionID,
	}); err != nil {
		t.Fatalf("repeat fan-out clone binding: %v", err)
	}

	staleCloneSessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("parse stale clone Session ID: %v", err)
	}
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    staleCloneSessionID,
			CurrentNode:  branchReference,
			AssociatedAt: time.UnixMilli(1_700_000_003_000).UTC(),
		},
		ExpectedCurrentSessionID: &sourceSessionID,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale fan-out replacement error = %v, want sql.ErrNoRows", err)
	}
	currentNodes, err = store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("list branch Current Node after stale replacement: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].SessionID == nil ||
		*currentNodes[0].SessionID != cloneSessionID {
		t.Fatalf("branch Current Nodes after stale replacement = %+v, want clone Session %q", currentNodes, cloneSessionID)
	}
}

func TestResolveCurrentSessionStartContextTreatsRetainedNonCurrentSessionAsOrdinary(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  started.Mutation.Created[0].Reference,
		AssociatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AssociateTaskSession: %v", err)
	}

	_, err = store.ResolveCurrentSessionStartContext(ctx, sessionID)
	if !errors.Is(err, ErrSessionNotCurrentWorkflowNode) {
		t.Fatalf("ResolveCurrentSessionStartContext error = %v, want retained non-current absence", err)
	}
	if err := store.ValidateCurrentNodeSessionBinding(ctx, sessionID, started.Mutation.Created[0].Reference); !errors.Is(err, ErrSessionNotCurrentWorkflowNode) {
		t.Fatalf("ValidateCurrentNodeSessionBinding error = %v, want retained non-current absence", err)
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

func TestLoadSessionReuseAssociationsUsesExistingSerialAndBranchLookups(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	serialReference := started.Reference
	branchA := workflow.TransitionBranchKey("branch-a")
	branchB := workflow.TransitionBranchKey("branch-b")
	branchAReference, err := workflow.NewCurrentNodeReference(task.ID, serialReference.NodeID, &branchA)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference branch A: %v", err)
	}
	branchBReference, err := workflow.NewCurrentNodeReference(task.ID, serialReference.NodeID, &branchB)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference branch B: %v", err)
	}
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	for _, reference := range []workflow.CurrentNodeReference{serialReference, branchAReference, branchBReference} {
		if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  reference,
			AssociatedAt: associatedAt,
		}); err != nil {
			t.Fatalf("AssociateTaskSession %v: %v", reference, err)
		}
	}

	associations, err := store.LoadSessionReuseAssociations(ctx, []workflow.CurrentNodeReference{
		serialReference,
		branchAReference,
		branchBReference,
	})
	if err != nil {
		t.Fatalf("LoadSessionReuseAssociations: %v", err)
	}
	if len(associations) != 3 {
		t.Fatalf("association count = %d, want 3", len(associations))
	}
	for _, want := range []workflow.CurrentNodeReference{serialReference, branchAReference, branchBReference} {
		found := false
		for _, association := range associations {
			if association.CurrentNode.Equal(want) && association.SessionID == sessionID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing retained association for %v: %+v", want, associations)
		}
	}
}

func TestLoadSessionReuseAssociationsTreatsMissingReferencesAsNormalWithoutDiagnostics(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	branchKey := workflow.TransitionBranchKey("missing")
	branchReference, err := workflow.NewCurrentNodeReference(
		task.ID,
		started.Reference.NodeID,
		&branchKey,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}

	diagnostics := testsetup.CaptureSlog(t)

	associations, err := store.LoadSessionReuseAssociations(
		metadata.WithQueryFailureDiagnostics(ctx),
		[]workflow.CurrentNodeReference{started.Reference, branchReference},
	)
	if err != nil {
		t.Fatalf("LoadSessionReuseAssociations: %v", err)
	}
	if len(associations) != 0 {
		t.Fatalf("missing retained associations = %+v, want none", associations)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("missing retained association diagnostics = %q, want none", diagnostics.String())
	}
}

func TestLoadSessionReuseAssociationsRetainsBranchVisitAfterJoinCycle(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	laterNodeID := started.Reference.NodeID
	for _, node := range definition.Nodes {
		if node.Kind() == workflow.NodeKindAgent && workflow.NodeIDOf(node) != laterNodeID {
			laterNodeID = workflow.NodeIDOf(node)
			break
		}
	}
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	branchKey := workflow.TransitionBranchKey("implementation")
	branchBeforeJoin, err := workflow.NewCurrentNodeReference(task.ID, started.Reference.NodeID, &branchKey)
	if err != nil {
		t.Fatalf("branch before Join reference: %v", err)
	}
	branchAfterJoin, err := workflow.NewCurrentNodeReference(task.ID, laterNodeID, &branchKey)
	if err != nil {
		t.Fatalf("branch after Join reference: %v", err)
	}
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	for _, reference := range []workflow.CurrentNodeReference{branchBeforeJoin, branchAfterJoin} {
		if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  reference,
			AssociatedAt: associatedAt,
		}); err != nil {
			t.Fatalf("AssociateTaskSession %v: %v", reference, err)
		}
	}

	associations, err := store.LoadSessionReuseAssociations(ctx, []workflow.CurrentNodeReference{
		branchBeforeJoin,
		branchAfterJoin,
	})
	if err != nil {
		t.Fatalf("LoadSessionReuseAssociations after Join cycle: %v", err)
	}
	if len(associations) != 2 {
		t.Fatalf("association count = %d, want 2 retained branch visits", len(associations))
	}
	for _, want := range []workflow.CurrentNodeReference{branchBeforeJoin, branchAfterJoin} {
		found := false
		for _, association := range associations {
			if association.CurrentNode.Equal(want) && association.SessionID == sessionID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing retained branch visit %v: %+v", want, associations)
		}
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

func TestEnsureCurrentNodeSessionAssociationRepairsMissingProvenanceIdempotently(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	currentNode := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	sessionID := associateAndBindCurrentNodeSessionForTest(
		t,
		ctx,
		store,
		binding,
		cfg,
		currentNode.Reference,
	)
	if _, err := store.db.ExecContext(
		ctx,
		`DELETE FROM session_workflow_node_associations
WHERE session_id = ? AND node_id = ? AND transition_branch_key IS NULL`,
		sessionID.String(),
		currentNode.Reference.NodeID,
	); err != nil {
		t.Fatalf("delete exact Session provenance: %v", err)
	}

	repaired, err := store.EnsureCurrentNodeSessionAssociation(ctx, sessionID)
	if err != nil {
		t.Fatalf("EnsureCurrentNodeSessionAssociation repair: %v", err)
	}
	repeated, err := store.EnsureCurrentNodeSessionAssociation(ctx, sessionID)
	if err != nil {
		t.Fatalf("EnsureCurrentNodeSessionAssociation repeat: %v", err)
	}
	if repaired != repeated {
		t.Fatalf("repeated ensure = %+v, want unchanged %+v", repeated, repaired)
	}
	if err := store.ValidateCurrentNodeSessionBinding(ctx, sessionID, currentNode.Reference); err != nil {
		t.Fatalf("repaired exact Session provenance: %v", err)
	}
}

func TestEnsureCurrentNodeSessionAssociationRejectsCrossTaskOwnershipWithoutChanges(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	ownerTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	otherTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	ownerCurrentNode := startTask(t, ctx, store, ownerTask.ID).Mutation.Created[0]
	otherCurrentNode := startTask(t, ctx, store, otherTask.ID).Mutation.Created[0]
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  ownerCurrentNode.Reference,
		AssociatedAt: time.UnixMilli(1_700_000_000_000).UTC(),
	}); err != nil {
		t.Fatalf("AssociateTaskSession owner: %v", err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`UPDATE task_current_nodes SET session_id = ?
WHERE task_id = ? AND node_id = ? AND transition_branch_key IS NULL`,
		sessionID.String(),
		otherTask.ID,
		otherCurrentNode.Reference.NodeID,
	); err != nil {
		t.Fatalf("seed cross-Task Current Node ownership: %v", err)
	}

	if _, err := store.EnsureCurrentNodeSessionAssociation(ctx, sessionID); err == nil {
		t.Fatal("EnsureCurrentNodeSessionAssociation accepted cross-Task ownership")
	}
	owner, err := store.TaskIDForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("TaskIDForSession: %v", err)
	}
	if owner == nil || *owner != ownerTask.ID {
		t.Fatalf("Session owner after rejection = %v, want %q", owner, ownerTask.ID)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, otherTask.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes other Task: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].SessionID == nil || *currentNodes[0].SessionID != sessionID {
		t.Fatalf("cross-Task Current Node changed after rejection: %+v", currentNodes)
	}
}

func TestEnsureCurrentNodeSessionAssociationRejectsMultipleCurrentNodesWithoutChanges(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	fanout, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		OutputValues: map[string]string{"summary": "multiple owner fixture"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode fan-out: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if _, err := store.AssociateTaskSession(ctx, TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  fanout.Mutation.Created[0].Reference,
		AssociatedAt: time.UnixMilli(1_700_000_000_000).UTC(),
	}); err != nil {
		t.Fatalf("AssociateTaskSession: %v", err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`UPDATE task_current_nodes SET session_id = ? WHERE task_id = ?`,
		sessionID.String(),
		task.ID,
	); err != nil {
		t.Fatalf("seed multiple Current Node ownership: %v", err)
	}

	if _, err := store.EnsureCurrentNodeSessionAssociation(ctx, sessionID); err == nil {
		t.Fatal("EnsureCurrentNodeSessionAssociation accepted multiple Current Nodes")
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 2 {
		t.Fatalf("Current Nodes after rejection = %+v, want two", currentNodes)
	}
	for _, currentNode := range currentNodes {
		if currentNode.SessionID == nil || *currentNode.SessionID != sessionID {
			t.Fatalf("Current Node changed after multiple-owner rejection: %+v", currentNode)
		}
	}
}

func TestPreparedTaskResumeRepairsMissingCurrentNodeSessionAssociationAtomically(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	currentNode := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	sessionID := associateAndBindCurrentNodeSessionForTest(t, ctx, store, binding, cfg, currentNode.Reference)
	if err := store.InterruptCurrentNode(
		ctx,
		currentNode.Reference,
		"test_interruption",
		workflow.NewCurrentNodeInterruptionDetail("test_interruption", nil),
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`DELETE FROM session_workflow_node_associations
WHERE session_id = ? AND node_id = ? AND transition_branch_key IS NULL`,
		sessionID.String(),
		currentNode.Reference.NodeID,
	); err != nil {
		t.Fatalf("delete exact Session provenance: %v", err)
	}

	prepared, err := store.PrepareTaskResume(ctx, task.ID)
	if err != nil {
		t.Fatalf("PrepareTaskResume: %v", err)
	}
	if err := prepared.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := store.ValidateCurrentNodeSessionBinding(ctx, sessionID, currentNode.Reference); err != nil {
		t.Fatalf("Resume did not repair exact Session provenance: %v", err)
	}
	resumed, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(resumed) != 1 || resumed[0].Scheduling == nil ||
		resumed[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("resumed Current Node = %+v, want ready", resumed)
	}
}

func TestPreparedTaskResumeCancellationKeepsMissingAssociationAndInterruption(t *testing.T) {
	parent, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, parent, store)
	linkWorkflow(t, parent, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, parent, store, binding.ProjectID)
	currentNode := startTask(t, parent, store, task.ID).Mutation.Created[0]
	sessionID := associateAndBindCurrentNodeSessionForTest(t, parent, store, binding, cfg, currentNode.Reference)
	if err := store.InterruptCurrentNode(
		parent,
		currentNode.Reference,
		"test_interruption",
		workflow.NewCurrentNodeInterruptionDetail("test_interruption", nil),
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	if _, err := store.db.ExecContext(
		parent,
		`DELETE FROM session_workflow_node_associations
WHERE session_id = ? AND node_id = ? AND transition_branch_key IS NULL`,
		sessionID.String(),
		currentNode.Reference.NodeID,
	); err != nil {
		t.Fatalf("delete exact Session provenance: %v", err)
	}
	ctx, cancel := context.WithCancel(parent)
	prepared, err := store.PrepareTaskResume(ctx, task.ID)
	if err != nil {
		t.Fatalf("PrepareTaskResume: %v", err)
	}
	cancel()
	if err := prepared.Commit(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit after cancellation = %v, want context cancellation", err)
	}
	if err := store.ValidateCurrentNodeSessionBinding(parent, sessionID, currentNode.Reference); !errors.Is(err, ErrSessionNotCurrentWorkflowNode) {
		t.Fatalf("canceled Resume provenance = %v, want still missing", err)
	}
	currentNodes, err := store.ListCurrentNodes(parent, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("Current Node after canceled Resume = %+v, want interrupted", currentNodes)
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
