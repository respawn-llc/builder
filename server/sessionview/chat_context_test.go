package sessionview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/auth"
	"core/server/metadata"
	"core/server/session"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
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
	service := NewService(newTestSessionResolver(store), nil, staticExecutionTargetResolver{target: target}).
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

func TestReadDormantSessionChatContextUsesProductionPersistenceResolverWithoutEventLogMaterialization(t *testing.T) {
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
		metadataStore,
		nil,
		metadataStore,
	).WithChatContextWorkspaceResolver(&sessionChatContextWorkspaceResolver{
		app: config.App{Settings: settings},
	}).WithChatContextAuthReader(&sessionChatContextAuthReader{})

	got, err := service.ReadSessionChatContext(t.Context(), sessionChatContextSessionID(t, store))
	if err != nil {
		t.Fatalf("ReadSessionChatContext: %v", err)
	}
	if got.UsedTokens != 42_000 ||
		got.CompletedCompactionCount != 2 ||
		!got.ManualCompactAvailable {
		t.Fatalf("production persisted Context = %+v", got)
	}
}

func TestReadDormantSessionChatContextPropagatesLoadAndAuthFailures(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "workspace", t.TempDir())
	sessionID := sessionChatContextSessionID(t, store)
	targets := staticExecutionTargetResolver{target: availableSessionExecutionTarget(t.TempDir())}

	loadErr := errors.New("config unavailable")
	service := NewService(newTestSessionResolver(store), nil, targets).
		WithChatContextWorkspaceResolver(&sessionChatContextWorkspaceResolver{err: loadErr})
	if _, err := service.ReadSessionChatContext(t.Context(), sessionID); !errors.Is(err, loadErr) {
		t.Fatalf("load error = %v, want %v", err, loadErr)
	}

	authErr := errors.New("auth unavailable")
	settings := config.DefaultOnboardingSettings()
	service = NewService(newTestSessionResolver(store), nil, targets).
		WithChatContextWorkspaceResolver(&sessionChatContextWorkspaceResolver{app: config.App{Settings: settings}}).
		WithChatContextAuthReader(&sessionChatContextAuthReader{err: authErr})
	if _, err := service.ReadSessionChatContext(t.Context(), sessionID); !errors.Is(err, authErr) {
		t.Fatalf("auth error = %v, want %v", err, authErr)
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
