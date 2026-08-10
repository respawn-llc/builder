package workflowrunner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/launch"
	"core/server/metadata"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

type gatedMetadataSessionPersistence struct {
	sessions *sessiontest.Persistence
	metadata *metadata.Store
}

func (p gatedMetadataSessionPersistence) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	if err := p.sessions.ObservePersistedStore(ctx, snapshot); err != nil {
		return err
	}
	return p.metadata.ImportSessionSnapshot(ctx, snapshot)
}

func (p gatedMetadataSessionPersistence) ObserveEventLogReconciliation(ctx context.Context, reconciliation session.PersistedEventLogReconciliation) error {
	if err := p.sessions.ObserveEventLogReconciliation(ctx, reconciliation); err != nil {
		return err
	}
	record, err := p.sessions.ResolvePersistedSession(ctx, reconciliation.SessionID)
	if err != nil {
		return err
	}
	if record.Meta == nil {
		return errors.New("reconciled session metadata is required")
	}
	return p.metadata.ImportSessionSnapshot(ctx, session.PersistedStoreSnapshot{
		SessionDir: record.SessionDir,
		Meta:       *record.Meta,
	})
}

func (p gatedMetadataSessionPersistence) ResolvePersistedSession(ctx context.Context, sessionID string) (session.PersistedSessionRecord, error) {
	return p.metadata.ResolvePersistedSession(ctx, sessionID)
}

