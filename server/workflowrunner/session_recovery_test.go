package workflowrunner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

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

	intent, disposable, err := starter.currentNodeSessionIntent(workflowstore.CurrentNodeStartContext{
		CurrentNode: workflow.CurrentNode{
			Reference: reference,
			SessionID: &sessionID,
		},
		ContextMode: workflow.ContextModeContinueSession,
	}, t.TempDir())
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

func TestPlanCurrentNodeCompactAndContinueSessionRestoresDirectRetainedSession(t *testing.T) {
	ctx := context.Background()
	persistenceRoot := t.TempDir()
	workspace := t.TempDir()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
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
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse retained session id: %v", err)
	}
	settings := config.Settings{
		Model:              "gpt-5",
		ModelContextWindow: 200_000,
		OpenAIBaseURL:      "http://workflow-planning.example/v1",
		Reviewer:           config.ReviewerSettings{Frequency: "off"},
		Shell: config.ShellSettings{
			PostprocessingMode: config.ShellPostprocessingModeNone,
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
	input := workflowstore.CurrentNodeStartContext{
		Task: workflowstore.TaskRecord{
			ID:        reference.TaskID,
			ProjectID: binding.ProjectID,
		},
		Node: workflowstore.NodeRecord{
			ID:           reference.NodeID,
			SubagentRole: workflow.DefaultAgentRole,
		},
		CurrentNode: workflow.CurrentNode{
			Reference: reference,
			SessionID: &sessionID,
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
		plan, disposable, planErr := starter.planCurrentNodeSession(ctx, input, root)
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
}

func mustSessionID(t *testing.T) runtimeids.SessionID {
	t.Helper()
	return runtimeids.NewSessionID()
}
