package sessionlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/auth"
	"core/server/launch"
	"core/server/metadata"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/protoapi"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type failingAuthStateReader struct{}

type nonRefreshingAuthStateReader struct {
	loaded       auth.State
	current      auth.State
	loadCalls    int
	currentCalls int
}

func (r *nonRefreshingAuthStateReader) Load(context.Context) (auth.State, error) {
	r.loadCalls++
	return r.loaded, nil
}

func (r *nonRefreshingAuthStateReader) CurrentState(context.Context) (auth.State, error) {
	r.currentCalls++
	return r.current, nil
}

func (r *nonRefreshingAuthStateReader) StoredState(context.Context) (auth.State, error) {
	return auth.EmptyState(), nil
}

var serviceTestPersistence = sessiontest.NewPersistence()

func createLaunchTestSession(t *testing.T, containerDir, name, workspace string) *session.Store {
	t.Helper()
	store, err := session.Create(containerDir, name, workspace, sessioncontract.SessionCategoryMain, serviceTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return store
}

func (failingAuthStateReader) Load(context.Context) (auth.State, error) {
	return auth.EmptyState(), nil
}

func (failingAuthStateReader) CurrentState(context.Context) (auth.State, error) {
	return auth.State{}, errors.New("auth unavailable")
}

func (failingAuthStateReader) StoredState(context.Context) (auth.State, error) {
	return auth.EmptyState(), nil
}

func TestPlanLaunchSessionResolvesEffectiveAuthAfterFinalNamedRoleSelection(t *testing.T) {
	workspace := t.TempDir()
	cfg, err := config.Load(workspace, config.LoadOptions{ConfigRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Settings.CompactionMode = config.CompactionModeNative
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: func() config.Settings {
				settings := cfg.Settings
				settings.Model = "worker-model"
				settings.OpenAIBaseURL = "https://compatible.example/v1"
				settings.ThinkingLevel = "high"
				settings.Reviewer.Model = "worker-model"
				settings.Reviewer.ThinkingLevel = "high"
				settings.Subagents = nil
				return settings
			}(),
			Sources: map[string]string{
				"model":           "file",
				"openai_base_url": "file",
				"thinking_level":  "file",
			},
		},
	}
	containerDir := t.TempDir()
	reader := &nonRefreshingAuthStateReader{
		loaded:  auth.State{Method: auth.Method{Type: auth.MethodAPIKey}},
		current: auth.State{Method: auth.Method{Type: auth.MethodOAuth}},
	}
	service := newSessionLaunchTestService(cfg, containerDir).WithAuthStateReader(reader)
	role := "worker"

	result, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:      launch.ModeHeadless,
		Intent:    serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		Overrides: serverapi.RunPromptOverrides{AgentRole: &role},
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if result.Plan.ActiveSettings.CompactionMode != config.CompactionModeLocal {
		t.Fatalf("CompactionMode = %q, want API-key compatible-provider local fallback; refreshing OAuth would select native", result.Plan.ActiveSettings.CompactionMode)
	}
	if reader.loadCalls != 1 || reader.currentCalls != 1 {
		t.Fatalf("auth calls Load/CurrentState = %d/%d, want non-refreshing policy read after existing readiness read", reader.loadCalls, reader.currentCalls)
	}
}

func TestPlanLaunchSessionLoadsEffectiveAuthWhenLockedProviderContractIsAbsent(t *testing.T) {
	workspace := t.TempDir()
	cfg, err := config.Load(workspace, config.LoadOptions{ConfigRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Settings.CompactionMode = config.CompactionModeNative
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model: cfg.Settings.Model,
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	reader := &nonRefreshingAuthStateReader{
		loaded:  auth.State{Method: auth.Method{Type: auth.MethodOAuth}},
		current: auth.State{Method: auth.Method{Type: auth.MethodOAuth}},
	}
	service := newSessionLaunchTestService(cfg, containerDir).WithAuthStateReader(reader)

	result, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if result.Plan.ActiveSettings.CompactionMode != config.CompactionModeNative {
		t.Fatalf("CompactionMode = %q, want OAuth provider-native mode", result.Plan.ActiveSettings.CompactionMode)
	}
	if reader.loadCalls != 1 {
		t.Fatalf("effective auth Load calls = %d, want 1", reader.loadCalls)
	}
}

func TestPlanLaunchSessionSkipsEffectiveAuthForExplicitProviderCapabilities(t *testing.T) {
	workspace := t.TempDir()
	cfg, err := config.Load(workspace, config.LoadOptions{ConfigRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Settings.CompactionMode = config.CompactionModeNative
	cfg.Settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{
		ProviderID:               "custom",
		SupportsResponsesCompact: false,
	}
	reader := &nonRefreshingAuthStateReader{
		loaded: auth.State{Method: auth.Method{Type: auth.MethodOAuth}},
	}
	service := newSessionLaunchTestService(cfg, t.TempDir()).WithAuthStateReader(reader)

	result, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if result.Plan.ActiveSettings.CompactionMode != config.CompactionModeLocal {
		t.Fatalf("CompactionMode = %q, want explicit-capability local fallback", result.Plan.ActiveSettings.CompactionMode)
	}
	if reader.loadCalls != 0 {
		t.Fatalf("effective auth Load calls = %d, want 0", reader.loadCalls)
	}
}

func sessionLaunchStringPtr(value string) *string {
	return &value
}

func newSessionLaunchTestService(cfg config.App, containerDir string) *Service {
	return NewService(launch.Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		StoreOptions:             serviceTestPersistence.Options(),
		PersistedSessions:        serviceTestPersistence,
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: cfg.WorkspaceRoot},
	})
}

type sessionLaunchBoundaryResolver struct{ root string }

func (r sessionLaunchBoundaryResolver) ResolveSessionProjectWorkspaceBoundary(context.Context, string) (metadata.ProjectWorkspaceBoundary, error) {
	return metadata.ProjectWorkspaceBoundary{ProjectID: "test-project", Workspaces: []metadata.ProjectWorkspace{{CanonicalRoot: r.root}}}, nil
}

func (r sessionLaunchBoundaryResolver) ListManagedWorktreeRoots(context.Context) ([]string, error) {
	return nil, nil
}

func TestPlanLaunchSessionReadsPromptHistoryFromMetadataOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, home)
	ctx := context.Background()
	workspace := t.TempDir()
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	meta, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	binding, err := meta.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	containerDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	store, err := session.Create(containerDir, filepath.Base(containerDir), cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	eventLogText := "event-log history must not become prompt history"
	if _, receipt, err := eventLog.AppendRecord(nil, session.LocalEntryRecord{
		Visibility: session.EntryVisibilityHidden,
		Role:       "system",
		Text:       &eventLogText,
	}); err != nil || !receipt.Committed {
		t.Fatalf("append event-log entry: receipt=%+v error=%v", receipt, err)
	}
	if _, err := meta.RecordPromptHistoryEntry(ctx, metadata.PromptHistoryEntry{
		SessionID: store.Meta().SessionID,
		Text:      "db-history",
	}); err != nil {
		t.Fatalf("record metadata prompt history: %v", err)
	}
	service := NewService(launch.Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		StoreOptions:             meta.AuthoritativeSessionStoreOptions(),
		PersistedSessions:        meta,
		ProjectWorkspaceBoundary: meta,
	}).WithPromptHistoryReader(meta)

	resp, err := service.PlanLaunchSession(ctx, PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if !reflect.DeepEqual(resp.Plan.PromptHistory, []string{"db-history"}) {
		t.Fatalf("prompt history = %+v, want metadata only", resp.Plan.PromptHistory)
	}
}

func TestServicePlanSessionProjectsTypedOptionalSessionName(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "sessions")
	store := createLaunchTestSession(t, containerDir, "initial", workspace)
	service := newSessionLaunchTestService(loadSessionLaunchTestConfig(t, workspace, root), containerDir)
	intent, err := protoapi.SessionLaunchIntentToProto(
		serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	)
	if err != nil {
		t.Fatalf("SessionLaunchIntentToProto: %v", err)
	}
	request := &sessionlaunchpb.SessionPlanRequest{
		Mode:   sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_INTERACTIVE,
		Intent: intent,
	}

	if err := store.SetName(""); err != nil {
		t.Fatalf("clear session name: %v", err)
	}
	absent, err := service.PlanSession(t.Context(), request)
	if err != nil {
		t.Fatalf("PlanSession absent name: %v", err)
	}
	if absent.Plan.SessionName != nil {
		t.Fatalf("absent session name = %v, want nil", absent.Plan.SessionName)
	}

	title := "Incident triage"
	if err := store.SetName(title); err != nil {
		t.Fatalf("set session name: %v", err)
	}
	present, err := service.PlanSession(t.Context(), request)
	if err != nil {
		t.Fatalf("PlanSession present name: %v", err)
	}
	if present.Plan.SessionName == nil || *present.Plan.SessionName != title {
		t.Fatalf("present session name = %v, want %q", present.Plan.SessionName, title)
	}
}

func TestPlanLaunchSessionReturnsPlanWithoutRegisteringStore(t *testing.T) {
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	service := newSessionLaunchTestService(config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: persistenceRoot,
		Settings:        config.Settings{Model: "gpt-5", OpenAIBaseURL: "http://config.local/v1"},
	}, containerDir)

	resp, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if resp.Plan.Descriptor.SessionID().String() == "" {
		t.Fatal("expected session id")
	}
	if resp.Plan.ActiveSettings.OpenAIBaseURL != "http://config.local/v1" {
		t.Fatalf("active OpenAI base URL = %q, want http://config.local/v1", resp.Plan.ActiveSettings.OpenAIBaseURL)
	}
}

