package sessionlaunch

import (
	"context"
	"errors"
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
	"core/shared/toolspec"
)

type failingAuthStateReader struct{}

func (failingAuthStateReader) CurrentState(context.Context) (auth.State, error) {
	return auth.State{}, errors.New("auth unavailable")
}

func TestServicePlanSessionReadsPromptHistoryFromMetadataOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	store, err := session.Create(containerDir, filepath.Base(containerDir), cfg.WorkspaceRoot, meta.AuthoritativeSessionStoreOptions()...)
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
		Config:       cfg,
		ContainerDir: containerDir,
		StoreOptions: meta.AuthoritativeSessionStoreOptions(),
	}, registry.NewSessionStoreRegistry()).WithPromptHistoryReader(meta)

	resp, err := service.PlanSession(ctx, serverapi.SessionPlanRequest{
		ClientRequestID:   "plan-1",
		Mode:              serverapi.SessionLaunchModeInteractive,
		SelectedSessionID: store.Meta().SessionID,
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
	persistence := sessiontest.NewPersistence()
	stores := registry.NewSessionStoreRegistry()
	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: persistenceRoot,
			Settings:        config.Settings{Model: "gpt-5", OpenAIBaseURL: "http://config.local/v1"},
		},
		ContainerDir: containerDir,
		StoreOptions: persistence.Options(),
	}, stores)

	resp, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		ForceNewSession: true,
		ParentSessionID: "parent-1",
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if resp.Plan.SessionID == "" {
		t.Fatal("expected session id")
	}
	if resp.Plan.WorkspaceRoot != "/tmp/workspace-a" {
		t.Fatalf("workspace root = %q, want /tmp/workspace-a", resp.Plan.WorkspaceRoot)
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
	if store.Meta().ParentSessionID != "parent-1" {
		t.Fatalf("parent session id = %q, want parent-1", store.Meta().ParentSessionID)
	}
}

func TestServicePlanSessionDedupesForceNewSessionRequestID(t *testing.T) {
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	persistence := sessiontest.NewPersistence()
	stores := registry.NewSessionStoreRegistry()
	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: persistenceRoot,
			Settings:        config.Settings{Model: "gpt-5"},
		},
		ContainerDir: containerDir,
		StoreOptions: persistence.Options(),
	}, stores)
	req := serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		ForceNewSession: true,
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

func TestServicePlanSessionAppliesLaunchOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	persistence := sessiontest.NewPersistence()
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	cfg.Settings.Model = "gpt-5.4"
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: true}
	cfg.Source.Sources["model"] = "file"
	cfg.Source.Sources["tools.shell"] = "file"
	cfg.Source.Sources["tools.patch"] = "default"
	service := NewService(launch.Planner{
		Config:       cfg,
		ContainerDir: containerDir,
		StoreOptions: persistence.Options(),
	}, registry.NewSessionStoreRegistry())

	resp, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		ForceNewSession: true,
		Overrides: serverapi.RunPromptOverrides{
			Model:         "gpt-5.3-codex",
			ThinkingLevel: "high",
			Tools:         "patch",
		},
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if resp.Plan.ActiveSettings.Model != "gpt-5.3-codex" || resp.Plan.ConfiguredModelName != "gpt-5.3-codex" {
		t.Fatalf("model = %q configured=%q, want override", resp.Plan.ActiveSettings.Model, resp.Plan.ConfiguredModelName)
	}
	if resp.Plan.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", resp.Plan.ActiveSettings.ThinkingLevel)
	}
	if strings.Join(resp.Plan.EnabledToolIDs, ",") != "patch" {
		t.Fatalf("enabled tools = %+v, want patch", resp.Plan.EnabledToolIDs)
	}
	if resp.Plan.Source.Sources["model"] != "cli" || resp.Plan.Source.Sources["tools.patch"] != "cli" {
		t.Fatalf("source = %+v, want cli override sources", resp.Plan.Source.Sources)
	}
}

func TestServicePlanSessionRespectsLockedContractWhenApplyingOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store, persistence := createSessionLaunchTestSession(t, containerDir, workspace)
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	cfg := loadSessionLaunchTestConfig(t, workspace, persistenceRoot)
	cfg.Settings.Model = "base-model"
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: true}
	service := NewService(launch.Planner{
		Config:       cfg,
		ContainerDir: containerDir,
		StoreOptions: persistence.Options(),
	}, registry.NewSessionStoreRegistry())

	resp, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID:   "req-1",
		Mode:              serverapi.SessionLaunchModeInteractive,
		SelectedSessionID: store.Meta().SessionID,
		Overrides: serverapi.RunPromptOverrides{
			Model: "cli-model",
			Tools: "patch",
		},
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if resp.Plan.ActiveSettings.Model != "locked-model" || resp.Plan.ConfiguredModelName != "base-model" {
		t.Fatalf("model = %q configured=%q, want locked model and base configured", resp.Plan.ActiveSettings.Model, resp.Plan.ConfiguredModelName)
	}
	if strings.Join(resp.Plan.EnabledToolIDs, ",") != "exec_command" {
		t.Fatalf("enabled tools = %+v, want locked shell runtime id", resp.Plan.EnabledToolIDs)
	}
}

