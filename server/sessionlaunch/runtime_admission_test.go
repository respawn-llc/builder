package sessionlaunch

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

type sessionLaunchRuntimeClient struct {
	started     chan struct{}
	startedOnce *sync.Once
	release     <-chan struct{}
}

type sessionLaunchMultiStepRuntimeClient struct {
	firstStarted      chan struct{}
	firstStartedOnce  sync.Once
	releaseFirst      <-chan struct{}
	secondStarted     chan struct{}
	secondStartedOnce sync.Once
	releaseSecond     <-chan struct{}
	mu                sync.Mutex
	calls             int
}

func (c sessionLaunchRuntimeClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	if c.started != nil {
		c.startedOnce.Do(func() {
			close(c.started)
		})
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return llm.Response{}, context.Cause(ctx)
		}
	}
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("finished"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200_000},
	}, nil
}

func (sessionLaunchRuntimeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func (c *sessionLaunchMultiStepRuntimeClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	var release <-chan struct{}
	switch call {
	case 1:
		c.firstStartedOnce.Do(func() { close(c.firstStarted) })
		release = c.releaseFirst
	case 2:
		c.secondStartedOnce.Do(func() { close(c.secondStarted) })
		release = c.releaseSecond
	default:
		return llm.Response{}, errors.New("unexpected model request")
	}
	select {
	case <-release:
	case <-ctx.Done():
		return llm.Response{}, context.Cause(ctx)
	}
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("finished"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200_000},
	}, nil
}

func (*sessionLaunchMultiStepRuntimeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func TestServiceOpenExistingRepairPlanningOwnsRuntimeAdmission(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "sessions")
	persistence := sessiontest.NewPersistence()
	persistenceGate := sessiontest.NewPersistenceGate(persistence)
	store, err := session.Create(
		containerDir,
		"sessions",
		workspace,
		sessioncontract.SessionCategorySubagent,
		session.WithPersistenceObserver(persistenceGate),
		session.WithPersistedSessionResolver(persistence),
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	missingRole := "removed-role"
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: &missingRole}); err != nil {
		t.Fatalf("set unavailable continuation role: %v", err)
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
			Settings: func() config.Settings {
				settings := config.DefaultOnboardingSettings()
				settings.Model = "gpt-5"
				settings.ProviderIdentifier = "openai"
				settings.OpenAIBaseURL = "http://planning.example/v1"
				settings.Reviewer.Model = "gpt-5"
				return settings
			}(),
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
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
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
		_, planErr := service.PlanLaunchSession(context.Background(), PlanRequest{
			Mode:   launch.ModeInteractive,
			Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
		})
		planned <- planErr
	}()
	select {
	case <-planningPersisted:
	case planErr := <-planned:
		t.Fatalf("planning returned before persistence gate: %v", planErr)
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
	if got := reopened.Meta().Continuation; got != nil {
		t.Fatalf("reopened continuation = %+v, want unavailable Agent repair to clear it", got)
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

func TestServiceOpenExistingSubagentSnapshotDoesNotWaitAcrossActiveSteps(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(
		containerDir,
		"sessions",
		workspace,
		sessioncontract.SessionCategorySubagent,
		persistence.Options()...,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionID := mustSessionLaunchIntentID(t, store.Meta().SessionID)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: root,
		StoreOptions:    persistence.Options(),
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
		ContainerDir:             containerDir,
		StoreOptions:             persistence.Options(),
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: workspace},
	}).WithRuntimeAuthority(authority)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	client := &sessionLaunchMultiStepRuntimeClient{
		firstStarted:  firstStarted,
		releaseFirst:  releaseFirst,
		secondStarted: secondStarted,
		releaseSecond: releaseSecond,
	}
	runtimePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200_000,
			Reviewer:           config.ReviewerSettings{Frequency: "off"},
			Shell:              config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeNone},
		},
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		FilesystemContext: func() tools.FilesystemContext {
			filesystemContext, err := runtimewire.NewFilesystemContext(workspace, workspace, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
			if err != nil {
				t.Fatalf("NewFilesystemContext: %v", err)
			}
			return tools.FilesystemContext{Access: filesystemContext.Access}
		}(),
		Client: client,
	})
	if err != nil {
		t.Fatalf("create runtime plan: %v", err)
	}
	attachment, err := authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "active-step-owner",
		Runtime:   &runtimePlan,
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
		select {
		case <-releaseSecond:
		default:
			close(releaseSecond)
		}
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseDetach)
	})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- authority.WithRuntime(context.Background(), attachment.Resource(), func(ctx context.Context, engine *runtime.Engine) error {
			_, err := engine.SubmitUserMessage(ctx, "first step")
			return err
		})
	}()
	select {
	case <-firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first model request did not start")
	}

	planned := make(chan error, 1)
	go func() {
		_, planErr := service.PlanLaunchSession(context.Background(), PlanRequest{
			Mode:   launch.ModeInteractive,
			Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
		})
		planned <- planErr
	}()

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("finish first model request: %v", err)
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- authority.WithRuntime(context.Background(), attachment.Resource(), func(ctx context.Context, engine *runtime.Engine) error {
			_, err := engine.SubmitUserMessage(ctx, "second step")
			return err
		})
	}()
	select {
	case <-secondStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("second model request did not start")
	}
	select {
	case planErr := <-planned:
		if planErr != nil {
			t.Fatalf("plan existing session: %v", planErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("read-only existing-session planning remained blocked across a completed step")
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new opened Session descriptor: %v", err)
	}
	if err := authority.WithSessionStore(context.Background(), descriptor, func(_ context.Context, opened *session.Store) error {
		got := opened.Meta().Category
		if got == nil || *got != sessioncontract.SessionCategoryMain {
			return errors.New("opened Session category was not promoted to main")
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect opened Session category: %v", err)
	}

	close(releaseSecond)
	if err := <-secondDone; err != nil {
		t.Fatalf("finish second model request: %v", err)
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
		_, planErr := service.PlanLaunchSession(context.Background(), PlanRequest{
			Mode:   launch.ModeInteractive,
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