func TestPlanLaunchSessionCreatesIndependentMainSessionWithInitialChatState(t *testing.T) {
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	cfg.Settings.Model = "gpt-5.6-sol"
	cfg.Settings.Reviewer.Frequency = "edits"
	cfg.Settings.ThinkingLevel = "medium"
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	service := newSessionLaunchTestService(cfg, containerDir)
	draft := " exact unsent\ncomposer text "

	result, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		InitialChat: &InitialChatCreation{
			Settings: serverapi.InitialChatSettings{
				AgentRole:             config.DefaultSubagentRole,
				Supervisor:            serverapi.ChatSettingsSupervisorAlways,
				Thinking:              textutil.Value("high"),
				Fast:                  textutil.Value(false),
				QuestionsEnabled:      false,
				AutoCompactionEnabled: false,
			},
			InputDraft: &draft,
		},
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}

	reopened, err := session.Open(
		filepath.Join(containerDir, result.Plan.Descriptor.SessionID().String()),
		serviceTestPersistence.Options()...,
	)
	if err != nil {
		t.Fatalf("open created Session: %v", err)
	}
	state, err := session.ChatDraftStateFromMeta(reopened.Meta())
	if err != nil {
		t.Fatalf("ChatDraftStateFromMeta: %v", err)
	}
	want := sessiontest.CompleteChatSettingsState(
		t,
		config.DefaultSubagentRole,
		"all",
		"high",
		false,
		false,
		false,
	)
	if state.Message != draft ||
		state.Agent != want.Agent ||
		!reflect.DeepEqual(state.Settings, want.Settings) {
		t.Fatalf("created Chat state = %+v, want draft %q and settings %+v", state, draft, want)
	}
	if result.Plan.ActiveSettings.Reviewer.Frequency != "all" ||
		result.Plan.ActiveSettings.ThinkingLevel != "high" ||
		result.Plan.ActiveSettings.PriorityRequestMode ||
		result.Plan.QuestionsEnabled ||
		result.Plan.AutoCompactionEnabled {
		t.Fatalf(
			"planned Chat settings = supervisor=%q thinking=%q fast=%t questions=%t auto_compaction=%t",
			result.Plan.ActiveSettings.Reviewer.Frequency,
			result.Plan.ActiveSettings.ThinkingLevel,
			result.Plan.ActiveSettings.PriorityRequestMode,
			result.Plan.QuestionsEnabled,
			result.Plan.AutoCompactionEnabled,
		)
	}
}

