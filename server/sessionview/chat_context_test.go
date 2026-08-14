package sessionview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

type sessionChatContextWorkspaceResolver struct {
	app   config.App
	err   error
	roots []string
}

func (r *sessionChatContextWorkspaceResolver) Resolve(workspaceRoot string) (config.App, error) {
	r.roots = append(r.roots, workspaceRoot)
	if r.err != nil {
		return config.App{}, r.err
	}
	app := r.app
	app.WorkspaceRoot = workspaceRoot
	return app, nil
}

type sessionChatContextAuthReader struct {
	state auth.State
	err   error
	calls int
}

func (r *sessionChatContextAuthReader) Load(context.Context) (auth.State, error) {
	r.calls++
	return r.state, r.err
}

func TestReadDormantSessionChatContextUsesExactExecutionRootAndBoundedFacts(t *testing.T) {
	workspaceRoot := t.TempDir()
	executionRoot := t.TempDir()
	store := newSessionViewStore(t, t.TempDir(), "workspace", workspaceRoot)
	if _, err := store.SetUsageState(&session.UsageState{InputTokens: 125_000}); err != nil {
		t.Fatalf("SetUsageState: %v", err)
	}
	if err := store.SetSessionContextFacts(3, true); err != nil {
		t.Fatalf("SetSessionContextFacts: %v", err)
	}
	autoCompaction := false
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{AutoCompaction: &autoCompaction}); err != nil {
		t.Fatalf("MutateChatSettings: %v", err)
	}
	settings := config.DefaultOnboardingSettings()
	settings.ModelContextWindow = 100_000
	settings.ContextCompactionThresholdTokens = 75_000
	settings.CompactionMode = config.CompactionModeLocal
	resolver := &sessionChatContextWorkspaceResolver{app: config.App{Settings: settings}}
	authReader := &sessionChatContextAuthReader{}
	target := availableSessionExecutionTarget(executionRoot)
	service := NewService(newTestSessionResolver(store), nil, nil, staticExecutionTargetResolver{target: target}).
		WithChatContextWorkspaceResolver(resolver).
		WithChatContextAuthReader(authReader)

	got, err := service.ReadSessionChatContext(t.Context(), sessionChatContextSessionID(t, store))
	if err != nil {
		t.Fatalf("ReadSessionChatContext: %v", err)
	}
	want := serverapi.ChatContext{
		ContextWindowTokens:      100_000,
		UsedTokens:               125_000,
		RemainingTokens:          -25_000,
		AutomaticThresholdTokens: 75_000,
		CompactionMode:           serverapi.ChatContextCompactionModeLocal,
		CompletedCompactionCount: 3,
		ManualCompactAvailable:   true,
	}
	if got != want {
		t.Fatalf("ReadSessionChatContext = %+v, want %+v", got, want)
	}
	if len(resolver.roots) != 1 || resolver.roots[0] != executionRoot {
		t.Fatalf("resolved roots = %v, want exact execution root %q", resolver.roots, executionRoot)
	}
	if authReader.calls != 1 {
		t.Fatalf("auth Load calls = %d, want 1 for unlocked dormant Session", authReader.calls)
	}
}

func TestReadDormantSessionChatContextUsesCurrentRoleSettingsWithLockedContinuity(t *testing.T) {
	executionRoot := t.TempDir()
	store := newSessionViewStore(t, t.TempDir(), "workspace", t.TempDir())
	role := "worker"
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: &role}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model:         "locked-model",
		ContextWindow: 90_000,
		ProviderContract: session.LockedProviderCapabilities{
			ProviderID:               "locked-provider",
			SupportsResponsesCompact: false,
		},
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5.6-sol"
	settings.Reviewer.Model = "gpt-5.6-sol"
	settings.Reviewer.ModelContextWindow = 160_000
	settings.ModelContextWindow = 160_000
	settings.ContextCompactionThresholdTokens = 120_000
	settings.CompactionMode = config.CompactionModeLocal
	roleSettings := settings
	roleSettings.Model = "gpt-5.6-sol"
	roleSettings.Reviewer.Model = "gpt-5.6-sol"
	roleSettings.Reviewer.ModelContextWindow = 140_000
	roleSettings.ModelContextWindow = 140_000
	roleSettings.ContextCompactionThresholdTokens = 110_000
	roleSettings.CompactionMode = config.CompactionModeNative
	roleSettings.Subagents = nil
	settings.Subagents = map[string]config.SubagentRole{
		role: {
			Settings: roleSettings,
			Sources: map[string]string{
				"model_context_window":                "file",
				"context_compaction_threshold_tokens": "file",
				"compaction_mode":                     "file",
			},
		},
	}
	resolver := &sessionChatContextWorkspaceResolver{app: config.App{Settings: settings}}
	authReader := &sessionChatContextAuthReader{err: errors.New("locked Session must not load current auth")}
	service := NewService(
		newTestSessionResolver(store),
		nil,
		nil,
		staticExecutionTargetResolver{target: availableSessionExecutionTarget(executionRoot)},
	).WithChatContextWorkspaceResolver(resolver).WithChatContextAuthReader(authReader)

	got, err := service.ReadSessionChatContext(t.Context(), sessionChatContextSessionID(t, store))
	if err != nil {
		t.Fatalf("ReadSessionChatContext: %v", err)
	}
	if got.ContextWindowTokens != 90_000 ||
		got.AutomaticThresholdTokens != 90_000 ||
		got.CompactionMode != serverapi.ChatContextCompactionModeLocal {
		t.Fatalf("locked/current result = %+v, want locked window/capabilities and current role threshold/mode", got)
	}
	if authReader.calls != 0 {
		t.Fatalf("locked Session made %d auth loads, want 0", authReader.calls)
	}
}

