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
	"core/server/sessionruntime"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
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

func (failingAuthStateReader) StoredState(context.Context) (auth.State, error) {
	return auth.EmptyState(), nil
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
	}).WithRuntimeAuthority(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    serviceTestPersistence.Options(),
	}))
}

type sessionLaunchBoundaryResolver struct{ root string }

func (r sessionLaunchBoundaryResolver) ResolveSessionProjectWorkspaceBoundary(context.Context, string) (metadata.ProjectWorkspaceBoundary, error) {
	return metadata.ProjectWorkspaceBoundary{ProjectID: "test-project", Workspaces: []metadata.ProjectWorkspace{{CanonicalRoot: r.root}}}, nil
}

func (r sessionLaunchBoundaryResolver) ListManagedWorktreeRoots(context.Context) ([]string, error) {
	return nil, nil
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
	if _, _, err := meta.RecordPromptHistoryEntry(ctx, metadata.PromptHistoryEntry{
		SessionID: store.Meta().SessionID,
		SourceID:  "req-1",
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
	}).WithPromptHistoryReader(meta).WithRuntimeAuthority(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    meta.AuthoritativeSessionStoreOptions(),
	}))

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

func TestServicePlanSessionProjectsTypedOptionalSessionName(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "sessions")
	store := createLaunchTestSession(t, containerDir, "initial", workspace)
	service := newSessionLaunchTestService(config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings:        config.Settings{Model: "gpt-5"},
	}, containerDir)
	request := serverapi.SessionPlanRequest{
		ClientRequestID: "typed-session-name",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
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
	request.ClientRequestID = "typed-session-name-present"
	present, err := service.PlanSession(t.Context(), request)
	if err != nil {
		t.Fatalf("PlanSession present name: %v", err)
	}
	if present.Plan.SessionName == nil || *present.Plan.SessionName != title {
		t.Fatalf("present session name = %v, want %q", present.Plan.SessionName, title)
	}
}

func TestServicePlanSessionReturnsPlanWithoutRegisteringStore(t *testing.T) {
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	service := newSessionLaunchTestService(config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: persistenceRoot,
		Settings:        config.Settings{Model: "gpt-5", OpenAIBaseURL: "http://config.local/v1"},
	}, containerDir)

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
}

func TestServicePlanSessionDedupesForceNewSessionRequestID(t *testing.T) {
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	service := newSessionLaunchTestService(config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: persistenceRoot,
		Settings:        config.Settings{Model: "gpt-5"},
	}, containerDir)
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
	service := newSessionLaunchTestService(snapshot, containerDir)
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
	service := newSessionLaunchTestService(config.App{
		WorkspaceRoot:   t.TempDir(),
		PersistenceRoot: t.TempDir(),
		Settings:        config.Settings{Model: "gpt-5"},
	}, t.TempDir())
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
	service := newSessionLaunchTestService(cfg, containerDir)

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
	service := newSessionLaunchTestService(cfg, containerDir)

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
	service := newSessionLaunchTestService(cfg, containerDir)

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
	service := newSessionLaunchTestService(config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: t.TempDir(),
		Settings:        config.Settings{Model: "gpt-5.6-sol"},
	}, containerDir).WithAuthStateReader(failingAuthStateReader{})

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
	service := newSessionLaunchTestService(cfg, containerDir)

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

