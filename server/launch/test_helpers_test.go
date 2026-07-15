package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/server/auth"
	"core/server/metadata"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

const (
	testWorkspaceContainer = "workspace-a"
	testProjectID          = "project-a"
)

func createTestSession(t *testing.T, workspace string) *session.Store {
	t.Helper()
	return createTestSessionInContainer(t, filepath.Join(t.TempDir(), "projects", testProjectID, "sessions"), testWorkspaceContainer, workspace)
}

func createTestSessionInContainer(t *testing.T, containerDir, workspaceContainer, workspaceRoot string, options ...session.StoreOption) *session.Store {
	t.Helper()
	if len(options) == 0 {
		options = sessiontest.NewPersistence().Options()
	}
	store, err := session.Create(containerDir, workspaceContainer, workspaceRoot, options...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return store
}

func applyRunPromptOverridesNoWarnings(t *testing.T, plan SessionPlan, overrides serverapi.RunPromptOverrides, authState auth.State) SessionPlan {
	t.Helper()
	updated, warnings, err := ApplyRunPromptOverrides(plan, overrides, authState)
	if err != nil {
		t.Fatalf("ApplyRunPromptOverrides: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	return updated
}

func newLoadedConfigPlan(t *testing.T, workspace string, loaded config.App) SessionPlan {
	t.Helper()
	return newSettingsPlanWithSource(t, workspace, loaded.Settings, loaded.Source)
}

func newSettingsPlan(t *testing.T, workspace string, settings config.Settings) SessionPlan {
	t.Helper()
	return newSettingsPlanWithSource(t, workspace, settings, config.SourceReport{})
}

func newSettingsPlanWithSource(t *testing.T, workspace string, settings config.Settings, source config.SourceReport) SessionPlan {
	t.Helper()
	return SessionPlan{
		Store:               createTestSessionInContainer(t, filepath.Join(t.TempDir(), "projects", "project-a", "sessions"), "workspace-a", workspace),
		ActiveSettings:      settings,
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ConfiguredModelName: settings.Model,
		WorkspaceRoot:       workspace,
		Source:              source,
	}
}

func loadLaunchConfig(t *testing.T, workspace string, configLines ...string) config.App {
	t.Helper()
	cfg, _ := loadLaunchConfigWithHome(t, workspace, configLines...)
	return cfg
}

func loadLaunchConfigWithHome(t *testing.T, workspace string, configLines ...string) (config.App, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if len(configLines) > 0 {
		writeHomeConfig(t, home, strings.Join(configLines, "\n"))
	}
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg, home
}

func writeHomeConfig(t *testing.T, home, contents string) {
	t.Helper()
	configPath := filepath.Join(home, config.ConfigDirName, "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func createMetadataBackedSession(
	t *testing.T,
	metadataStore *metadata.Store,
	persistenceRoot string,
	workspaceRoot string,
	continuation session.ContinuationContext,
) *session.Store {
	t.Helper()
	binding, err := metadataStore.RegisterWorkspaceBinding(t.Context(), workspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	containerDir := filepath.Join(filepath.Join(config.App{PersistenceRoot: persistenceRoot}.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	store := createTestSessionInContainer(
		t,
		containerDir,
		filepath.Base(containerDir),
		workspaceRoot,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err := store.SetContinuationContext(continuation); err != nil {
		t.Fatalf("set continuation context: %v", err)
	}
	return store
}

func writeSessionEventArtifact(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("write session events: %v", err)
	}
}

func requireSameSessionDir(t *testing.T, got, want string) {
	t.Helper()
	openedDir, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("EvalSymlinks opened dir: %v", err)
	}
	selectedDir, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("EvalSymlinks selected dir: %v", err)
	}
	if openedDir != selectedDir {
		t.Fatalf("opened session dir = %q, want %q", openedDir, selectedDir)
	}
}
