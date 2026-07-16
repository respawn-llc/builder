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

	"core/server/auth"
	"core/server/launch"
	"core/server/metadata"
	"core/server/registry"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
)

type failingAuthStateReader struct{}

var serviceTestPersistence = sessiontest.NewPersistence()

func createLaunchTestSession(t *testing.T, containerDir, name, workspace string) *session.Store {
	t.Helper()
	store, err := session.Create(containerDir, name, workspace, sessioncontract.SessionCategoryMain, serviceTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return store
}

func (failingAuthStateReader) CurrentState(context.Context) (auth.State, error) {
	return auth.State{}, errors.New("auth unavailable")
}

type countingStoreRegistrar struct {
	registrations int
}

func (r *countingStoreRegistrar) RegisterStore(*session.Store) {
	r.registrations++
}

func sessionLaunchStringPtr(value string) *string {
	return &value
}

func TestServicePlanSessionReadsPromptHistoryFromMetadataOnly(t *testing.T) {
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
	if _, _, err := store.AppendEvent("", "prompt_history", map[string]any{"text": "json-history"}); err != nil {
		t.Fatalf("append legacy prompt history event: %v", err)
	}
	if _, _, err := meta.RecordPromptHistoryEntry(ctx, metadata.PromptHistoryEntry{
		SessionID: store.Meta().SessionID,
		SourceID:  "req-1",
		Text:      "db-history",
	}); err != nil {
		t.Fatalf("record metadata prompt history: %v", err)
	}
	service := NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      meta.AuthoritativeSessionStoreOptions(),
		PersistedSessions: meta,
	}, registry.NewSessionStoreRegistry()).WithPromptHistoryReader(meta)

	resp, err := service.PlanSession(ctx, serverapi.SessionPlanRequest{
		ClientRequestID: "plan-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if !reflect.DeepEqual(resp.Plan.PromptHistory, []string{"db-history"}) {
		t.Fatalf("prompt history = %+v, want metadata only", resp.Plan.PromptHistory)
	}
}

