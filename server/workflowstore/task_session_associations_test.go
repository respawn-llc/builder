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
	"core/shared/runtimeids"
)

func requireCurrentBindingAssociation(
	t *testing.T,
	authority CurrentNodeSessionBindingAuthority,
) TaskSessionAssociation {
	t.Helper()
	association, ok := authority.CurrentAssociation()
	if !ok {
		t.Fatalf("binding authority = %q, want exact current association", authority.Kind())
	}
	return association
}

func TestBindSessionToCurrentNodeBindsFreshSessionToTaskAndCurrentNode(t *testing.T) {
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

	authority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  started.Mutation.Created[0].Reference,
			AssociatedAt: associatedAt,
		},
	})
	if err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}
	association := requireCurrentBindingAssociation(t, authority)
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
	current, err := store.CurrentTaskSessionForNode(ctx, started.Mutation.Created[0].Reference)
	if err != nil {
		t.Fatalf("CurrentTaskSessionForNode: %v", err)
	}
	if current != association {
		t.Fatalf("current node association = %+v, want %+v", current, association)
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

	authority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  started.Mutation.Created[0].Reference,
			AssociatedAt: time.UnixMilli(1_700_000_000_000).UTC(),
		},
	})
	if err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}
	association := requireCurrentBindingAssociation(t, authority)

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
	if association.SourceSessionID != sessionID {
		t.Fatalf("live binding source = %q, want self source %q", association.SourceSessionID, sessionID)
	}
	sourceSessionID, exact := currentNodes[0].ContinuationSource.ExactSessionID()
	if !exact || sourceSessionID != sessionID {
		t.Fatalf("Current Node source = %q, %v; want %q, true", sourceSessionID, exact, sessionID)
	}
	current, err := store.CurrentTaskSessionForNode(ctx, started.Mutation.Created[0].Reference)
	if err != nil {
		t.Fatalf("CurrentTaskSessionForNode: %v", err)
	}
	if current.SessionID != sessionID {
		t.Fatalf("current association = %+v, want %q", current, sessionID)
	}
	if count, err := store.CountTaskSessions(ctx, task.ID); err != nil || count != 1 {
		t.Fatalf("CountTaskSessions = %d, %v, want 1", count, err)
	}
	if err := store.ValidateCurrentNodeSessionBinding(ctx, sessionID, started.Mutation.Created[0].Reference); err != nil {
		t.Fatalf("ValidateCurrentNodeSessionBinding: %v", err)
	}
	startupAuthority, err := store.ResolveCurrentNodeSessionBindingAuthority(ctx, sessionID, started.Mutation.Created[0].Reference)
	if err != nil {
		t.Fatalf("ResolveCurrentNodeSessionBindingAuthority: %v", err)
	}
	if startupAuthority.Kind() != CurrentNodeSessionBindingAuthorityExactCurrent {
		t.Fatalf("startup authority = %q, want %q", startupAuthority.Kind(), CurrentNodeSessionBindingAuthorityExactCurrent)
	}
}

func TestBindSessionToCurrentNodeReplacesCurrentTupleAndRetainsHistoricalProvenance(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	firstSessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID first: %v", err)
	}
	replacementSessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID replacement: %v", err)
	}
	hasHistorical, err := store.HasHistoricalTaskSessionForNode(ctx, started.Reference)
	if err != nil {
		t.Fatalf("HasHistoricalTaskSessionForNode before binding: %v", err)
	}
	if hasHistorical {
		t.Fatal("unbound Current Node unexpectedly has historical Session provenance")
	}
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    firstSessionID,
			CurrentNode:  started.Reference,
			AssociatedAt: time.UnixMilli(1_700_000_000_000).UTC(),
		},
	}); err != nil {
		t.Fatalf("bind first Session: %v", err)
	}
	replacedAuthority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    replacementSessionID,
			CurrentNode:  started.Reference,
			AssociatedAt: time.UnixMilli(1_700_000_001_000).UTC(),
		},
		ExpectedCurrentSessionID: &firstSessionID,
	})
	if err != nil {
		t.Fatalf("replace bound Session: %v", err)
	}
	replaced := requireCurrentBindingAssociation(t, replacedAuthority)
	if replaced.SessionID != replacementSessionID || replaced.SourceSessionID != firstSessionID {
		t.Fatalf("replacement tuple = (%q, %q), want (%q, %q)",
			replaced.SessionID,
			replaced.SourceSessionID,
			replacementSessionID,
			firstSessionID,
		)
	}
	current, err := store.CurrentTaskSessionForNode(ctx, started.Reference)
	if err != nil {
		t.Fatalf("CurrentTaskSessionForNode: %v", err)
	}
	if current.SessionID != replacementSessionID || current.SourceSessionID != firstSessionID {
		t.Fatalf("current tuple = (%q, %q), want (%q, %q)",
			current.SessionID,
			current.SourceSessionID,
			replacementSessionID,
			firstSessionID,
		)
	}
	hasHistorical, err = store.HasHistoricalTaskSessionForNode(ctx, started.Reference)
	if err != nil {
		t.Fatalf("HasHistoricalTaskSessionForNode after replacement: %v", err)
	}
	if !hasHistorical {
		t.Fatal("replaced Session provenance was not retained as historical")
	}
}