func TestServicePlanSessionPropagatesOverrideToolConflict(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistence := sessiontest.NewPersistence()
	cfg := loadSessionLaunchTestConfig(t, workspace, t.TempDir())
	cfg.Settings.Model = "claude-sonnet-4.5"
	cfg.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: true}
	cfg.Source.Sources["tools.patch"] = "default"
	cfg.Source.Sources["tools.edit"] = "default"
	service := NewService(launch.Planner{
		Config:       cfg,
		ContainerDir: t.TempDir(),
		StoreOptions: persistence.Options(),
	}, registry.NewSessionStoreRegistry())

	_, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		ForceNewSession: true,
		Overrides:       serverapi.RunPromptOverrides{Tools: "patch,edit"},
	})
	if err == nil || !errors.Is(err, launch.ErrPatchEditToolsConflict) {
		t.Fatalf("error = %v, want tool conflict", err)
	}
}

func loadSessionLaunchTestConfig(t *testing.T, workspace string, persistenceRoot string) config.App {
	t.Helper()
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.PersistenceRoot = persistenceRoot
	return cfg
}

func createSessionLaunchTestSession(t *testing.T, containerDir, workspace string) (*session.Store, *sessiontest.Persistence) {
	t.Helper()
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(containerDir, "workspace-a", workspace, persistence.Options()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	return store, persistence
}

func TestServicePlanSessionDefaultRoleClearDoesNotRequireAuthState(t *testing.T) {
	workspace := t.TempDir()
	containerDir := t.TempDir()
	persistence := sessiontest.NewPersistence()
	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Model: "gpt-5.6-sol"},
		},
		ContainerDir: containerDir,
		StoreOptions: persistence.Options(),
	}, registry.NewSessionStoreRegistry()).WithAuthStateReader(failingAuthStateReader{})

	if _, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		ForceNewSession: true,
		Overrides:       serverapi.RunPromptOverrides{AgentRole: config.DefaultSubagentRole},
	}); err != nil {
		t.Fatalf("PlanSession with default role clear should not read auth state: %v", err)
	}
}

func TestServicePlanSessionCanClearInvalidPersistedRoleBeforeValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store, persistence := createSessionLaunchTestSession(t, containerDir, workspace)
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
		StoreOptions: persistence.Options(),
	}, registry.NewSessionStoreRegistry())

	resp, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID:   "req-1",
		Mode:              serverapi.SessionLaunchModeInteractive,
		SelectedSessionID: store.Meta().SessionID,
		Overrides:         serverapi.RunPromptOverrides{AgentRole: config.DefaultSubagentRole},
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if resp.Plan.ActiveSettings.Model != cfg.Settings.Model {
		t.Fatalf("model = %q, want base model %q", resp.Plan.ActiveSettings.Model, cfg.Settings.Model)
	}
	reopened, err := session.Open(store.Dir(), persistence.Options()...)
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
	store, persistence := createSessionLaunchTestSession(t, containerDir, workspace)
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
		StoreOptions: persistence.Options(),
	}, registry.NewSessionStoreRegistry())

	_, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID:   "req-1",
		Mode:              serverapi.SessionLaunchModeInteractive,
		SelectedSessionID: store.Meta().SessionID,
		Overrides:         serverapi.RunPromptOverrides{Model: "gpt-5.6-sol"},
	})
	if err == nil {
		t.Fatal("expected invalid persisted role validation to fail")
	}
	if errors.Is(err, serverapi.ErrInvalidRunPromptAgentRole) {
		t.Fatalf("error = %v, want persisted role validation error", err)
	}
}

func TestServicePlanSessionInvalidRoleOverridePrecedesPersistedRoleValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	containerDir := t.TempDir()
	store, persistence := createSessionLaunchTestSession(t, containerDir, workspace)
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
		StoreOptions: persistence.Options(),
	}, registry.NewSessionStoreRegistry())

	for _, role := range []string{"none", "self"} {
		t.Run(role, func(t *testing.T) {
			_, err := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
				ClientRequestID:   "req-" + role,
				Mode:              serverapi.SessionLaunchModeInteractive,
				SelectedSessionID: store.Meta().SessionID,
				Overrides:         serverapi.RunPromptOverrides{AgentRole: role},
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