func TestServicePlanSessionRegistersStoreAndReturnsPlan(t *testing.T) {
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	stores := registry.NewSessionStoreRegistry()
	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: persistenceRoot,
			Settings:        config.Settings{Model: "gpt-5", OpenAIBaseURL: "http://config.local/v1"},
		},
		ContainerDir: containerDir,
		StoreOptions: serviceTestPersistence.Options(),
	}, stores)

	resp, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if resp.Plan.SessionID == "" {
		t.Fatal("expected session id")
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal plan response: %v", err)
	}
	var wire struct {
		Plan map[string]json.RawMessage `json:"plan"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("json.Unmarshal plan response: %v", err)
	}
	if _, exists := wire.Plan["workspace_root"]; exists {
		t.Fatalf("session plan exposed parallel raw workspace authority: %s", encoded)
	}
	if resp.Plan.ActiveSettings.OpenAIBaseURL != "http://config.local/v1" {
		t.Fatalf("active OpenAI base URL = %q, want http://config.local/v1", resp.Plan.ActiveSettings.OpenAIBaseURL)
	}
	store, err := stores.ResolveStore(context.Background(), resp.Plan.SessionID)
	if err != nil {
		t.Fatalf("ResolveStore: %v", err)
	}
	if store == nil {
		t.Fatal("expected planned session in registry")
	}
}

func TestServicePlanSessionDedupesForceNewSessionRequestID(t *testing.T) {
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	stores := &countingStoreRegistrar{}
	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: persistenceRoot,
			Settings:        config.Settings{Model: "gpt-5"},
		},
		ContainerDir: containerDir,
		StoreOptions: serviceTestPersistence.Options(),
	}, stores)
	req := serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	}
	first, err := service.PlanSession(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSession first: %v", err)
	}
	second, err := service.PlanSession(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSession second: %v", err)
	}
	if first.Plan.SessionID != second.Plan.SessionID {
		t.Fatalf("session ids = %q and %q, want stable replay", first.Plan.SessionID, second.Plan.SessionID)
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
		Config:       snapshot,
		ContainerDir: t.TempDir(),
		StoreOptions: serviceTestPersistence.Options(),
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
	}, registry.NewSessionStoreRegistry())
	role := "worker"

	response, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "snapshot-1",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		Overrides: serverapi.RunPromptOverrides{
			AgentRole: &role,
			Model:     "gpt-5.4",
		},
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
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
	stores := registry.NewSessionStoreRegistry()
	service := NewService(launch.Planner{
		Config:       snapshot,
		ContainerDir: containerDir,
		StoreOptions: serviceTestPersistence.Options(),
	}, stores)
	role := "invalid"
	_, err := service.PlanLaunchSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "invalid-prepared-target",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &role},
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

func TestSessionPlanMemoRequestUsesCanonicalNullableValues(t *testing.T) {
	base := sessionPlanMemoRequest{
		Mode:   serverapi.SessionLaunchModeHeadless,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	}
	if !sameSessionPlanMemoRequest(base, base) {
		t.Fatal("identical canonical request must match")
	}
	explicitDefault := base
	role := config.DefaultSubagentRole
	explicitDefault.Overrides = serverapi.RunPromptOverridesKey{
		AgentRole: serverapi.OptionalStringKey{Present: true, Value: role},
	}
	if sameSessionPlanMemoRequest(base, explicitDefault) {
		t.Fatal("omitted and explicit default selectors must not share a memo entry")
	}
}

func TestPlanLaunchSessionRejectsUnknownParentBeforeRegisteringStore(t *testing.T) {
	stores := &countingStoreRegistrar{}
	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   t.TempDir(),
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Model: "gpt-5"},
		},
		ContainerDir: t.TempDir(),
		StoreOptions: serviceTestPersistence.Options(),
	}, stores)
	unknownParent := mustSessionLaunchIntentID(t, "unknown-parent")
	_, err := service.PlanLaunchSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(unknownParent)),
	})
	var denied *serverapi.SubagentLaunchDeniedError
	if !errors.As(err, &denied) || denied.Kind != serverapi.SubagentLaunchDenialParentMissing {
		t.Fatalf("error = %T %v, want parent-missing denial", err, err)
	}
	if stores.registrations != 0 {
		t.Fatalf("store registrations = %d, want 0", stores.registrations)
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
	if err := workflowCaller.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1"}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
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
	stores := &countingStoreRegistrar{}
	service := NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      meta.AuthoritativeSessionStoreOptions(),
		PersistedSessions: meta,
	}, stores)
	worker := "worker"
	workflowCallerID := workflowCaller.Meta().SessionID
	workflowCallerRuntimeID := mustSessionLaunchIntentID(t, workflowCallerID)
	_, err = service.PlanLaunchSession(ctx, serverapi.SessionPlanRequest{
		ClientRequestID: "workflow-caller-target",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(workflowCallerRuntimeID)),
		CallerSessionID: &workflowCallerID,
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &worker},
	})
	var denied *serverapi.SubagentLaunchDeniedError
	if !errors.As(err, &denied) || denied.Kind != serverapi.SubagentLaunchDenialNotCallable {
		t.Fatalf("workflow caller error = %T %v, want not-callable denial", err, err)
	}
	if stores.registrations != 0 {
		t.Fatalf("workflow denial store registrations = %d, want 0", stores.registrations)
	}

	ordinaryCallerID := ordinaryCaller.Meta().SessionID
	ordinaryCallerRuntimeID := mustSessionLaunchIntentID(t, ordinaryCallerID)
	if _, err := service.PlanLaunchSession(ctx, serverapi.SessionPlanRequest{
		ClientRequestID: "ordinary-caller-target",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(ordinaryCallerRuntimeID)),
		CallerSessionID: &ordinaryCallerID,
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &worker},
	}); err != nil {
		t.Fatalf("ordinary caller target: %v", err)
	}
	if stores.registrations != 1 {
		t.Fatalf("ordinary launch store registrations = %d, want 1", stores.registrations)
	}
}
func TestServicePlanSessionRetainsLockedToolsForPreparedNamedTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	role := "worker"
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: &role}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
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
	service := NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      serviceTestPersistence.Options(),
		PersistedSessions: serviceTestPersistence,
	}, registry.NewSessionStoreRegistry())

	resp, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "locked-named-tools",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides: serverapi.RunPromptOverrides{
			AgentRole: &role,
			Tools:     "patch,edit",
		},
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if strings.Join(resp.Plan.EnabledToolIDs, ",") != "exec_command" {
		t.Fatalf("enabled tools = %+v, want the persisted locked tool set", resp.Plan.EnabledToolIDs)
	}
}

func TestServicePlanSessionPreparesOmittedSelectedRoleBeforeMaterialization(t *testing.T) {
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
	service := NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      serviceTestPersistence.Options(),
		PersistedSessions: serviceTestPersistence,
	}, registry.NewSessionStoreRegistry())

	resp, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "omitted-selected-role",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides:       serverapi.RunPromptOverrides{Tools: "patch"},
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if resp.Plan.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want persisted worker role value", resp.Plan.ActiveSettings.ThinkingLevel)
	}
	if strings.Join(resp.Plan.EnabledToolIDs, ",") != "patch" {
		t.Fatalf("enabled tools = %+v, want prepared patch target", resp.Plan.EnabledToolIDs)
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
	stores := &countingStoreRegistrar{}
	service := NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      serviceTestPersistence.Options(),
		PersistedSessions: serviceTestPersistence,
	}, stores)

	_, err := service.PlanLaunchSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "omitted-target-conflict",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		Overrides:       serverapi.RunPromptOverrides{Tools: "patch,edit"},
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
	if stores.registrations != 0 {
		t.Fatalf("omitted target failure registered %d stores", stores.registrations)
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

func TestServicePlanSessionDefaultRoleClearDoesNotRequireAuthState(t *testing.T) {
	workspace := t.TempDir()
	containerDir := t.TempDir()
	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Model: "gpt-5.6-sol"},
		},
		ContainerDir: containerDir,
		StoreOptions: serviceTestPersistence.Options(),
	}, registry.NewSessionStoreRegistry()).WithAuthStateReader(failingAuthStateReader{})

	if _, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		Overrides:       serverapi.RunPromptOverrides{AgentRole: sessionLaunchStringPtr(config.DefaultSubagentRole)},
	}); err != nil {
		t.Fatalf("PlanSession with default role clear should not read auth state: %v", err)
	}
}

func TestServicePlanSessionCanClearInvalidPersistedRoleBeforeValidation(t *testing.T) {
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
	service := NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      serviceTestPersistence.Options(),
		PersistedSessions: serviceTestPersistence,
	}, registry.NewSessionStoreRegistry())

	resp, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides:       serverapi.RunPromptOverrides{AgentRole: sessionLaunchStringPtr(config.DefaultSubagentRole)},
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if resp.Plan.ActiveSettings.Model != cfg.Settings.Model {
		t.Fatalf("model = %q, want base model %q", resp.Plan.ActiveSettings.Model, cfg.Settings.Model)
	}
	reopened, err := session.Open(store.Dir(), serviceTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	if got := reopened.Meta().Continuation; got != nil && got.AgentRole != nil {
		t.Fatalf("continuation = %+v, want cleared agent role", got)
	}
}

func TestServicePlanSessionConfigOnlyOverrideDoesNotSkipInvalidPersistedRoleValidation(t *testing.T) {
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
	service := NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      serviceTestPersistence.Options(),
		PersistedSessions: serviceTestPersistence,
	}, registry.NewSessionStoreRegistry())

	_, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides:       serverapi.RunPromptOverrides{Model: "gpt-5.6-sol"},
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
	service := NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      serviceTestPersistence.Options(),
		PersistedSessions: serviceTestPersistence,
	}, &countingStoreRegistrar{})

	result, err := service.PlanLaunchSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-persisted-role",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if result.Plan.Store.Meta().SessionID != store.Meta().SessionID {
		t.Fatalf("session id = %q, want selected %q", result.Plan.Store.Meta().SessionID, store.Meta().SessionID)
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
	service := NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      serviceTestPersistence.Options(),
		PersistedSessions: serviceTestPersistence,
	}, &countingStoreRegistrar{})

	result, err := service.PlanLaunchSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-removed-persisted-role",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
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
	service := NewService(launch.Planner{
		Config:            cfg,
		ContainerDir:      containerDir,
		StoreOptions:      serviceTestPersistence.Options(),
		PersistedSessions: serviceTestPersistence,
	}, &countingStoreRegistrar{})

	result, err := service.PlanLaunchSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-omitted-persisted-role",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if continuation := result.Plan.Store.Meta().Continuation; continuation != nil && continuation.AgentRole != nil {
		t.Fatalf("continuation = %+v, want omitted default role", continuation)
	}
}

func TestServicePlanSessionInvalidRoleOverridePrecedesPersistedRoleValidation(t *testing.T) {
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
	service := NewService(launch.Planner{
		Config:       cfg,
		ContainerDir: containerDir,
		StoreOptions: serviceTestPersistence.Options(),
	}, registry.NewSessionStoreRegistry())

	for _, role := range []string{"none", "self"} {
		t.Run(role, func(t *testing.T) {
			_, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
				ClientRequestID: "req-" + role,
				Mode:            serverapi.SessionLaunchModeInteractive,
				Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
				Overrides:       serverapi.RunPromptOverrides{AgentRole: sessionLaunchStringPtr(role)},
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