func TestBindSessionToCurrentNodeRetiresTransitiveDependenciesOfSupersededSource(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewNodeID := workflow.NodeIDOf(nodeByKey(t, definition, "review"))
	auditNodeID := workflow.NodeIDOf(nodeByKey(t, definition, "audit"))
	sessionIDs := make([]runtimeids.SessionID, 4)
	for index := range sessionIDs {
		sessionIDs[index], err = runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
		if err != nil {
			t.Fatalf("ParseSessionID %d: %v", index, err)
		}
	}
	sourceA, dependentR, transitiveX, replacementB := sessionIDs[0], sessionIDs[1], sessionIDs[2], sessionIDs[3]
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sourceA,
			CurrentNode:  started.Reference,
			AssociatedAt: associatedAt,
		},
	}); err != nil {
		t.Fatalf("bind source A: %v", err)
	}
	sourceAContinuation, err := workflow.NewExactMaterializedContinuationSource(sourceA)
	if err != nil {
		t.Fatalf("create source A continuation: %v", err)
	}
	reviewReference := replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		reviewNodeID,
		&sourceA,
		sourceAContinuation,
	)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    dependentR,
			CurrentNode:  reviewReference,
			AssociatedAt: associatedAt.Add(time.Millisecond),
		},
		ExpectedCurrentSessionID: &sourceA,
	}); err != nil {
		t.Fatalf("bind dependent R: %v", err)
	}
	dependentRContinuation, err := workflow.NewExactMaterializedContinuationSource(dependentR)
	if err != nil {
		t.Fatalf("create dependent R continuation: %v", err)
	}
	auditReference := replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		auditNodeID,
		&dependentR,
		dependentRContinuation,
	)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    transitiveX,
			CurrentNode:  auditReference,
			AssociatedAt: associatedAt.Add(2 * time.Millisecond),
		},
		ExpectedCurrentSessionID: &dependentR,
	}); err != nil {
		t.Fatalf("bind transitive X: %v", err)
	}
	replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		started.Reference.NodeID,
		nil,
		workflow.DeferredSelfMaterializedContinuationSource(),
	)
	replacementAuthority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    replacementB,
			CurrentNode:  started.Reference,
			AssociatedAt: associatedAt.Add(3 * time.Millisecond),
		},
	})
	if err != nil {
		t.Fatalf("bind replacement B: %v", err)
	}
	replacement := requireCurrentBindingAssociation(t, replacementAuthority)
	if replacement.SessionID != replacementB || replacement.SourceSessionID != replacementB {
		t.Fatalf("replacement tuple = (%q, %q), want (%q, %q)",
			replacement.SessionID,
			replacement.SourceSessionID,
			replacementB,
			replacementB,
		)
	}
	for _, retired := range []workflow.CurrentNodeReference{reviewReference, auditReference} {
		if _, err := store.CurrentTaskSessionForNode(ctx, retired); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("current association for retired dependency %v = %v, want sql.ErrNoRows", retired, err)
		}
		hasHistorical, err := store.HasHistoricalTaskSessionForNode(ctx, retired)
		if err != nil {
			t.Fatalf("HasHistoricalTaskSessionForNode %v: %v", retired, err)
		}
		if !hasHistorical {
			t.Fatalf("retired dependency %v omitted historical provenance", retired)
		}
	}
}

func TestBindSessionToCurrentNodeRetiresDisplacedRetainedDependenciesAndPreservesSource(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewNodeID := workflow.NodeIDOf(nodeByKey(t, definition, "review"))
	auditNodeID := workflow.NodeIDOf(nodeByKey(t, definition, "audit"))
	sessionIDs := make([]runtimeids.SessionID, 4)
	for index := range sessionIDs {
		sessionIDs[index], err = runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
		if err != nil {
			t.Fatalf("ParseSessionID %d: %v", index, err)
		}
	}
	sourceS, retainedR, dependentD, replacementC := sessionIDs[0], sessionIDs[1], sessionIDs[2], sessionIDs[3]
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sourceS, CurrentNode: started.Reference, AssociatedAt: associatedAt,
		},
	}); err != nil {
		t.Fatalf("bind source S: %v", err)
	}
	sourceContinuation, err := workflow.NewExactMaterializedContinuationSource(sourceS)
	if err != nil {
		t.Fatalf("create source continuation: %v", err)
	}
	reviewReference := replaceSerialCurrentNodeBindingFixture(t, ctx, store, started, reviewNodeID, &sourceS, sourceContinuation)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: retainedR, CurrentNode: reviewReference, AssociatedAt: associatedAt.Add(time.Millisecond),
		},
		ExpectedCurrentSessionID: &sourceS,
	}); err != nil {
		t.Fatalf("bind retained R: %v", err)
	}
	retainedContinuation, err := workflow.NewExactMaterializedContinuationSource(retainedR)
	if err != nil {
		t.Fatalf("create retained continuation: %v", err)
	}
	auditReference := replaceSerialCurrentNodeBindingFixture(t, ctx, store, started, auditNodeID, &retainedR, retainedContinuation)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: dependentD, CurrentNode: auditReference, AssociatedAt: associatedAt.Add(2 * time.Millisecond),
		},
		ExpectedCurrentSessionID: &retainedR,
	}); err != nil {
		t.Fatalf("bind dependent D: %v", err)
	}
	replaceSerialCurrentNodeBindingFixture(t, ctx, store, started, reviewNodeID, &retainedR, sourceContinuation)
	replacementAuthority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: replacementC, CurrentNode: reviewReference, AssociatedAt: associatedAt.Add(3 * time.Millisecond),
		},
		ExpectedCurrentSessionID: &retainedR,
	})
	if err != nil {
		t.Fatalf("replace retained R: %v", err)
	}
	replacement := requireCurrentBindingAssociation(t, replacementAuthority)
	if replacement.SessionID != replacementC || replacement.SourceSessionID != sourceS {
		t.Fatalf("replacement tuple = (%q, %q), want (%q, %q)",
			replacement.SessionID, replacement.SourceSessionID, replacementC, sourceS)
	}
	sourceAssociation, err := store.CurrentTaskSessionForNode(ctx, started.Reference)
	if err != nil {
		t.Fatalf("source association: %v", err)
	}
	if sourceAssociation.SessionID != sourceS || sourceAssociation.SourceSessionID != sourceS {
		t.Fatalf("preserved source tuple = (%q, %q), want (%q, %q)",
			sourceAssociation.SessionID, sourceAssociation.SourceSessionID, sourceS, sourceS)
	}
	if _, err := store.CurrentTaskSessionForNode(ctx, auditReference); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("dependent association after replacement = %v, want sql.ErrNoRows", err)
	}
}