func TestReadDormantSessionChatContextRejectsMalformedEventLogWithoutMaterialization(t *testing.T) {
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(t.Context(), workspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	store, err := session.Create(
		filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions"),
		filepath.Base(binding.CanonicalRoot),
		binding.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if _, err := store.SetUsageState(&session.UsageState{InputTokens: 42_000}); err != nil {
		t.Fatalf("SetUsageState: %v", err)
	}
	if err := store.SetSessionContextFacts(2, true); err != nil {
		t.Fatalf("SetSessionContextFacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), "events.jsonl"), []byte("{malformed"), 0o600); err != nil {
		t.Fatalf("write malformed event log: %v", err)
	}
	settings := config.DefaultOnboardingSettings()
	settings.ModelContextWindow = 100_000
	settings.ContextCompactionThresholdTokens = 75_000
	settings.CompactionMode = config.CompactionModeLocal
	service := NewService(
		registry.NewGlobalPersistenceSessionResolver(
			persistenceRoot,
			metadataStore,
			metadataStore.AuthoritativeSessionStoreOptions()...,
		),
		nil,
		nil,
		metadataStore,
	).WithChatContextWorkspaceResolver(&sessionChatContextWorkspaceResolver{
		app: config.App{Settings: settings},
	}).WithChatContextAuthReader(&sessionChatContextAuthReader{})

	before, err := os.ReadFile(filepath.Join(store.Dir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("read malformed event log before Context read: %v", err)
	}
	_, err = service.ReadSessionChatContext(t.Context(), sessionChatContextSessionID(t, store))
	var materializationErr *session.EventLogMaterializationError
	if !errors.As(err, &materializationErr) {
		t.Fatalf("ReadSessionChatContext error = %v, want EventLogMaterializationError", err)
	}
	after, err := os.ReadFile(filepath.Join(store.Dir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("read malformed event log after Context read: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("Chat Context read materialized or repaired malformed event log")
	}
}

func TestReadDormantSessionChatContextPropagatesLoadAndAuthFailures(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "workspace", t.TempDir())
	sessionID := sessionChatContextSessionID(t, store)
	targets := staticExecutionTargetResolver{target: availableSessionExecutionTarget(t.TempDir())}

	loadErr := errors.New("config unavailable")
	service := NewService(newTestSessionResolver(store), nil, nil, targets).
		WithChatContextWorkspaceResolver(&sessionChatContextWorkspaceResolver{err: loadErr})
	if _, err := service.ReadSessionChatContext(t.Context(), sessionID); !errors.Is(err, loadErr) {
		t.Fatalf("load error = %v, want %v", err, loadErr)
	}

	authErr := errors.New("auth unavailable")
	settings := config.DefaultOnboardingSettings()
	service = NewService(newTestSessionResolver(store), nil, nil, targets).
		WithChatContextWorkspaceResolver(&sessionChatContextWorkspaceResolver{app: config.App{Settings: settings}}).
		WithChatContextAuthReader(&sessionChatContextAuthReader{err: authErr})
	if _, err := service.ReadSessionChatContext(t.Context(), sessionID); !errors.Is(err, authErr) {
		t.Fatalf("auth error = %v, want %v", err, authErr)
	}
}

func TestReadSessionChatContextUsesPersistedFactsWhenRuntimeIsAttached(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "workspace", t.TempDir())
	if _, err := store.SetUsageState(&session.UsageState{InputTokens: 64_000}); err != nil {
		t.Fatalf("SetUsageState: %v", err)
	}
	if err := store.SetSessionContextFacts(0, false); err != nil {
		t.Fatalf("SetSessionContextFacts: %v", err)
	}
	settings := config.DefaultOnboardingSettings()
	settings.ProviderOverride = "openai"
	settings.Model = "gpt-5"
	settings.Reviewer.Frequency = "off"
	settings.ModelContextWindow = 100_000
	settings.ContextCompactionThresholdTokens = 75_000
	settings.CompactionMode = config.CompactionModeLocal
	fixture := newSessionChatContextRuntimeFixture(t, store, settings, llm.ProviderCapabilities{})
	fixture.withEngine(t, func(engine *runtime.Engine) {
		engine.SetAutoCompactionEnabled(false)
	})
	resolver := &sessionChatContextWorkspaceResolver{app: config.App{Settings: settings}}
	authReader := &sessionChatContextAuthReader{}
	service := NewService(
		newTestSessionResolver(store),
		nil,
		fixture.authority,
		staticExecutionTargetResolver{target: availableSessionExecutionTarget(t.TempDir())},
	).WithChatContextWorkspaceResolver(resolver).WithChatContextAuthReader(authReader)

	got, err := service.ReadSessionChatContext(t.Context(), sessionChatContextSessionID(t, store))
	if err != nil {
		t.Fatalf("ReadSessionChatContext: %v", err)
	}
	if got.ContextWindowTokens != 100_000 ||
		got.UsedTokens != 64_000 ||
		got.RemainingTokens != 36_000 ||
		got.AutomaticThresholdTokens != 75_000 ||
		got.CompactionMode != serverapi.ChatContextCompactionModeLocal {
		t.Fatalf("persisted Context = %+v", got)
	}
	if len(resolver.roots) != 1 || authReader.calls != 1 {
		t.Fatalf("persisted Context reads: roots=%v auth_calls=%d", resolver.roots, authReader.calls)
	}
}

func TestReadSessionChatContextReResolvesPersistedConfigWhileRuntimeIsAttached(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "workspace", t.TempDir())
	settings := config.DefaultOnboardingSettings()
	settings.ProviderOverride = "openai"
	settings.Model = "gpt-5"
	settings.Reviewer.Frequency = "off"
	settings.ModelContextWindow = 100_000
	settings.ContextCompactionThresholdTokens = 75_000
	settings.CompactionMode = config.CompactionModeLocal
	fixture := newSessionChatContextRuntimeFixture(t, store, settings, llm.ProviderCapabilities{})
	resolver := &sessionChatContextWorkspaceResolver{app: config.App{Settings: settings}}
	authReader := &sessionChatContextAuthReader{}
	service := NewService(
		newTestSessionResolver(store),
		nil,
		fixture.authority,
		staticExecutionTargetResolver{target: availableSessionExecutionTarget(t.TempDir())},
	).WithChatContextWorkspaceResolver(resolver).WithChatContextAuthReader(authReader)

	live, err := service.ReadSessionChatContext(t.Context(), fixture.sessionID)
	if err != nil {
		t.Fatalf("live ReadSessionChatContext: %v", err)
	}
	if live.ContextWindowTokens != 100_000 || live.CompactionMode != serverapi.ChatContextCompactionModeLocal {
		t.Fatalf("initial live Context = %+v", live)
	}

	resolver.app.Settings.ModelContextWindow = 180_000
	resolver.app.Settings.ContextCompactionThresholdTokens = 140_000
	resolver.app.Settings.CompactionMode = config.CompactionModeNone
	authErr := errors.New("changed auth")
	authReader.err = authErr
	if _, err := service.ReadSessionChatContext(t.Context(), fixture.sessionID); !errors.Is(err, authErr) {
		t.Fatalf("changed persisted auth error = %v, want %v", err, authErr)
	}
	authReader.err = nil
	updated, err := service.ReadSessionChatContext(t.Context(), fixture.sessionID)
	if err != nil {
		t.Fatalf("updated ReadSessionChatContext: %v", err)
	}
	if updated.ContextWindowTokens != 180_000 ||
		updated.AutomaticThresholdTokens != 140_000 ||
		updated.CompactionMode != serverapi.ChatContextCompactionModeDisabled {
		t.Fatalf("updated persisted Context = %+v", updated)
	}
	if len(resolver.roots) != 3 || authReader.calls != 3 {
		t.Fatalf("persisted resolution roots=%v auth_calls=%d, want one per read", resolver.roots, authReader.calls)
	}
}

func TestReadSessionChatContextRuntimeAttachmentDoesNotChangeNamedAgentPolicy(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "workspace", t.TempDir())
	role := "worker"
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: &role}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	settings := config.DefaultOnboardingSettings()
	settings.ProviderOverride = "openai"
	settings.Model = "gpt-5"
	settings.Reviewer.Model = "gpt-5"
	settings.Reviewer.ModelContextWindow = 100_000
	settings.Reviewer.Frequency = "off"
	settings.ModelContextWindow = 100_000
	settings.ContextCompactionThresholdTokens = 75_000
	settings.CompactionMode = config.CompactionModeLocal
	capabilities := llm.ProviderCapabilities{
		ProviderID:               "openai",
		SupportsResponsesCompact: false,
	}
	settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{
		ProviderID:               capabilities.ProviderID,
		SupportsResponsesCompact: capabilities.SupportsResponsesCompact,
	}
	roleSettings := settings
	roleSettings.Reviewer.ModelContextWindow = 130_000
	roleSettings.ModelContextWindow = 130_000
	roleSettings.ContextCompactionThresholdTokens = 105_000
	roleSettings.CompactionMode = config.CompactionModeNative
	roleSettings.Subagents = nil
	settings.Subagents = map[string]config.SubagentRole{
		role: {
			Settings: roleSettings,
			Sources: map[string]string{
				"model_context_window":                "file",
				"context_compaction_threshold_tokens": "file",
				"compaction_mode":                     "file",
			},
		},
	}
	targets := staticExecutionTargetResolver{target: availableSessionExecutionTarget(t.TempDir())}
	dormantService := NewService(newTestSessionResolver(store), nil, nil, targets).
		WithChatContextWorkspaceResolver(&sessionChatContextWorkspaceResolver{app: config.App{Settings: settings}}).
		WithChatContextAuthReader(&sessionChatContextAuthReader{})
	dormant, err := dormantService.ReadSessionChatContext(t.Context(), sessionChatContextSessionID(t, store))
	if err != nil {
		t.Fatalf("dormant ReadSessionChatContext: %v", err)
	}

	current, err := launch.ResolveReadOnlySessionContextSettings(config.App{Settings: settings}, store.ContextSnapshot().Meta, false)
	if err != nil {
		t.Fatalf("ResolveReadOnlySessionContextSettings: %v", err)
	}
	fixture := newSessionChatContextRuntimeFixture(t, store, current.Settings, capabilities)
	liveService := NewService(newTestSessionResolver(store), nil, fixture.authority, targets).
		WithChatContextWorkspaceResolver(&sessionChatContextWorkspaceResolver{app: config.App{Settings: settings}}).
		WithChatContextAuthReader(&sessionChatContextAuthReader{})
	live, err := liveService.ReadSessionChatContext(t.Context(), fixture.sessionID)
	if err != nil {
		t.Fatalf("live ReadSessionChatContext: %v", err)
	}
	if live != dormant {
		t.Fatalf("first-live Context = %+v, want dormant/planned %+v", live, dormant)
	}
	if live.ContextWindowTokens != 130_000 ||
		live.AutomaticThresholdTokens != 105_000 ||
		live.CompactionMode != serverapi.ChatContextCompactionModeLocal {
		t.Fatalf("named Agent Context = %+v", live)
	}
}

type sessionChatContextRuntimeFixture struct {
	authority  *sessionruntime.Authority
	attachment sessionruntime.RuntimeAttachment
	sessionID  runtimeids.SessionID
}

func newSessionChatContextRuntimeFixture(
	t *testing.T,
	store *session.Store,
	settings config.Settings,
	capabilities llm.ProviderCapabilities,
) sessionChatContextRuntimeFixture {
	t.Helper()
	sessionID := sessionChatContextSessionID(t, store)
	filesystemContext, err := runtimewire.NewFilesystemContext(
		store.Meta().WorkspaceRoot,
		store.Meta().WorkspaceRoot,
		metadata.ProjectWorkspaceBoundary{ProjectID: "test"},
	)
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:                     settings,
		QuestionsEnabled:             textutil.Value(true),
		AutoCompactionEnabled:        textutil.Value(true),
		FilesystemContext:            filesystemContext,
		Client:                       &serviceFakeLLM{},
		ProviderCapabilitiesOverride: &capabilities,
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: t.TempDir(),
		StoreOptions:    sessionViewTestPersistence.Options(),
	})
	attachment, err := authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "chat-context-test",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("OpenRuntime: %v", err)
	}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	return sessionChatContextRuntimeFixture{
		authority:  authority,
		attachment: attachment,
		sessionID:  sessionID,
	}
}

func (f sessionChatContextRuntimeFixture) withEngine(t *testing.T, read func(*runtime.Engine)) {
	t.Helper()
	if err := f.authority.WithCurrentRuntime(t.Context(), f.sessionID, func(_ context.Context, engine *runtime.Engine) error {
		read(engine)
		return nil
	}); err != nil {
		t.Fatalf("WithCurrentRuntime: %v", err)
	}
}

func sessionChatContextSessionID(t *testing.T, store *session.Store) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return id
}