func TestPlanLaunchSessionReturnsNoSessionWhenOrdinaryCreationPersistenceFails(t *testing.T) {
	workspace := t.TempDir()
	cfg := loadSessionLaunchTestConfig(t, workspace, t.TempDir())
	cfg.Settings.Model = "gpt-5.6-sol"
	persistence := sessiontest.NewPersistence()
	gate := sessiontest.NewPersistenceGate(persistence)
	persistenceErr := errors.New("ordinary creation persistence failed")
	gate.FailNext(persistenceErr)
	service := NewService(launch.Planner{
		Config:       cfg,
		ContainerDir: t.TempDir(),
		StoreOptions: []session.StoreOption{
			session.WithPersistenceObserver(gate),
			session.WithPersistedSessionResolver(persistence),
			session.WithSessionContextFactWriter(persistence),
		},
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: workspace},
	})

	result, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		InitialChat: &InitialChatCreation{
			Settings: serverapi.InitialChatSettings{
				AgentRole:             config.DefaultSubagentRole,
				Supervisor:            serverapi.ChatSettingsSupervisorAfterEdits,
				QuestionsEnabled:      true,
				AutoCompactionEnabled: true,
			},
		},
	})
	if !errors.Is(err, persistenceErr) {
		t.Fatalf("PlanLaunchSession error = %v, want ordinary persistence failure", err)
	}
	if !reflect.DeepEqual(result, PlanResult{}) {
		t.Fatalf("PlanLaunchSession result = %+v, want no Session result", result)
	}
}

