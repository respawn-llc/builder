package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

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

func TestContinueSessionUsesRetainedSessionThinkingContract(t *testing.T) {
	for _, test := range []struct {
		name   string
		source workflow.ContextSource
	}{
		{
			name:   "immediate source",
			source: workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
		},
		{
			name: "selected Node",
			source: workflow.ContextSource{
				Kind:    workflow.ContextSourceSelectedNode,
				NodeKey: "plan",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireRetainedSessionThinkingContract(t, test.source)
		})
	}
}

func requireRetainedSessionThinkingContract(
	t *testing.T,
	contextSource workflow.ContextSource,
) {
	t.Helper()
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	store.roleResolver = completionTargetCatalog{
		roles: map[string]workflow.TargetAgentRole{
			"coder": {
				Identity:         "coder",
				QuestionsEnabled: true,
				Thinking: workflow.ThinkingCapability{
					ReasoningCapable: true,
					Finite:           true,
					Levels:           []string{"low"},
				},
			},
			"reviewer": {
				Identity:         "reviewer",
				QuestionsEnabled: true,
				Thinking: workflow.ThinkingCapability{
					ReasoningCapable: true,
					Finite:           true,
					Levels:           []string{"high", "xhigh"},
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
		edge.ContextSource = contextSource
		edge.ThinkingSelection = workflow.ThinkingSelectionPreviousNode
		edge.Parameters = append(edge.Parameters, workflow.Parameter{
			Key:     "thinking",
			Purpose: workflow.ParameterPurposeTargetThinking,
		})
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	sessionID := associateAndBindCurrentNodeSessionForTest(
		t,
		ctx,
		store,
		binding,
		cfg,
		started.Reference,
	)
	setPersistedSessionRoleForTest(t, cfg, binding, store.metadata, sessionID, "reviewer")

	startContext, err := store.ResolveCurrentNodeStartContext(ctx, started.Reference)
	if err != nil {
		t.Fatalf("ResolveCurrentNodeStartContext: %v", err)
	}
	var thinkingParameterPresent bool
	for _, option := range startContext.TransitionOptions {
		if option.ID != "review" {
			continue
		}
		for _, parameter := range option.Parameters {
			thinkingParameterPresent = thinkingParameterPresent ||
				parameter.Key == "thinking"
		}
	}
	if !thinkingParameterPresent {
		t.Fatalf("retained Session thinking parameter omitted: %+v", startContext.TransitionOptions)
	}

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       started.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{
			"summary":  "plan complete",
			"thinking": "high",
		},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode with retained Session thinking: %v", err)
	}
	target := completed.Mutation.Created[0]
	if target.SessionID == nil || *target.SessionID != sessionID {
		t.Fatalf("target Session = %v, want retained %q", target.SessionID, sessionID)
	}
	if target.AgentExecutionSelection == nil ||
		target.AgentExecutionSelection.Assignee != "reviewer" ||
		target.AgentExecutionSelection.Thinking == nil ||
		*target.AgentExecutionSelection.Thinking != workflow.ThinkingValue("high") ||
		target.AgentExecutionSelection.Origin != workflow.AssigneeOriginRetainedSession {
		t.Fatalf(
			"target execution selection = %+v, want retained reviewer/high",
			target.AgentExecutionSelection,
		)
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
		testGraphEntityBlob(t, string(nodeID)),
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
			testGraphEntityBlob(t, string(started.Reference.NodeID)),
			string(branchKey),
			sourceSessionID.String(),
			testGraphEntityBlob(t, string(*started.EnteredByEdgeID)),
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