func TestServicePlanSessionExplicitCurrentAgentRefreshesContinuationEndpoint(t *testing.T) {
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

	resp, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "refresh-current-agent-continuation",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides:       serverapi.RunPromptOverrides{AgentRole: sessionLaunchStringPtr(config.DefaultSubagentRole)},
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if got := resp.Plan.ActiveSettings.OpenAIBaseURL; got != cfg.Settings.OpenAIBaseURL {
		t.Fatalf("planned base URL = %q, want %q", got, cfg.Settings.OpenAIBaseURL)
	}
	reopened, err := session.Open(store.Dir(), serviceTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	continuation := reopened.Meta().Continuation
	if continuation == nil || continuation.OpenAIBaseURL == nil || *continuation.OpenAIBaseURL != cfg.Settings.OpenAIBaseURL {
		t.Fatalf("persisted continuation = %+v, want refreshed base URL %q", continuation, cfg.Settings.OpenAIBaseURL)
	}
}

func TestServicePlanSessionAgentSelectionPersistsCompletePreparedBaseline(t *testing.T) {
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
	workerSettings.ThinkingLevel = "  custom-depth  "
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

	if _, err := service.PlanSession(t.Context(), serverapi.SessionPlanRequest{
		ClientRequestID: "select-worker",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
		Overrides:       serverapi.RunPromptOverrides{AgentRole: &worker},
	}); err != nil {
		t.Fatalf("PlanSession select worker: %v", err)
	}
	assertSessionLaunchChatSettings(t, store.Dir(), session.ChatSettingsState{
		Agent: "worker",
		Settings: &session.ChatSettingsOverrides{
			Supervisor:     sessionLaunchStringPtr("all"),
			Thinking:       sessionLaunchStringPtr("custom-depth"),
			Fast:           textutil.Value(true),
			Questions:      textutil.Value(true),
			AutoCompaction: textutil.Value(true),
		},
	})
	selected, err := session.Open(store.Dir(), serviceTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("reopen selected Agent Session: %v", err)
	}
	if continuation := selected.Meta().Continuation; continuation == nil || continuation.OpenAIBaseURL != nil {
		t.Fatalf("selected Agent continuation = %+v, want previous base URL cleared", continuation)
	}

	second, err := service.PlanSession(t.Context(), serverapi.SessionPlanRequest{
		ClientRequestID: "observe-worker",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanSession observe worker: %v", err)
	}
	if strings.TrimSpace(second.Plan.ActiveSettings.ThinkingLevel) != "custom-depth" ||
		second.Plan.ActiveSettings.Reviewer.Frequency != "all" ||
		!second.Plan.ActiveSettings.PriorityRequestMode ||
		second.Plan.ActiveSettings.OpenAIBaseURL != "https://api.openai.com/v1" {
		t.Fatalf("second plan active settings = %+v, want selected worker baseline", second.Plan.ActiveSettings)
	}
}

func TestServicePlanSessionRepairsUnavailableAgentWithCompleteDefaultBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	removed := "removed"
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{Agent: &session.ChatAgentSelection{
		Agent: removed,
		Baseline: session.ChatSettings{
			Supervisor:     "all",
			Thinking:       "stale-custom",
			Fast:           true,
			Questions:      false,
			AutoCompaction: false,
		},
	}}); err != nil {
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
	cfg.Settings.ThinkingLevel = "default-custom"
	cfg.Settings.PriorityRequestMode = false
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolAskQuestion: true}
	service := newSessionLaunchTestService(cfg, containerDir)

	repaired, err := service.PlanSession(t.Context(), serverapi.SessionPlanRequest{
		ClientRequestID: "repair-removed",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanSession repair removed Agent: %v", err)
	}
	assertSessionLaunchChatSettings(t, store.Dir(), session.ChatSettingsState{
		Agent: config.DefaultSubagentRole,
		Settings: &session.ChatSettingsOverrides{
			Supervisor:     sessionLaunchStringPtr("edits"),
			Thinking:       sessionLaunchStringPtr("default-custom"),
			Fast:           textutil.Value(false),
			Questions:      textutil.Value(true),
			AutoCompaction: textutil.Value(true),
		},
	})
	reopened, err := session.Open(store.Dir(), serviceTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("reopen repaired Agent Session: %v", err)
	}
	if continuation := reopened.Meta().Continuation; continuation != nil {
		t.Fatalf("repaired Agent continuation = %+v, want default Agent inheriting current config", continuation)
	}
	if repaired.Plan.ActiveSettings.OpenAIBaseURL != "https://api.openai.com/v1" {
		t.Fatalf("repaired Agent base URL = %q, want current config", repaired.Plan.ActiveSettings.OpenAIBaseURL)
	}
}

func TestServicePlanSessionAppliesPersistedChatSettingPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "workspace-a", workspace)
	setSessionLaunchChatSettings(t, store, session.ChatSettings{
		Supervisor:     "off",
		Thinking:       "custom-depth",
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

	response, err := service.PlanSession(t.Context(), serverapi.SessionPlanRequest{
		ClientRequestID: "persisted-chat-settings",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if response.Plan.ActiveSettings.Reviewer.Frequency != "off" ||
		response.Plan.ActiveSettings.ThinkingLevel != "custom-depth" ||
		response.Plan.ActiveSettings.PriorityRequestMode ||
		response.Plan.QuestionsEnabled ||
		response.Plan.AutoCompactionEnabled {
		t.Fatalf("effective Session Chat settings = %+v questions=%t auto_compaction=%t", response.Plan.ActiveSettings, response.Plan.QuestionsEnabled, response.Plan.AutoCompactionEnabled)
	}

	withoutOverrides := createLaunchTestSession(t, containerDir, "workspace-b", workspace)
	fallback, err := service.PlanSession(t.Context(), serverapi.SessionPlanRequest{
		ClientRequestID: "current-chat-settings",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, withoutOverrides.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanSession without overrides: %v", err)
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
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{
		Supervisor:     sessionLaunchStringPtr(settings.Supervisor),
		Thinking:       sessionLaunchStringPtr(settings.Thinking),
		Fast:           textutil.Value(settings.Fast),
		Questions:      textutil.Value(settings.Questions),
		AutoCompaction: textutil.Value(settings.AutoCompaction),
	}); err != nil {
		t.Fatalf("MutateChatSettings: %v", err)
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
	service := newSessionLaunchTestService(cfg, containerDir)

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
	service := newSessionLaunchTestService(cfg, containerDir)

	result, err := service.PlanLaunchSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-persisted-role",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
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
	service := newSessionLaunchTestService(cfg, containerDir)

	result, err := service.PlanLaunchSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-omitted-persisted-role",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanLaunchSession: %v", err)
	}
	if continuation := result.Plan.Continuation; continuation != nil && continuation.AgentRole != nil {
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
	service := newSessionLaunchTestService(cfg, containerDir)

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