func TestPlanLaunchSessionMakesInitialChatVisibleWithoutDraft(t *testing.T) {
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	cfg.Settings.Model = "gpt-5.6-sol"
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(t.Context(), workspace)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	containerDir := filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions")
	if err := os.MkdirAll(containerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll Session container: %v", err)
	}
	service := NewService(launch.Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		StoreOptions:             metadataStore.AuthoritativeSessionStoreOptions(),
		PersistedSessions:        metadataStore,
		ProjectWorkspaceBoundary: metadataStore,
	})

	result, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		InitialChat: &InitialChatCreation{
			Settings: serverapi.InitialChatSettings{
				AgentRole:             config.DefaultSubagentRole,
				Supervisor:            serverapi.ChatSettingsSupervisorAfterEdits,
				QuestionsEnabled:      true,
				AutoCompactionEnabled: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	page, err := metadataStore.ListSessionPage(
		t.Context(),
		binding.ProjectID,
		sessioncontract.SessionCategoryMain,
		0,
		10,
	)
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(page.Sessions) != 1 ||
		page.Sessions[0].SessionID != result.Plan.Descriptor.SessionID() {
		t.Fatalf("visible Sessions = %+v, want created initial Chat", page.Sessions)
	}
}

func TestPlanLaunchSessionRebasesRemovedInitialAgentToReloadedDefaultBaseline(t *testing.T) {
	workspace := t.TempDir()
	containerDir := t.TempDir()
	stale := loadSessionLaunchTestConfig(t, workspace, t.TempDir())
	workerSettings := stale.Settings
	workerSettings.Model = "gpt-5.6-sol"
	workerSettings.ThinkingLevel = "high"
	stale.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings:         workerSettings,
			AgentCallable:    true,
			AgentCallableSet: true,
		},
	}
	current := stale
	current.Settings.Subagents = nil
	current.Settings.Model = "gpt-5.4"
	current.Settings.Reviewer.Frequency = "off"
	current.Settings.ThinkingLevel = "low"
	current.Settings.PriorityRequestMode = false
	current.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: false}
	current.Settings.CompactionMode = config.CompactionModeNone
	persistence := sessiontest.NewPersistence()
	reloads := 0
	service := NewService(launch.Planner{
		Config:                   stale,
		ContainerDir:             containerDir,
		StoreOptions:             persistence.Options(),
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: workspace},
		ReloadConfig: func() (config.App, error) {
			reloads++
			return current, nil
		},
	})
	fast := true
	thinking := "high"

	result, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		InitialChat: &InitialChatCreation{
			Settings: serverapi.InitialChatSettings{
				AgentRole:             "worker",
				Supervisor:            serverapi.ChatSettingsSupervisorAlways,
				Thinking:              &thinking,
				Fast:                  &fast,
				QuestionsEnabled:      true,
				AutoCompactionEnabled: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("ReloadConfig calls = %d, want 1", reloads)
	}
	reopened, err := session.Open(
		filepath.Join(containerDir, result.Plan.Descriptor.SessionID().String()),
		persistence.Options()...,
	)
	if err != nil {
		t.Fatalf("open created Session: %v", err)
	}
	state, err := session.ChatSettingsStateFromMeta(reopened.Meta())
	if err != nil {
		t.Fatalf("ChatSettingsStateFromMeta: %v", err)
	}
	want := sessiontest.CompleteChatSettingsState(
		t,
		config.DefaultSubagentRole,
		"off",
		"low",
		false,
		false,
		true,
	)
	if !reflect.DeepEqual(state, want) {
		gotSettings := session.ChatSettings{
			Supervisor:     *state.Settings.Supervisor,
			Thinking:       *state.Settings.Thinking,
			Fast:           *state.Settings.Fast,
			Questions:      *state.Settings.Questions,
			AutoCompaction: *state.Settings.AutoCompaction,
		}
		wantSettings := session.ChatSettings{
			Supervisor:     *want.Settings.Supervisor,
			Thinking:       *want.Settings.Thinking,
			Fast:           *want.Settings.Fast,
			Questions:      *want.Settings.Questions,
			AutoCompaction: *want.Settings.AutoCompaction,
		}
		t.Fatalf(
			"rebased initial Chat settings = agent:%q settings:%+v, want agent:%q settings:%+v",
			state.Agent,
			gotSettings,
			want.Agent,
			wantSettings,
		)
	}
}

func TestPlanLaunchSessionUsesOneConfigSnapshotForNamedRole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	snapshot := loadSessionLaunchTestConfig(t, workspace, t.TempDir())
	roleSettings := snapshot.Settings
	roleSettings.Model = "gpt-5.3-codex-spark"
	snapshot.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings:         roleSettings,
			Sources:          map[string]string{"model": "file"},
			AgentCallable:    true,
			AgentCallableSet: true,
		},
	}
	reloads := 0
	service := NewService(launch.Planner{
		Config:                   snapshot,
		ContainerDir:             t.TempDir(),
		StoreOptions:             serviceTestPersistence.Options(),
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: snapshot.WorkspaceRoot},
		ReloadConfig: func() (config.App, error) {
			reloads++
			if reloads != 1 {
				t.Fatalf("ReloadConfig called %d times, want exactly once", reloads)
			}
			configPath := filepath.Join(home, config.ConfigDirName, "config.toml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatalf("MkdirAll config dir: %v", err)
			}
			if err := os.WriteFile(configPath, []byte("this is not valid toml = ["), 0o644); err != nil {
				t.Fatalf("WriteFile changed config: %v", err)
			}
			return snapshot, nil
		},
	})
	role := "worker"

	response, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:   launch.ModeHeadless,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		Overrides: serverapi.RunPromptOverrides{
			AgentRole: &role,
			Model:     "gpt-5.4",
		},
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("ReloadConfig called %d times, want exactly once", reloads)
	}
	if response.Plan.ActiveSettings.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want request override from the captured snapshot", response.Plan.ActiveSettings.Model)
	}
}

func TestPlanLaunchSessionRejectsInvalidPreparedNamedTargetBeforeCreatingSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	snapshot := loadSessionLaunchTestConfig(t, workspace, t.TempDir())
	roleSettings := snapshot.Settings
	roleSettings.Model = "gpt-5.3-codex-spark"
	roleSettings.ModelContextWindow = 100
	roleSettings.ContextCompactionThresholdTokens = 101
	snapshot.Settings.Subagents = map[string]config.SubagentRole{
		"invalid": {
			Settings: roleSettings,
			Sources: map[string]string{
				"model":                               "file",
				"model_context_window":                "file",
				"context_compaction_threshold_tokens": "file",
			},
			AgentCallable:    true,
			AgentCallableSet: true,
		},
	}
	containerDir := t.TempDir()
	service := newSessionLaunchTestService(snapshot, containerDir)
	role := "invalid"
	_, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:      launch.ModeHeadless,
		Intent:    serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		Overrides: serverapi.RunPromptOverrides{AgentRole: &role},
	})
	if err == nil {
		t.Fatal("PlanLaunchSession unexpectedly succeeded")
	}
	entries, readErr := os.ReadDir(containerDir)
	if readErr != nil {
		t.Fatalf("ReadDir session container: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid prepared target created session artifacts: %+v", entries)
	}
}