func TestBindSessionToCurrentNodePreservesSourceWhenCloneDisplacesSelfRetainedTuple(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	sourceS, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID source: %v", err)
	}
	cloneC, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID clone: %v", err)
	}
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sourceS, CurrentNode: started.Reference, AssociatedAt: associatedAt,
		},
	}); err != nil {
		t.Fatalf("bind source S: %v", err)
	}
	sourceContinuation, err := workflow.NewExactMaterializedContinuationSource(sourceS)
	if err != nil {
		t.Fatalf("create source continuation: %v", err)
	}
	reviewReference := replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		workflow.NodeIDOf(nodeByKey(t, definition, "review")),
		&sourceS,
		sourceContinuation,
	)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sourceS, CurrentNode: reviewReference, AssociatedAt: associatedAt.Add(time.Millisecond),
		},
		ExpectedCurrentSessionID: &sourceS,
	}); err != nil {
		t.Fatalf("bind self-retained target: %v", err)
	}
	replacementAuthority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: cloneC, CurrentNode: reviewReference, AssociatedAt: associatedAt.Add(2 * time.Millisecond),
		},
		ExpectedCurrentSessionID: &sourceS,
	})
	if err != nil {
		t.Fatalf("bind clone C: %v", err)
	}
	replacement := requireCurrentBindingAssociation(t, replacementAuthority)
	if replacement.SessionID != cloneC || replacement.SourceSessionID != sourceS {
		t.Fatalf("clone tuple = (%q, %q), want (%q, %q)",
			replacement.SessionID, replacement.SourceSessionID, cloneC, sourceS)
	}
	sourceAssociation, err := store.CurrentTaskSessionForNode(ctx, started.Reference)
	if err != nil {
		t.Fatalf("source association: %v", err)
	}
	if sourceAssociation.SessionID != sourceS || sourceAssociation.SourceSessionID != sourceS {
		t.Fatalf("preserved source tuple = (%q, %q), want (%q, %q)",
			sourceAssociation.SessionID, sourceAssociation.SourceSessionID, sourceS, sourceS)
	}
}

func TestBindSessionToCurrentNodeRetiresCloneDependenciesWhenSourceBecomesRetainedAgain(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewNodeID := workflow.NodeIDOf(nodeByKey(t, definition, "review"))
	auditNodeID := workflow.NodeIDOf(nodeByKey(t, definition, "audit"))
	sessionIDs := make([]runtimeids.SessionID, 3)
	for index := range sessionIDs {
		sessionIDs[index], err = runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
		if err != nil {
			t.Fatalf("ParseSessionID %d: %v", index, err)
		}
	}
	sourceS, cloneC, dependentD := sessionIDs[0], sessionIDs[1], sessionIDs[2]
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sourceS, CurrentNode: started.Reference, AssociatedAt: associatedAt,
		},
	}); err != nil {
		t.Fatalf("bind source S: %v", err)
	}
	sourceContinuation, err := workflow.NewExactMaterializedContinuationSource(sourceS)
	if err != nil {
		t.Fatalf("create source continuation: %v", err)
	}
	reviewReference := replaceSerialCurrentNodeBindingFixture(t, ctx, store, started, reviewNodeID, &sourceS, sourceContinuation)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: cloneC, CurrentNode: reviewReference, AssociatedAt: associatedAt.Add(time.Millisecond),
		},
		ExpectedCurrentSessionID: &sourceS,
	}); err != nil {
		t.Fatalf("bind clone C: %v", err)
	}
	cloneContinuation, err := workflow.NewExactMaterializedContinuationSource(cloneC)
	if err != nil {
		t.Fatalf("create clone continuation: %v", err)
	}
	auditReference := replaceSerialCurrentNodeBindingFixture(t, ctx, store, started, auditNodeID, &cloneC, cloneContinuation)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: dependentD, CurrentNode: auditReference, AssociatedAt: associatedAt.Add(2 * time.Millisecond),
		},
		ExpectedCurrentSessionID: &cloneC,
	}); err != nil {
		t.Fatalf("bind dependent D: %v", err)
	}
	replaceSerialCurrentNodeBindingFixture(t, ctx, store, started, reviewNodeID, &cloneC, sourceContinuation)
	replacementAuthority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sourceS, CurrentNode: reviewReference, AssociatedAt: associatedAt.Add(3 * time.Millisecond),
		},
		ExpectedCurrentSessionID: &cloneC,
	})
	if err != nil {
		t.Fatalf("restore source S as retained Session: %v", err)
	}
	replacement := requireCurrentBindingAssociation(t, replacementAuthority)
	if replacement.SessionID != sourceS || replacement.SourceSessionID != sourceS {
		t.Fatalf("restored tuple = (%q, %q), want (%q, %q)",
			replacement.SessionID, replacement.SourceSessionID, sourceS, sourceS)
	}
	if _, err := store.CurrentTaskSessionForNode(ctx, auditReference); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("clone-dependent association after restoration = %v, want sql.ErrNoRows", err)
	}
	sourceAssociation, err := store.CurrentTaskSessionForNode(ctx, started.Reference)
	if err != nil {
		t.Fatalf("source association: %v", err)
	}
	if sourceAssociation.SessionID != sourceS {
		t.Fatalf("preserved source association = %+v, want Session %q", sourceAssociation, sourceS)
	}
}