func TestCurrentNodeSessionIntentReusesDirectRetainedSession(t *testing.T) {
	sessionID := mustSessionID(t)
	reference, err := workflow.NewCurrentNodeReference("task-retained-session", "node-reimplementation", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	starter := &Starter{}

	input := workflowstore.CurrentNodeStartContext{
		CurrentNode: workflow.CurrentNode{
			Reference: reference,
			SessionID: &sessionID,
		},
		ContextMode: workflow.ContextModeContinueSession,
	}
	policy, err := resolveCurrentNodeSessionPolicy(input)
	if err != nil {
		t.Fatalf("resolveCurrentNodeSessionPolicy: %v", err)
	}
	intent, disposable, err := starter.currentNodeSessionIntent(input, t.TempDir(), policy)
	if err != nil {
		t.Fatalf("currentNodeSessionIntent: %v", err)
	}
	resolvedSessionID, existing := intent.SessionID()
	if !existing || resolvedSessionID != sessionID || disposable {
		t.Fatalf(
			"retained current-node intent = session %q existing %t disposable %t, want existing retained session %q",
			resolvedSessionID,
			existing,
			disposable,
			sessionID,
		)
	}
}

func TestCurrentNodeSessionPolicyReusesTargetOwnedFanoutSession(t *testing.T) {
	sessionID := mustSessionID(t)
	policy, err := resolveCurrentNodeSessionPolicy(workflowstore.CurrentNodeStartContext{
		ContextMode:    workflow.ContextModeContinueSession,
		IsFanoutBranch: true,
		CurrentNode: workflow.CurrentNode{
			SessionID: &sessionID,
		},
		EnteringEdge: workflow.Edge{
			ContextSource: workflow.ContextSource{
				Kind: workflow.ContextSourcePreviousTargetOrNew,
			},
		},
	})
	if err != nil {
		t.Fatalf("resolveCurrentNodeSessionPolicy: %v", err)
	}
	if policy.cloneRetainedSession {
		t.Fatal("previous-target fan-out continuation must reuse its target-owned Session")
	}
	if policy.assignee != currentNodeSessionAssigneePreserve {
		t.Fatalf("previous-target assignee policy = %v, want preserve", policy.assignee)
	}
}

func TestPlanCurrentNodeSessionEnforcesRoleBoundaries(t *testing.T) {
	ctx := context.Background()
	persistenceRoot := t.TempDir()
	workspace := t.TempDir()
	metadataStore := testsetup.OpenStore(t, persistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(ctx, workspace)
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}
	persistence := sessiontest.NewPersistence()
	persisted := gatedMetadataSessionPersistence{
		sessions: persistence,
		metadata: metadataStore,
	}
	persistenceGate := sessiontest.NewPersistenceGate(persisted)
	storeOptions := []session.StoreOption{
		session.WithPersistenceObserver(persistenceGate),
		session.WithPersistedSessionResolver(persisted),
	}
	containerDir := filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions")
	store, err := session.Create(
		containerDir,
		"sessions",
		workspace,
		sessioncontract.SessionCategoryMain,
		storeOptions...,
	)
	if err != nil {
		t.Fatalf("create retained workflow session: %v", err)
	}
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("coder")}); err != nil {
		t.Fatalf("set retained workflow role: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "gpt-5"}); err != nil {
		t.Fatalf("lock retained workflow session: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse retained session id: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	loaded, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("load workflow planning config: %v", err)
	}
	settings := loaded.Settings
	settings.Model = "gpt-5"
	settings.OpenAIBaseURL = "http://workflow-planning.example/v1"
	settings.Reviewer.Frequency = "off"
	settings.Shell.PostprocessingMode = config.ShellPostprocessingModeNone
	coderSettings := settings
	coderSettings.Model = "gpt-5"
	coderSettings.Subagents = nil
	reviewerSettings := settings
	reviewerSettings.Model = "gpt-5-reviewer"
	reviewerSettings.Subagents = nil
	settings.Subagents = map[string]config.SubagentRole{
		"coder": {
			Settings: coderSettings,
			Sources:  map[string]string{"model": "test"},
		},
		"reviewer": {
			Settings: reviewerSettings,
			Sources:  map[string]string{"model": "test"},
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: persistenceRoot,
		StoreOptions:    storeOptions,
	})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close authority: %v", closeErr)
		}
	})
	starter := &Starter{
		cfg: config.App{
			PersistenceRoot: persistenceRoot,
			WorkspaceRoot:   workspace,
			Settings:        settings,
		},
		metadata:         metadataStore,
		runtimeAuthority: authority,
		storeOptions:     storeOptions,
	}
	reference, err := workflow.NewCurrentNodeReference(workflow.TaskID("task-retained-session"), workflow.NodeID("node-reimplementation"), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	selection, err := workflow.NewAgentExecutionSelection(
		"reviewer",
		nil,
		workflow.AssigneeOriginTransitionSelected,
	)
	if err != nil {
		t.Fatalf("NewAgentExecutionSelection: %v", err)
	}
	input := workflowstore.CurrentNodeStartContext{
		Task: workflowstore.TaskRecord{
			ID:        reference.TaskID,
			ProjectID: binding.ProjectID,
		},
		Node: workflowstore.NodeRecord{
			ID:           reference.NodeID,
			SubagentRole: "reviewer",
		},
		CurrentNode: workflow.CurrentNode{
			Reference:               reference,
			SessionID:               &sessionID,
			AgentExecutionSelection: &selection,
		},
		ContextMode: workflow.ContextModeCompactAndContinueSession,
		ExecutionRoot: &workflowstore.ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: workspace,
		},
	}
	root, err := requireCurrentNodeExecutionRoot(input)
	if err != nil {
		t.Fatalf("requireCurrentNodeExecutionRoot: %v", err)
	}

	planningPersisted, releasePlanningPersistence := persistenceGate.BlockNext()
	t.Cleanup(releasePlanningPersistence)
	type planResult struct {
		sessionID  runtimeids.SessionID
		disposable bool
		err        error
	}
	planned := make(chan planResult, 1)
	go func() {
		plan, disposable, planErr := starter.planCurrentNodeSession(ctx, input, root, false)
		result := planResult{disposable: disposable, err: planErr}
		if planErr == nil {
			result.sessionID = plan.Descriptor.SessionID()
		}
		planned <- result
	}()
	select {
	case <-planningPersisted:
	case <-time.After(3 * time.Second):
		t.Fatal("retained current-node planning did not reach persistence gate")
	}

	releasePlanningPersistence()
	var plannedResult planResult
	select {
	case plannedResult = <-planned:
	case <-time.After(3 * time.Second):
		t.Fatal("retained current-node planning did not complete after persistence release")
	}
	if plannedResult.err != nil {
		t.Fatalf("plan retained current-node session: %v", plannedResult.err)
	}
	if plannedResult.sessionID != sessionID || plannedResult.disposable {
		t.Fatalf(
			"retained current-node plan = session %q disposable %t, want restored session %q without disposal",
			plannedResult.sessionID,
			plannedResult.disposable,
			sessionID,
		)
	}
	record, err := metadataStore.ResolvePersistedSession(ctx, sessionID.String())
	if err != nil {
		t.Fatalf("resolve compacted workflow session: %v", err)
	}
	if record.Meta == nil || record.Meta.Continuation == nil || record.Meta.Continuation.AgentRole == nil || *record.Meta.Continuation.AgentRole != "reviewer" {
		t.Fatalf("compacted workflow Session continuation = %+v, want reviewer", record.Meta)
	}
	if record.Meta.Locked != nil || record.Meta.PromptCacheLineageGeneration != 1 {
		t.Fatalf("compacted workflow Session contract = locked %+v lineage %d, want unlocked lineage 1", record.Meta.Locked, record.Meta.PromptCacheLineageGeneration)
	}

	continuedStore, err := session.Create(
		containerDir,
		"sessions",
		workspace,
		sessioncontract.SessionCategoryMain,
		storeOptions...,
	)
	if err != nil {
		t.Fatalf("create direct continuation workflow session: %v", err)
	}
	if err := continuedStore.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("coder")}); err != nil {
		t.Fatalf("set direct continuation role: %v", err)
	}
	if err := continuedStore.MarkModelDispatchLocked(session.LockedContract{Model: "gpt-5"}); err != nil {
		t.Fatalf("lock direct continuation Session: %v", err)
	}
	continuedSessionID, err := runtimeids.ParseSessionID(continuedStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse direct continuation session id: %v", err)
	}
	input.CurrentNode.SessionID = &continuedSessionID
	input.ContextMode = workflow.ContextModeContinueSession

	_, directDisposable, err := starter.planCurrentNodeSession(ctx, input, root, false)
	if !errors.Is(err, launch.ErrLockedAgentRoleChange) {
		t.Fatalf("plan cross-role direct continuation error = %v, want %v", err, launch.ErrLockedAgentRoleChange)
	}
	if directDisposable {
		t.Fatal("cross-role direct continuation unexpectedly marked retained Session disposable")
	}

	input.IsFanoutBranch = true
	input.EnteringEdge.ContextSource = workflow.ContextSource{
		Kind: workflow.ContextSourcePreviousTargetOrNew,
	}
	_, targetOwnedDisposable, err := starter.planCurrentNodeSession(ctx, input, root, false)
	if !errors.Is(err, launch.ErrLockedAgentRoleChange) {
		t.Fatalf("plan cross-role target-owned continuation error = %v, want %v", err, launch.ErrLockedAgentRoleChange)
	}
	if targetOwnedDisposable {
		t.Fatal("cross-role target-owned continuation unexpectedly marked retained Session disposable")
	}
	targetOwnedRecord, err := metadataStore.ResolvePersistedSession(ctx, continuedSessionID.String())
	if err != nil {
		t.Fatalf("resolve target-owned continuation Session: %v", err)
	}
	if targetOwnedRecord.Meta == nil ||
		targetOwnedRecord.Meta.Continuation == nil ||
		targetOwnedRecord.Meta.Continuation.AgentRole == nil ||
		*targetOwnedRecord.Meta.Continuation.AgentRole != "coder" {
		t.Fatalf(
			"target-owned continuation role = %+v, want preserved coder",
			targetOwnedRecord.Meta,
		)
	}
}

func mustSessionID(t *testing.T) runtimeids.SessionID {
	t.Helper()
	return runtimeids.NewSessionID()
}