func TestPlanLaunchSessionRejectsUnknownParentBeforeRegisteringStore(t *testing.T) {
	service := newSessionLaunchTestService(config.App{
		WorkspaceRoot:   t.TempDir(),
		PersistenceRoot: t.TempDir(),
		Settings:        config.Settings{Model: "gpt-5"},
	}, t.TempDir())
	unknownParent := mustSessionLaunchIntentID(t, "unknown-parent")
	_, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:   launch.ModeHeadless,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(unknownParent)),
	})
	var denied *serverapi.SubagentLaunchDeniedError
	if !errors.As(err, &denied) || denied.Kind != serverapi.SubagentLaunchDenialParentMissing {
		t.Fatalf("error = %T %v, want parent-missing denial", err, err)
	}
}

func TestPlanLaunchSessionUsesResolvedCallerWorkflowOrigin(t *testing.T) {
	ctx := context.Background()
	persistenceRoot := t.TempDir()
	workspace := t.TempDir()
	meta, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	binding, err := meta.RegisterWorkspaceBinding(ctx, workspace)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	containerDir := filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions")
	workflowCaller, err := session.Create(containerDir, filepath.Base(containerDir), workspace, sessioncontract.SessionCategoryMain, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create workflow caller: %v", err)
	}
	if err := workflowCaller.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable workflow caller: %v", err)
	}
	testsetup.BindSessionToWorkflowTask(t, meta, binding.ProjectID, workflowCaller.Meta().SessionID)
	ordinaryCaller, err := session.Create(containerDir, filepath.Base(containerDir), workspace, sessioncontract.SessionCategoryMain, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create ordinary caller: %v", err)
	}

	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	cfg.Settings.Workflow = config.WorkflowSettings{Subagents: false}
	roleSettings := cfg.Settings
	roleSettings.ThinkingLevel = "high"
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings:         roleSettings,
			Sources:          map[string]string{"thinking_level": "file"},
			AgentCallable:    true,
			AgentCallableSet: true,
		},
	}
	service := NewService(launch.Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		StoreOptions:             meta.AuthoritativeSessionStoreOptions(),
		PersistedSessions:        meta,
		ProjectWorkspaceBoundary: meta,
	})
	worker := "worker"
	workflowCallerID := workflowCaller.Meta().SessionID
	workflowCallerRuntimeID := mustSessionLaunchIntentID(t, workflowCallerID)
	_, err = service.PlanLaunchSession(ctx, PlanRequest{
		Mode:            launch.ModeHeadless,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(workflowCallerRuntimeID)),
		CallerSessionID: &workflowCallerID,
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &worker},
	})
	var denied *serverapi.SubagentLaunchDeniedError
	if !errors.As(err, &denied) || denied.Kind != serverapi.SubagentLaunchDenialNotCallable {
		t.Fatalf("workflow caller error = %T %v, want not-callable denial", err, err)
	}

	ordinaryCallerID := ordinaryCaller.Meta().SessionID
	ordinaryCallerRuntimeID := mustSessionLaunchIntentID(t, ordinaryCallerID)
	if _, err := service.PlanLaunchSession(ctx, PlanRequest{
		Mode:            launch.ModeHeadless,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(ordinaryCallerRuntimeID)),
		CallerSessionID: &ordinaryCallerID,
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &worker},
	}); err != nil {
		t.Fatalf("ordinary caller target: %v", err)
	}
}
func TestPlanLaunchSessionPreservesLockedAgentRoleAndTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	persistedRole := "old_role"
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: &persistedRole}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	persistedSettings := cfg.Settings
	persistedSettings.ThinkingLevel = "low"
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		persistedRole: {
			Settings:         persistedSettings,
			Sources:          map[string]string{"thinking_level": "file"},
			AgentCallable:    true,
			AgentCallableSet: true,
		},
	}
	service := newSessionLaunchTestService(cfg, containerDir)
	requestedRole := "removed_role"

	resp, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides: serverapi.RunPromptOverrides{
			AgentRole: &requestedRole,
			Tools:     "patch,edit",
		},
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if !reflect.DeepEqual(resp.Plan.EnabledTools, []toolspec.ID{toolspec.ToolExecCommand}) {
		t.Fatalf("enabled tools = %+v, want the persisted locked tool set", resp.Plan.EnabledTools)
	}
	continuation := store.Meta().Continuation
	if continuation == nil ||
		continuation.AgentRole == nil ||
		*continuation.AgentRole != persistedRole {
		t.Fatalf("continuation = %+v, want preserved %q role", continuation, persistedRole)
	}
	if resp.Plan.ActiveSettings.ThinkingLevel != "low" {
		t.Fatalf("thinking level = %q, want persisted role value low", resp.Plan.ActiveSettings.ThinkingLevel)
	}
}