func TestBindSessionToCurrentNodeRebindingSameTupleDoesNotCreateHistoricalProvenance(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	firstAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sessionID, CurrentNode: started.Reference, AssociatedAt: firstAt,
		},
	}); err != nil {
		t.Fatalf("bind tuple: %v", err)
	}
	reboundAuthority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sessionID, CurrentNode: started.Reference, AssociatedAt: firstAt.Add(time.Second),
		},
	})
	if err != nil {
		t.Fatalf("rebind same tuple: %v", err)
	}
	rebound := requireCurrentBindingAssociation(t, reboundAuthority)
	if rebound.SessionID != sessionID || rebound.SourceSessionID != sessionID {
		t.Fatalf("rebound tuple = (%q, %q), want (%q, %q)",
			rebound.SessionID, rebound.SourceSessionID, sessionID, sessionID)
	}
	hasHistorical, err := store.HasHistoricalTaskSessionForNode(ctx, started.Reference)
	if err != nil {
		t.Fatalf("HasHistoricalTaskSessionForNode: %v", err)
	}
	if hasHistorical {
		t.Fatal("same-tuple rebind created historical provenance")
	}
}

func TestBindSessionToCurrentNodeDependencyCycleTerminates(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewNodeID := workflow.NodeIDOf(nodeByKey(t, definition, "review"))
	sessionIDs := make([]runtimeids.SessionID, 3)
	for index := range sessionIDs {
		sessionIDs[index], err = runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
		if err != nil {
			t.Fatalf("ParseSessionID %d: %v", index, err)
		}
	}
	sessionB, sessionA, replacementC := sessionIDs[0], sessionIDs[1], sessionIDs[2]
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sessionB, CurrentNode: started.Reference, AssociatedAt: associatedAt,
		},
	}); err != nil {
		t.Fatalf("bind B self tuple: %v", err)
	}
	sourceB, err := workflow.NewExactMaterializedContinuationSource(sessionB)
	if err != nil {
		t.Fatalf("create B continuation: %v", err)
	}
	reviewReference := replaceSerialCurrentNodeBindingFixture(t, ctx, store, started, reviewNodeID, &sessionB, sourceB)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sessionA, CurrentNode: reviewReference, AssociatedAt: associatedAt.Add(time.Millisecond),
		},
		ExpectedCurrentSessionID: &sessionB,
	}); err != nil {
		t.Fatalf("bind A from B: %v", err)
	}
	sourceA, err := workflow.NewExactMaterializedContinuationSource(sessionA)
	if err != nil {
		t.Fatalf("create A continuation: %v", err)
	}
	replaceSerialCurrentNodeBindingFixture(t, ctx, store, started, started.Reference.NodeID, &sessionB, sourceA)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sessionB, CurrentNode: started.Reference, AssociatedAt: associatedAt.Add(2 * time.Millisecond),
		},
		ExpectedCurrentSessionID: &sessionB,
	}); err != nil {
		t.Fatalf("bind B from A: %v", err)
	}
	replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		reviewNodeID,
		nil,
		workflow.DeferredSelfMaterializedContinuationSource(),
	)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: replacementC, CurrentNode: reviewReference, AssociatedAt: associatedAt.Add(3 * time.Millisecond),
		},
	}); err != nil {
		t.Fatalf("replace cycle root with C: %v", err)
	}
	if _, err := store.CurrentTaskSessionForNode(ctx, started.Reference); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("B side of dependency cycle remains current: %v", err)
	}
	current, err := store.CurrentTaskSessionForNode(ctx, reviewReference)
	if err != nil {
		t.Fatalf("replacement association: %v", err)
	}
	if current.SessionID != replacementC || current.SourceSessionID != replacementC {
		t.Fatalf("replacement tuple = (%q, %q), want (%q, %q)",
			current.SessionID, current.SourceSessionID, replacementC, replacementC)
	}
}

