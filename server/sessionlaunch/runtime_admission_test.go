package sessionlaunch

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type sessionLaunchRuntimeClient struct{}

func (sessionLaunchRuntimeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("unexpected model request")
}

func (sessionLaunchRuntimeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func TestServiceOpenExistingPlanningOwnsRuntimeAdmission(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "sessions")
	persistence := sessiontest.NewPersistence()
	persistenceGate := sessiontest.NewPersistenceGate(persistence)
	store, err := session.Create(
		containerDir,
		"sessions",
		workspace,
		sessioncontract.SessionCategoryMain,
		session.WithPersistenceObserver(persistenceGate),
		session.WithPersistedSessionResolver(persistence),
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionID := mustSessionLaunchIntentID(t, store.Meta().SessionID)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: root,
		StoreOptions: []session.StoreOption{
			session.WithPersistenceObserver(persistenceGate),
			session.WithPersistedSessionResolver(persistence),
		},
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings: config.Settings{
				Model:         "gpt-5",
				OpenAIBaseURL: "http://planning.example/v1",
			},
		},
		ContainerDir: containerDir,
		StoreOptions: []session.StoreOption{
			session.WithPersistenceObserver(persistenceGate),
			session.WithPersistedSessionResolver(persistence),
		},
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: workspace},
	}).WithRuntimeAuthority(authority)
	runtimePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200_000,
			Reviewer:           config.ReviewerSettings{Frequency: "off"},
			Shell: config.ShellSettings{
				PostprocessingMode: config.ShellPostprocessingModeNone,
			},
		},
		FilesystemContext: func() tools.FilesystemContext {
			context, err := runtimewire.NewFilesystemContext(workspace, workspace, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
			if err != nil {
				t.Fatalf("NewFilesystemContext: %v", err)
			}
			return context
		}(),
		Client: sessionLaunchRuntimeClient{},
	})
	if err != nil {
		t.Fatalf("create runtime plan: %v", err)
	}

	planningPersisted, releasePlanningPersistence := persistenceGate.BlockNext()
	t.Cleanup(releasePlanningPersistence)
	planned := make(chan error, 1)
	go func() {
		_, planErr := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{

			Mode:   serverapi.SessionLaunchModeInteractive,
			Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
		})
		planned <- planErr
	}()
	select {
	case <-planningPersisted:
	case <-time.After(3 * time.Second):
		t.Fatal("planning mutation did not reach persistence gate")
	}
	if _, err := authority.TryBlockSessionStarts(
		context.Background(),
		[]runtimeids.SessionID{sessionID},
		sessionruntime.SessionStartBlockMaintenance,
	); !errors.Is(err, sessionruntime.ErrSessionStartAdmissionBusy) {
		t.Fatalf("planning admission = %v, want ErrSessionStartAdmissionBusy", err)
	}

	runnerRelease := make(chan struct{})
	t.Cleanup(func() { close(runnerRelease) })
	startDescriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}
	type startResult struct {
		handle sessionruntime.ExecutionHandle
		err    error
	}
	started := make(chan startResult, 1)
	go func() {
		handle, startErr := authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
			Descriptor: startDescriptor,
			Runtime:    &runtimePlan,
			Resource:   sessionruntime.OpenAgentResource{},
			Runner: func(ctx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
				select {
				case <-runnerRelease:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
		})
		started <- startResult{handle: handle, err: startErr}
	}()

	releasePlanningPersistence()
	var planErr error
	select {
	case planErr = <-planned:
	case <-time.After(3 * time.Second):
		t.Fatal("planning did not complete after persistence release")
	}
	if planErr != nil {
		t.Fatalf("plan existing session: %v", planErr)
	}
	var runtimeStart startResult
	select {
	case runtimeStart = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime activation did not complete after planning release")
	}
	if runtimeStart.err != nil {
		t.Fatalf("start runtime: %v", runtimeStart.err)
	}
	if runtimeStart.handle == nil {
		t.Fatal("runtime activation returned nil handle")
	}

	eventText := "runtime-owned committed event"
	storeDescriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}
	if err := authority.WithSessionStore(context.Background(), storeDescriptor, func(_ context.Context, runtimeStore *session.Store) error {
		eventLog, materializeErr := runtimeStore.MaterializeEventLog()
		if materializeErr != nil {
			return materializeErr
		}
		_, receipt, appendErr := eventLog.AppendRecord(nil, session.LocalEntryRecord{
			Visibility: session.EntryVisibilityHidden,
			Role:       "runtime",
			Text:       &eventText,
		})
		if appendErr != nil {
			return appendErr
		}
		if !receipt.Committed {
			return errors.New("runtime event append was not committed")
		}
		return nil
	}); err != nil {
		t.Fatalf("append runtime-owned event: %v", err)
	}

	reopened, err := session.Open(
		store.Dir(),
		session.WithPersistenceObserver(persistenceGate),
		session.WithPersistedSessionResolver(persistence),
	)
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	if got := reopened.Meta().Continuation; got == nil || got.OpenAIBaseURL != "http://planning.example/v1" {
		t.Fatalf("reopened continuation = %+v, want planning metadata", got)
	}
	eventLog, err := reopened.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize reopened event log: %v", err)
	}
	window, err := eventLog.ReadRecentRecords(8)
	if err != nil {
		t.Fatalf("read reopened event log: %v", err)
	}
	foundRuntimeEvent := false
	for _, record := range window.Records {
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			t.Fatalf("read reopened event payload: %v", payloadErr)
		}
		entry, ok := payload.(session.LocalEntryRecord)
		if ok && entry.Role == "runtime" &&
			entry.Text != nil && *entry.Text == eventText {
			foundRuntimeEvent = true
		}
	}
	if !foundRuntimeEvent {
		t.Fatal("reopened event log omitted runtime-owned committed event")
	}
}

func TestServiceOpenExistingWithoutAuthorityFailsBeforeStoreMutation(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "sessions")
	persistence := sessiontest.NewPersistence()
	persistenceGate := sessiontest.NewPersistenceGate(persistence)
	store, err := session.Create(
		containerDir,
		"sessions",
		workspace,
		sessioncontract.SessionCategoryMain,
		session.WithPersistenceObserver(persistenceGate),
		session.WithPersistedSessionResolver(persistence),
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings:        config.Settings{Model: "gpt-5"},
		},
		ContainerDir: containerDir,
		StoreOptions: []session.StoreOption{
			session.WithPersistenceObserver(persistenceGate),
			session.WithPersistedSessionResolver(persistence),
		},
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: workspace},
	})

	persisted, releasePersistence := persistenceGate.BlockNext()
	t.Cleanup(releasePersistence)
	result := make(chan error, 1)
	go func() {
		_, planErr := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{

			Mode:   serverapi.SessionLaunchModeInteractive,
			Intent: serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		})
		result <- planErr
	}()

	select {
	case err := <-result:
		if !errors.Is(err, ErrExistingSessionAuthorityRequired) {
			t.Fatalf("PlanSession error = %v, want ErrExistingSessionAuthorityRequired", err)
		}
	case <-persisted:
		t.Fatal("existing-session planning mutated the Store without Authority admission")
	case <-time.After(3 * time.Second):
		t.Fatal("existing-session planning did not fail closed without Authority")
	}
	select {
	case <-persisted:
		t.Fatal("existing-session planning reached persistence without Authority admission")
	default:
	}
	if store.Meta().Continuation != nil {
		t.Fatalf("existing-session planning mutated continuation without Authority: %+v", store.Meta().Continuation)
	}
}