func TestPlanLaunchSessionPreparesOmittedSelectedRoleBeforeMaterialization(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	role := "worker"
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: &role}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	roleSettings := cfg.Settings
	roleSettings.ThinkingLevel = "high"
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		role: {
			Settings:         roleSettings,
			Sources:          map[string]string{"thinking_level": "file"},
			AgentCallable:    true,
			AgentCallableSet: true,
		},
	}
	service := newSessionLaunchTestService(cfg, containerDir)

	resp, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:      launch.ModeHeadless,
		Intent:    serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides: serverapi.RunPromptOverrides{Tools: "patch"},
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if resp.Plan.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want persisted worker role value", resp.Plan.ActiveSettings.ThinkingLevel)
	}
	if !reflect.DeepEqual(resp.Plan.EnabledTools, []toolspec.ID{toolspec.ToolPatch}) {
		t.Fatalf("enabled tools = %+v, want prepared patch target", resp.Plan.EnabledTools)
	}
}

func TestPlanLaunchSessionRejectsOmittedTargetBeforeMaterializingSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	containerDir := t.TempDir()
	cfg := loadSessionLaunchTestConfig(t, workspace, t.TempDir())
	cfg.Settings.Model = "claude-sonnet-4.5"
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: true}
	cfg.Source.Sources["tools.patch"] = "default"
	cfg.Source.Sources["tools.edit"] = "default"
	service := newSessionLaunchTestService(cfg, containerDir)

	_, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:      launch.ModeHeadless,
		Intent:    serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		Overrides: serverapi.RunPromptOverrides{Tools: "patch,edit"},
	})
	if !errors.Is(err, launch.ErrPatchEditToolsConflict) {
		t.Fatalf("PlanLaunchSession error = %v, want patch/edit conflict", err)
	}
	entries, readErr := os.ReadDir(containerDir)
	if readErr != nil {
		t.Fatalf("ReadDir session container: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("omitted target failure created session artifacts: %+v", entries)
	}
}

func loadSessionLaunchTestConfig(t *testing.T, workspace string, persistenceRoot string) config.App {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(config.PersistenceRootEnvName, t.TempDir())
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.PersistenceRoot = persistenceRoot
	return cfg
}

func TestPlanLaunchSessionDefaultRoleClearDoesNotRequireAuthState(t *testing.T) {
	workspace := t.TempDir()
	containerDir := t.TempDir()
	service := newSessionLaunchTestService(config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: t.TempDir(),
		Settings:        config.Settings{Model: "gpt-5.6-sol"},
	}, containerDir).WithAuthStateReader(failingAuthStateReader{})

	if _, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:      launch.ModeInteractive,
		Intent:    serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		Overrides: serverapi.RunPromptOverrides{AgentRole: sessionLaunchStringPtr(config.DefaultSubagentRole)},
	}); err != nil {
		t.Fatalf("PlanLaunchSession with default role clear should not read auth state: %v", err)
	}
}

func TestPlanLaunchSessionCanProjectDefaultRoleBeforeValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	roleSettings := cfg.Settings
	roleSettings.Model = "gpt-5.3-codex-spark"
	roleSettings.ContextCompactionThresholdTokens = 200_000
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: roleSettings,
			Sources:  map[string]string{"model": "file", "context_compaction_threshold_tokens": "file"},
		},
	}
	service := newSessionLaunchTestService(cfg, containerDir)

	resp, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:      launch.ModeInteractive,
		Intent:    serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides: serverapi.RunPromptOverrides{AgentRole: sessionLaunchStringPtr(config.DefaultSubagentRole)},
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if resp.Plan.ActiveSettings.Model != cfg.Settings.Model {
		t.Fatalf("model = %q, want base model %q", resp.Plan.ActiveSettings.Model, cfg.Settings.Model)
	}
}

func TestPlanLaunchSessionExplicitCurrentAgentProjectsCurrentEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{
		OpenAIBaseURL: textutil.Value("https://old.example/v1"),
	}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	cfg.Settings.OpenAIBaseURL = "https://new.example/v1"
	service := newSessionLaunchTestService(cfg, containerDir)

	resp, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:      launch.ModeInteractive,
		Intent:    serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides: serverapi.RunPromptOverrides{AgentRole: sessionLaunchStringPtr(config.DefaultSubagentRole)},
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if got := resp.Plan.ActiveSettings.OpenAIBaseURL; got != cfg.Settings.OpenAIBaseURL {
		t.Fatalf("planned base URL = %q, want %q", got, cfg.Settings.OpenAIBaseURL)
	}
}