func TestBindSessionToCurrentNodeRollsBackCurrentNodeTupleAndCascadeOnDesignationFailure(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	sessionIDs := make([]runtimeids.SessionID, 3)
	for index := range sessionIDs {
		sessionIDs[index], err = runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
		if err != nil {
			t.Fatalf("ParseSessionID %d: %v", index, err)
		}
	}
	sourceA, dependentR, replacementB := sessionIDs[0], sessionIDs[1], sessionIDs[2]
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sourceA, CurrentNode: started.Reference, AssociatedAt: associatedAt,
		},
	}); err != nil {
		t.Fatalf("bind source A: %v", err)
	}
	sourceContinuation, err := workflow.NewExactMaterializedContinuationSource(sourceA)
	if err != nil {
		t.Fatalf("create source continuation: %v", err)
	}
	reviewReference := replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		workflow.NodeIDOf(nodeByKey(t, definition, "review")),
		&sourceA,
		sourceContinuation,
	)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: dependentR, CurrentNode: reviewReference, AssociatedAt: associatedAt.Add(time.Millisecond),
		},
		ExpectedCurrentSessionID: &sourceA,
	}); err != nil {
		t.Fatalf("bind dependent R: %v", err)
	}
	replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		started.Reference.NodeID,
		nil,
		workflow.DeferredSelfMaterializedContinuationSource(),
	)
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER fail_current_association_designation
BEFORE INSERT ON session_workflow_node_associations
FOR EACH ROW
WHEN NEW.association_status = 'current'
BEGIN
    SELECT RAISE(ABORT, 'forced designation failure');