func TestPlanLaunchSessionAgentSelectionUsesCompletePreparedBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	setSessionLaunchChatSettings(t, store, session.ChatSettings{
		Supervisor:     "off",
		Thinking:       "low",
		Fast:           false,
		Questions:      false,
		AutoCompaction: false,
	})
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	cfg.Settings.OpenAIBaseURL = "https://api.openai.com/v1"
	workerSettings := cfg.Settings
	workerSettings.ProviderOverride = "openai"
	workerSettings.ProviderCapabilities = config.ProviderCapabilitiesOverride{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}
	workerSettings.Reviewer.Frequency = "all"
	workerSettings.ThinkingLevel = "  high  "
	workerSettings.PriorityRequestMode = true
	workerSettings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: workerSettings,
			Sources: map[string]string{
				"reviewer.frequency":    "file",
				"thinking_level":        "file",
				"priority_request_mode": "file",
				"tools.ask_question":    "file",
			},
		},
	}
	service := newSessionLaunchTestService(cfg, containerDir)
	worker := "worker"
	if err := store.SetContinuationContext(session.ContinuationContext{
		OpenAIBaseURL: textutil.Value("https://previous-agent.example/v1"),
	}); err != nil {
		t.Fatalf("seed previous Agent base URL: %v", err)
	}

	selected, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:      launch.ModeInteractive,
		Intent:    serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides: serverapi.RunPromptOverrides{AgentRole: &worker},
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession select worker: %v", err)
	}
	if strings.TrimSpace(selected.Plan.ActiveSettings.ThinkingLevel) != "high" ||
		selected.Plan.ActiveSettings.Reviewer.Frequency != "all" ||
		!selected.Plan.ActiveSettings.PriorityRequestMode ||
		selected.Plan.ActiveSettings.OpenAIBaseURL != "https://api.openai.com/v1" ||
		!selected.Plan.QuestionsEnabled ||
		!selected.Plan.AutoCompactionEnabled {
		t.Fatalf("selected plan = %+v, want complete worker baseline", selected.Plan)
	}
	wantSelection := sessiontest.CompleteChatSettingsState(t, "worker", "all", "high", true, true, true)
	if selected.Plan.ActivationAgentSelection == nil ||
		!reflect.DeepEqual(*selected.Plan.ActivationAgentSelection, wantSelection) {
		t.Fatalf(
			"activation Agent selection = %+v, want %+v",
			selected.Plan.ActivationAgentSelection,
			wantSelection,
		)
	}
	generated, err := sessionPlanSuccessFromResult(selected)
	if err != nil {
		t.Fatalf("encode Session plan: %v", err)
	}
	if generated.Plan.ActivationAgentSelection == nil ||
		generated.Plan.ActivationAgentSelection.Agent != "worker" ||
		generated.Plan.ActivationAgentSelection.Baseline == nil ||
		generated.Plan.ActivationAgentSelection.Baseline.Thinking != "high" {
		t.Fatalf(
			"generated activation Agent selection = %+v, want complete worker selection",
			generated.Plan.ActivationAgentSelection,
		)
	}
}

func TestPlanLaunchSessionProjectsUnavailableAgentWithCompleteDefaultBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	removed := "removed"
	target := sessiontest.CompleteChatSettingsState(t, removed, "all", "high", true, false, false)
	if _, err := store.CommitChatSettingsState(target); err != nil {
		t.Fatalf("seed removed Agent: %v", err)
	}
	if err := store.SetContinuationContext(session.ContinuationContext{
		AgentRole:     &removed,
		OpenAIBaseURL: textutil.Value("https://removed-agent.example/v1"),
	}); err != nil {
		t.Fatalf("seed removed Agent base URL: %v", err)
	}
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	cfg.Settings.OpenAIBaseURL = "https://api.openai.com/v1"
	cfg.Settings.Reviewer.Frequency = "edits"
	cfg.Settings.ThinkingLevel = "medium"
	cfg.Settings.PriorityRequestMode = false
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	service := newSessionLaunchTestService(cfg, containerDir)

	repaired, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession repair removed Agent: %v", err)
	}
	if repaired.Plan.ActiveSettings.OpenAIBaseURL != "https://api.openai.com/v1" ||
		repaired.Plan.ActiveSettings.Reviewer.Frequency != "edits" ||
		repaired.Plan.ActiveSettings.ThinkingLevel != "medium" ||
		repaired.Plan.ActiveSettings.PriorityRequestMode ||
		!repaired.Plan.QuestionsEnabled ||
		!repaired.Plan.AutoCompactionEnabled {
		t.Fatalf("projected default plan = %+v, want complete current baseline", repaired.Plan)
	}
}

func TestPlanLaunchSessionAppliesPersistedChatSettingPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	setSessionLaunchChatSettings(t, store, session.ChatSettings{
		Supervisor:     "off",
		Thinking:       "medium",
		Fast:           false,
		Questions:      false,
		AutoCompaction: false,
	})
	cfg := loadSessionLaunchTestConfig(t, workspace, t.TempDir())
	cfg.Settings.Reviewer.Frequency = "all"
	cfg.Settings.ThinkingLevel = "high"
	cfg.Settings.PriorityRequestMode = true
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	service := newSessionLaunchTestService(cfg, containerDir)

	response, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if response.Plan.ActiveSettings.Reviewer.Frequency != "off" ||
		response.Plan.ActiveSettings.ThinkingLevel != "medium" ||
		response.Plan.ActiveSettings.PriorityRequestMode ||
		response.Plan.QuestionsEnabled ||
		response.Plan.AutoCompactionEnabled {
		t.Fatalf("effective Session Chat settings = %+v questions=%t auto_compaction=%t", response.Plan.ActiveSettings, response.Plan.QuestionsEnabled, response.Plan.AutoCompactionEnabled)
	}

	withoutOverrides := createLaunchTestSession(t, containerDir, "workspace-b", workspace)
	fallback, err := service.PlanLaunchSession(t.Context(), PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, withoutOverrides.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession without overrides: %v", err)
	}
	if fallback.Plan.ActiveSettings.Reviewer.Frequency != "all" ||
		fallback.Plan.ActiveSettings.ThinkingLevel != "high" ||
		!fallback.Plan.ActiveSettings.PriorityRequestMode ||
		!fallback.Plan.QuestionsEnabled ||
		!fallback.Plan.AutoCompactionEnabled {
		t.Fatalf("fallback Session Chat settings = %+v questions=%t auto_compaction=%t", fallback.Plan.ActiveSettings, fallback.Plan.QuestionsEnabled, fallback.Plan.AutoCompactionEnabled)
	}
}