END`); err != nil {
		t.Fatalf("install designation failure trigger: %v", err)
	}
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: replacementB, CurrentNode: started.Reference, AssociatedAt: associatedAt.Add(2 * time.Millisecond),
		},
	}); err == nil {
		t.Fatal("binding succeeded despite forced designation failure")
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].SessionID != nil ||
		currentNodes[0].ContinuationSource.Kind() != workflow.MaterializedContinuationSourceDeferredSelf {
		t.Fatalf("Current Node after rollback = %+v, want unbound deferred-self replacement", currentNodes)
	}
	for _, want := range []struct {
		reference workflow.CurrentNodeReference
		sessionID runtimeids.SessionID
	}{
		{reference: started.Reference, sessionID: sourceA},
		{reference: reviewReference, sessionID: dependentR},
	} {
		association, err := store.CurrentTaskSessionForNode(ctx, want.reference)
		if err != nil {
			t.Fatalf("current association %v after rollback: %v", want.reference, err)
		}
		if association.SessionID != want.sessionID {
			t.Fatalf("current association %v = %+v, want Session %q", want.reference, association, want.sessionID)
		}
	}
	owner, err := store.TaskIDForSession(ctx, replacementB)
	if err != nil {
		t.Fatalf("TaskIDForSession replacement: %v", err)
	}
	if owner != nil {
		t.Fatalf("replacement Session owner after rollback = %q, want none", *owner)
	}
}

func TestMaterializedDeferredSelfReplacementDoesNotSupersedeBeforeBinding(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	sessionA, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionA,
			CurrentNode:  started.Reference,
			AssociatedAt: time.UnixMilli(1_700_000_000_000).UTC(),
		},
	}); err != nil {
		t.Fatalf("bind Session A: %v", err)
	}
	replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		started.Reference.NodeID,
		nil,
		workflow.DeferredSelfMaterializedContinuationSource(),
	)
	current, err := store.CurrentTaskSessionForNode(ctx, started.Reference)
	if err != nil {
		t.Fatalf("CurrentTaskSessionForNode after materialization: %v", err)
	}
	if current.SessionID != sessionA || current.SourceSessionID != sessionA {
		t.Fatalf("current tuple after unbound replacement = (%q, %q), want (%q, %q)",
			current.SessionID, current.SourceSessionID, sessionA, sessionA)
	}
}

func TestBindSessionToLegacyCurrentNodeAppendsHistoricalOnlyForSameAndClone(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	sessionIDs := make([]runtimeids.SessionID, 3)
	for index := range sessionIDs {
		sessionIDs[index], err = runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
		if err != nil {
			t.Fatalf("ParseSessionID %d: %v", index, err)
		}
	}
	sourceS, dependentR, cloneC := sessionIDs[0], sessionIDs[1], sessionIDs[2]
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sourceS, CurrentNode: started.Reference, AssociatedAt: associatedAt,
		},
	}); err != nil {
		t.Fatalf("bind source S: %v", err)
	}
	sourceContinuation, err := workflow.NewExactMaterializedContinuationSource(sourceS)
	if err != nil {
		t.Fatalf("create source continuation: %v", err)
	}
	reviewReference := replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		started,
		workflow.NodeIDOf(nodeByKey(t, definition, "review")),
		&sourceS,
		sourceContinuation,
	)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: dependentR, CurrentNode: reviewReference, AssociatedAt: associatedAt.Add(time.Millisecond),
		},
		ExpectedCurrentSessionID: &sourceS,
	}); err != nil {
		t.Fatalf("bind dependent R: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE session_workflow_node_associations
SET association_status = 'historical', source_session_id = NULL
WHERE task_id = ?
  AND node_id = ?
  AND transition_branch_key IS NULL`,
		string(task.ID),
		string(started.Reference.NodeID),
	); err != nil {
		t.Fatalf("mark migrated source association historical: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE task_current_nodes
SET
    node_id = ?,
    session_id = ?,
    continuation_source_kind = NULL,
    continuation_source_session_id = NULL,
    legacy_materialized = 1
WHERE task_id = ?
  AND transition_branch_key IS NULL`,
		string(started.Reference.NodeID),
		sourceS.String(),
		string(task.ID),
	); err != nil {
		t.Fatalf("materialize migrated legacy Current Node: %v", err)
	}
	sameAuthority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: sourceS, CurrentNode: started.Reference, AssociatedAt: associatedAt.Add(2 * time.Millisecond),
		},
	})
	if err != nil {
		t.Fatalf("bind same legacy Session: %v", err)
	}
	if sameAuthority.Kind() != CurrentNodeSessionBindingAuthorityLegacyHistorical {
		t.Fatalf("same legacy binding authority = %q", sameAuthority.Kind())
	}
	cloneAuthority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID: cloneC, CurrentNode: started.Reference, AssociatedAt: associatedAt.Add(3 * time.Millisecond),
		},
		ExpectedCurrentSessionID: &sourceS,
	})
	if err != nil {
		t.Fatalf("bind legacy clone: %v", err)
	}
	if cloneAuthority.Kind() != CurrentNodeSessionBindingAuthorityLegacyHistorical {
		t.Fatalf("clone legacy binding authority = %q", cloneAuthority.Kind())
	}
	if _, err := store.CurrentTaskSessionForNode(ctx, started.Reference); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("legacy target current association = %v, want sql.ErrNoRows", err)
	}
	dependent, err := store.CurrentTaskSessionForNode(ctx, reviewReference)
	if err != nil {
		t.Fatalf("dependent association after legacy clone: %v", err)
	}
	if dependent.SessionID != dependentR || dependent.SourceSessionID != sourceS {
		t.Fatalf("dependent tuple = (%q, %q), want (%q, %q)",
			dependent.SessionID, dependent.SourceSessionID, dependentR, sourceS)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].SessionID == nil ||
		*currentNodes[0].SessionID != cloneC ||
		currentNodes[0].ContinuationSource.Kind() != workflow.MaterializedContinuationSourceLegacy {
		t.Fatalf("legacy Current Node after clone = %+v", currentNodes)
	}
	if hasHistorical, err := store.HasHistoricalTaskSessionForNode(ctx, started.Reference); err != nil || !hasHistorical {
		t.Fatalf("legacy historical provenance = %t, %v, want true", hasHistorical, err)
	}
}

func TestLegacyAgentStartupAuthorityAcceptsReadyAdmittedAndInterruptedStates(t *testing.T) {
	for _, state := range []workflow.CurrentNodeSchedulingState{
		workflow.CurrentNodeSchedulingReady,
		workflow.CurrentNodeSchedulingAdmitted,
		workflow.CurrentNodeSchedulingInterrupted,
	} {
		t.Run(string(state), func(t *testing.T) {
			ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
			workflowID := createValidWorkflow(t, ctx, store)
			linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
			task := createDefaultTask(t, ctx, store, binding.ProjectID)
			started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
			sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
			if err != nil {
				t.Fatalf("ParseSessionID: %v", err)
			}
			if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
				Association: TaskSessionAssociationRequest{
					SessionID:    sessionID,
					CurrentNode:  started.Reference,
					AssociatedAt: time.UnixMilli(1_700_000_000_000).UTC(),
				},
			}); err != nil {
				t.Fatalf("BindSessionToCurrentNode: %v", err)
			}
			if _, err := store.db.ExecContext(ctx, `
UPDATE session_workflow_node_associations
SET association_status = 'historical', source_session_id = NULL
WHERE task_id = ?
  AND node_id = ?
  AND transition_branch_key IS NULL`,
				string(task.ID),
				string(started.Reference.NodeID),
			); err != nil {
				t.Fatalf("mark migrated association historical: %v", err)
			}
			var (
				interruptionReason     sql.NullString
				interruptionDetail     sql.NullString
				interruptedAtUnixMilli sql.NullInt64
			)
			if state == workflow.CurrentNodeSchedulingInterrupted {
				interruptionReason = sql.NullString{String: "restart_recovery", Valid: true}
				interruptionDetail = sql.NullString{String: "{}", Valid: true}
				interruptedAtUnixMilli = sql.NullInt64{Int64: 1_700_000_001_000, Valid: true}
			}
			if _, err := store.db.ExecContext(ctx, `
UPDATE task_current_nodes
SET
    scheduling_state = ?,
    interruption_reason = ?,
    interruption_detail_json = ?,
    interrupted_at_unix_ms = ?,
    continuation_source_kind = NULL,
    continuation_source_session_id = NULL,
    legacy_materialized = 1
WHERE task_id = ?
  AND node_id = ?
  AND transition_branch_key IS NULL`,
				string(state),
				interruptionReason,
				interruptionDetail,
				interruptedAtUnixMilli,
				string(task.ID),
				string(started.Reference.NodeID),
			); err != nil {
				t.Fatalf("materialize migrated %s Current Node: %v", state, err)
			}
			startContext, err := store.ResolveCurrentSessionStartContext(ctx, sessionID)
			if err != nil {
				t.Fatalf("ResolveCurrentSessionStartContext: %v", err)
			}
			if startContext.CurrentNode.Scheduling == nil ||
				startContext.CurrentNode.Scheduling.State != state ||
				startContext.CurrentNode.ContinuationSource.Kind() != workflow.MaterializedContinuationSourceLegacy {
				t.Fatalf("legacy %s start context = %+v", state, startContext.CurrentNode)
			}
			authority, err := store.ResolveCurrentNodeSessionBindingAuthority(ctx, sessionID, started.Reference)
			if err != nil {
				t.Fatalf("ResolveCurrentNodeSessionBindingAuthority: %v", err)
			}
			if authority.Kind() != CurrentNodeSessionBindingAuthorityLegacyHistorical {
				t.Fatalf("legacy %s authority = %q", state, authority.Kind())
			}
			if _, err := store.CurrentTaskSessionForNode(ctx, started.Reference); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("legacy %s current association = %v, want sql.ErrNoRows", state, err)
			}
		})
	}
}

func replaceSerialCurrentNodeBindingFixture(
	t *testing.T,
	ctx context.Context,
	store *Store,
	template workflow.CurrentNode,
	nodeID workflow.NodeID,
	sessionID *runtimeids.SessionID,
	source workflow.MaterializedContinuationSource,
) workflow.CurrentNodeReference {
	t.Helper()
	sourceSessionID, exact := source.ExactSessionID()
	var persistedSessionID sql.NullString
	if sessionID != nil {
		persistedSessionID = sql.NullString{String: sessionID.String(), Valid: true}
	}
	var persistedSourceSessionID sql.NullString
	if exact {
		persistedSourceSessionID = sql.NullString{String: sourceSessionID.String(), Valid: true}
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE task_current_nodes
SET
    node_id = ?,
    session_id = ?,
    continuation_source_kind = ?,
    continuation_source_session_id = ?,
    legacy_materialized = 0
WHERE task_id = ?
  AND transition_branch_key IS NULL`,
		string(nodeID),
		persistedSessionID,
		string(source.Kind()),
		persistedSourceSessionID,
		string(template.Reference.TaskID),
	); err != nil {
		t.Fatalf("replace serial Current Node binding fixture: %v", err)
	}
	reference, err := workflow.NewCurrentNodeReference(template.Reference.TaskID, nodeID, nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	return reference
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
    effective_assignee, assignee_origin, continuation_source_kind,
    continuation_source_session_id, legacy_materialized
) VALUES (?, ?, ?, '{}', '{"transition_parameters":{}}', ?, 'ready', ?, ?, ?, 'exact', ?, 0)`,
		string(task.ID),
		string(started.Reference.NodeID),
		string(branchKey),
		sourceSessionID.String(),
		string(*started.EnteredByEdgeID),
		started.AgentExecutionSelection.Assignee,
		string(started.AgentExecutionSelection.Origin),
		sourceSessionID.String(),
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
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  started.Mutation.Created[0].Reference,
			AssociatedAt: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       started.Mutation.Created[0].Reference,
		TransitionID: "done",
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}

	_, err = store.ResolveCurrentSessionStartContext(ctx, sessionID)
	if !errors.Is(err, ErrSessionNotCurrentWorkflowNode) {
		t.Fatalf("ResolveCurrentSessionStartContext error = %v, want retained non-current absence", err)
	}
	if err := store.ValidateCurrentNodeSessionBinding(ctx, sessionID, started.Mutation.Created[0].Reference); !errors.Is(err, ErrSessionNotCurrentWorkflowNode) {
		t.Fatalf("ValidateCurrentNodeSessionBinding error = %v, want retained non-current absence", err)
	}
}

func TestBindSessionToCurrentNodeUpsertsRepeatedSerialAssociation(t *testing.T) {
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
	firstAuthority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  started.Mutation.Created[0].Reference,
			AssociatedAt: firstAt,
		},
	})
	if err != nil {
		t.Fatalf("first BindSessionToCurrentNode: %v", err)
	}
	first := requireCurrentBindingAssociation(t, firstAuthority)
	secondAuthority, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  started.Mutation.Created[0].Reference,
			AssociatedAt: secondAt,
		},
	})
	if err != nil {
		t.Fatalf("second BindSessionToCurrentNode: %v", err)
	}
	second := requireCurrentBindingAssociation(t, secondAuthority)
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

func TestLoadSessionReuseAssociationsUsesCurrentSerialAndBranchBindings(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	sourceSessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	associatedAt := time.UnixMilli(1_700_000_000_000).UTC()
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sourceSessionID,
			CurrentNode:  started.Reference,
			AssociatedAt: associatedAt,
		},
	}); err != nil {
		t.Fatalf("bind serial source Session: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM task_current_nodes WHERE task_id = ?`, string(task.ID)); err != nil {
		t.Fatalf("delete serial Current Node: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO task_active_fanouts (task_id) VALUES (?)`, string(task.ID)); err != nil {
		t.Fatalf("insert active fan-out: %v", err)
	}
	references := []workflow.CurrentNodeReference{started.Reference}
	sessionByReference := map[workflow.CurrentNodeReferenceKey]runtimeids.SessionID{}
	startedKey, err := started.Reference.Key()
	if err != nil {
		t.Fatalf("serial Current Node key: %v", err)
	}
	sessionByReference[startedKey] = sourceSessionID
	for index, branchKey := range []workflow.TransitionBranchKey{"branch-a", "branch-b"} {
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO task_active_fanout_branches (
    task_id, transition_branch_key, arrival_state, arrival_values_json,
    continuation_source_kind, continuation_source_session_id, legacy_materialized
) VALUES (?, ?, 'pending', NULL, 'exact', ?, 0)`,
			string(task.ID),
			string(branchKey),
			sourceSessionID.String(),
		); err != nil {
			t.Fatalf("insert active fan-out branch %q: %v", branchKey, err)
		}
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO task_current_nodes (
    task_id, node_id, transition_branch_key, current_input_values_json,
    prior_node_values_json, session_id, scheduling_state, entered_by_edge_id,
    effective_assignee, assignee_origin, continuation_source_kind,
    continuation_source_session_id, legacy_materialized
) VALUES (?, ?, ?, '{}', '{"transition_parameters":{}}', ?, 'ready', ?, ?, ?, 'exact', ?, 0)`,
			string(task.ID),
			string(started.Reference.NodeID),
			string(branchKey),
			sourceSessionID.String(),
			string(*started.EnteredByEdgeID),
			started.AgentExecutionSelection.Assignee,
			string(started.AgentExecutionSelection.Origin),
			sourceSessionID.String(),
		); err != nil {
			t.Fatalf("insert branch Current Node %q: %v", branchKey, err)
		}
		reference, err := workflow.NewCurrentNodeReference(task.ID, started.Reference.NodeID, &branchKey)
		if err != nil {
			t.Fatalf("create branch Current Node reference %q: %v", branchKey, err)
		}
		sessionID, err := runtimeids.ParseSessionID(createTestSession(t, ctx, store, binding, cfg))
		if err != nil {
			t.Fatalf("ParseSessionID branch %d: %v", index, err)
		}
		if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
			Association: TaskSessionAssociationRequest{
				SessionID:    sessionID,
				CurrentNode:  reference,
				AssociatedAt: associatedAt.Add(time.Duration(index+1) * time.Millisecond),
			},
			ExpectedCurrentSessionID: &sourceSessionID,
		}); err != nil {
			t.Fatalf("bind branch %v: %v", reference, err)
		}
		key, err := reference.Key()
		if err != nil {
			t.Fatalf("branch Current Node key: %v", err)
		}
		references = append(references, reference)
		sessionByReference[key] = sessionID
	}

	associations, err := store.LoadSessionReuseAssociations(ctx, references)
	if err != nil {
		t.Fatalf("LoadSessionReuseAssociations: %v", err)
	}
	if len(associations) != 3 {
		t.Fatalf("association count = %d, want 3", len(associations))
	}
	for _, want := range references {
		key, err := want.Key()
		if err != nil {
			t.Fatalf("Current Node key: %v", err)
		}
		found := false
		for _, association := range associations {
			if association.CurrentNode.Equal(want) && association.SessionID == sessionByReference[key] {
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

func TestBindSessionToCurrentNodeRetainsVisitsAcrossNodes(t *testing.T) {
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
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  started.Mutation.Created[0].Reference,
			AssociatedAt: firstAt,
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode plan: %v", err)
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
	source, err := workflow.NewExactMaterializedContinuationSource(sessionID)
	if err != nil {
		t.Fatalf("NewExactMaterializedContinuationSource: %v", err)
	}
	reviewReference = replaceSerialCurrentNodeBindingFixture(
		t,
		ctx,
		store,
		completed.Mutation.Created[0],
		reviewReference.NodeID,
		&sessionID,
		source,
	)
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  reviewReference,
			AssociatedAt: firstAt.Add(time.Second),
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode review: %v", err)
	}
	count, err := store.CountTaskSessions(ctx, task.ID)
	if err != nil {
		t.Fatalf("CountTaskSessions: %v", err)
	}
	if count != 1 {
		t.Fatalf("task session count = %d, want 1", count)
	}
	for _, currentNode := range []workflow.CurrentNodeReference{started.Mutation.Created[0].Reference, reviewReference} {
		current, err := store.CurrentTaskSessionForNode(ctx, currentNode)
		if err != nil {
			t.Fatalf("CurrentTaskSessionForNode %v: %v", currentNode, err)
		}
		if current.SessionID != sessionID {
			t.Fatalf("current association = %+v, want reused session %q", current, sessionID)
		}
	}
}

func TestBindSessionToCurrentNodeRejectsCrossTaskOwnership(t *testing.T) {
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
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  firstCurrentNode,
			AssociatedAt: associatedAt,
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode first task: %v", err)
	}
	if _, err := store.BindSessionToCurrentNode(ctx, CurrentNodeSessionBindingRequest{
		Association: TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  secondCurrentNode,
			AssociatedAt: associatedAt,
		},
	}); err == nil {
		t.Fatal("BindSessionToCurrentNode cross task succeeded")
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
	if _, err := store.CurrentTaskSessionForNode(ctx, secondCurrentNode); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second task association error = %v, want sql.ErrNoRows", err)
	}
}