func setSessionLaunchChatSettings(t *testing.T, store *session.Store, settings session.ChatSettings) {
	t.Helper()
	target, err := session.ChatSettingsStateFromCompleteSettings(config.DefaultSubagentRole, settings)
	if err != nil {
		t.Fatalf("build Chat settings: %v", err)
	}
	if _, err := store.CommitChatSettingsState(target); err != nil {
		t.Fatalf("commit Chat settings: %v", err)
	}
}

func assertSessionLaunchChatSettings(t *testing.T, sessionDir string, want session.ChatSettingsState) {
	t.Helper()
	reopened, err := session.Open(sessionDir, serviceTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("reopen Session: %v", err)
	}
	got, err := session.ChatSettingsStateFromMeta(reopened.Meta())
	if err != nil {
		t.Fatalf("ChatSettingsStateFromMeta: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got.Settings)
		wantJSON, _ := json.Marshal(want.Settings)
		t.Fatalf("Chat settings state = agent %q settings %s, want agent %q settings %s", got.Agent, gotJSON, want.Agent, wantJSON)
	}
}

func TestPlanLaunchSessionConfigOnlyOverrideDoesNotSkipInvalidPersistedRoleValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	roleSettings := cfg.Settings
	roleSettings.Model = "gpt-5.3-codex-spark"
	roleSettings.ContextCompactionThresholdTokens = 200_000
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: roleSettings,
			Sources:  map[string]string{"model": "file", "context_compaction_threshold_tokens": "file"},
		},
	}
	service := newSessionLaunchTestService(cfg, containerDir)

	_, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:      launch.ModeInteractive,
		Intent:    serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides: serverapi.RunPromptOverrides{Model: "gpt-5.6-sol"},
	})
	if err == nil {
		t.Fatal("expected invalid persisted role validation to fail")
	}
	if errors.Is(err, serverapi.ErrInvalidRunPromptAgentRole) {
		t.Fatalf("error = %v, want persisted role validation error", err)
	}
}

func TestPlanLaunchSessionHeadlessSelectedSessionAllowsHumanContinuationOfNonCallableRole(t *testing.T) {
	workspace := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	cfg := loadSessionLaunchTestConfig(t, workspace, t.TempDir())
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			AgentCallableSet: true,
			AgentCallable:    false,
		},
	}
	service := newSessionLaunchTestService(cfg, containerDir)

	result, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:   launch.ModeHeadless,
		Intent: serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if result.Plan.Descriptor.SessionID().String() != store.Meta().SessionID {
		t.Fatalf("session id = %q, want selected %q", result.Plan.Descriptor.SessionID(), store.Meta().SessionID)
	}
}

func TestPlanLaunchSessionHeadlessSelectedSessionAllowsRemovedContinuationRole(t *testing.T) {
	workspace := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("removed")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	cfg := loadSessionLaunchTestConfig(t, workspace, t.TempDir())
	service := newSessionLaunchTestService(cfg, containerDir)

	result, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:   launch.ModeHeadless,
		Intent: serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if result.Plan.ActiveSettings.Model != cfg.Settings.Model {
		t.Fatalf("model = %q, want base model %q", result.Plan.ActiveSettings.Model, cfg.Settings.Model)
	}
}

func TestPlanLaunchSessionHeadlessSelectedSessionKeepsOmittedContinuationRoleDefault(t *testing.T) {
	workspace := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	cfg := loadSessionLaunchTestConfig(t, workspace, t.TempDir())
	service := newSessionLaunchTestService(cfg, containerDir)

	result, err := service.PlanLaunchSession(context.Background(), PlanRequest{
		Mode:   launch.ModeHeadless,
		Intent: serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if continuation := result.Plan.Continuation; continuation != nil && continuation.AgentRole != nil {
		t.Fatalf("continuation = %+v, want omitted default role", continuation)
	}
}

func TestPlanLaunchSessionInvalidRoleOverridePrecedesPersistedRoleValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	roleSettings := cfg.Settings
	roleSettings.Model = "gpt-5.3-codex-spark"
	roleSettings.ContextCompactionThresholdTokens = 200_000
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: roleSettings,
			Sources:  map[string]string{"model": "file", "context_compaction_threshold_tokens": "file"},
		},
	}
	service := newSessionLaunchTestService(cfg, containerDir)

	for _, role := range []string{"none", "self"} {
		t.Run(role, func(t *testing.T) {
			_, err := service.PlanLaunchSession(context.Background(), PlanRequest{
				Mode:      launch.ModeInteractive,
				Intent:    serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
				Overrides: serverapi.RunPromptOverrides{AgentRole: sessionLaunchStringPtr(role)},
			})
			if err == nil {
				t.Fatal("expected invalid role override to fail")
			}
			if !errors.Is(err, serverapi.ErrInvalidRunPromptAgentRole) {
				t.Fatalf("error = %v, want malformed role error before persisted role validation", err)
			}
		})
	}
}
