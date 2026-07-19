package startupconfig

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"core/server/metadata"
	"core/server/session"
	"core/shared/config"
	"core/shared/sessioncontract"
)

func TestResolveInteractiveConfigReturnsServerAndClientSnapshots(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte("[hooks.client]\nlifecycle = [\"notify\", \"fixed\"]\n"), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	resolved, err := ResolveInteractiveConfig(Request{
		WorkspaceRoot: workspace,
		LoadOptions: config.LoadOptions{
			ConfigRoot: root,
			Model:      "resolved-model",
		},
	})
	if err != nil {
		t.Fatalf("ResolveInteractiveConfig: %v", err)
	}
	if resolved.Server.Settings.Model != "resolved-model" {
		t.Fatalf("server model = %q, want resolved-model", resolved.Server.Settings.Model)
	}
	if got, want := resolved.Client.Hooks.LifecycleCommand(), []string{"notify", "fixed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("client lifecycle command = %#v, want %#v", got, want)
	}
}

func TestResolveInteractiveConfigRejectsInvalidClientConfiguration(t *testing.T) {
	tests := map[string]func(*testing.T, string, string){
		"malformed global file": func(t *testing.T, root string, _ string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("[hooks.client\n"), 0o644); err != nil {
				t.Fatalf("write malformed config: %v", err)
			}
		},
		"unreadable global path": func(t *testing.T, root string, _ string) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(root, "config.toml"), 0o755); err != nil {
				t.Fatalf("create config directory: %v", err)
			}
		},
		"type-invalid global key": func(t *testing.T, root string, _ string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("[hooks.client]\nlifecycle = \"notify\"\n"), 0o644); err != nil {
				t.Fatalf("write type-invalid config: %v", err)
			}
		},
		"unknown global key": func(t *testing.T, root string, _ string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("unknown_startup_key = true\n"), 0o644); err != nil {
				t.Fatalf("write unknown config: %v", err)
			}
		},
		"client key in workspace": func(t *testing.T, _ string, workspace string) {
			t.Helper()
			configDir := filepath.Join(workspace, config.ConfigDirName)
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatalf("create workspace config dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[hooks.client]\nlifecycle = [\"notify\"]\n"), 0o644); err != nil {
				t.Fatalf("write workspace config: %v", err)
			}
		},
	}
	for name, arrange := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			workspace := t.TempDir()
			arrange(t, root, workspace)
			if _, err := ResolveInteractiveConfig(Request{
				WorkspaceRoot: workspace,
				LoadOptions:   config.LoadOptions{ConfigRoot: root},
			}); err == nil {
				t.Fatal("invalid interactive configuration succeeded")
			}
		})
	}
}

func TestResolveWorkspaceRootUsesCWDWhenEmpty(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	got, err := ResolveWorkspaceRoot(" ")
	if err != nil {
		t.Fatalf("ResolveWorkspaceRoot: %v", err)
	}
	if got != cwd {
		t.Fatalf("workspace root = %q, want %q", got, cwd)
	}
}

func TestResolveRunPromptConfigWrapsMissingImplicitWorkspaceContextSession(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	_, err := ResolveRunPromptConfig(Request{
		WorkspaceRoot:             workspace,
		WorkspaceContextSessionID: "missing-context-session",
		LoadOptions:               config.LoadOptions{ConfigRoot: t.TempDir()},
	})
	if err == nil {
		t.Fatal("expected missing context session error")
	}
	if !errors.Is(err, sessioncontract.ErrSessionNotFound) {
		t.Fatalf("error = %v, want ErrSessionNotFound", err)
	}
	if !errors.Is(err, ErrWorkspaceContextSessionMissing) {
		t.Fatalf("error = %v, want workspace context guidance", err)
	}
}

func TestResolveRunPromptConfigKeepsExplicitSessionLookupStrict(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	_, err := ResolveRunPromptConfig(Request{
		WorkspaceRoot: workspace,
		SessionID:     "missing-explicit-session",
		LoadOptions:   config.LoadOptions{ConfigRoot: t.TempDir()},
	})
	if !errors.Is(err, sessioncontract.ErrSessionNotFound) {
		t.Fatalf("error = %v, want ErrSessionNotFound", err)
	}
	if errors.Is(err, ErrWorkspaceContextSessionMissing) {
		t.Fatalf("explicit session error should not be rewritten as workspace context guidance: %v", err)
	}
}

func TestResolveSessionConfigAppliesLoadOptions(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := ResolveSessionConfig(Request{
		WorkspaceRoot: workspace,
		LoadOptions: config.LoadOptions{
			Model:         "gpt-5",
			ThinkingLevel: "high",
		},
	})
	if err != nil {
		t.Fatalf("ResolveSessionConfig: %v", err)
	}
	if cfg.Settings.Model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", cfg.Settings.Model)
	}
	if cfg.Settings.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", cfg.Settings.ThinkingLevel)
	}
}

func TestResolveRunPromptConfigThreadsPersistenceRoot(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()

	res, err := ResolveRunPromptConfig(Request{
		WorkspaceRoot: workspace,
		LoadOptions:   config.LoadOptions{ConfigRoot: root},
	})
	if err != nil {
		t.Fatalf("ResolveRunPromptConfig: %v", err)
	}
	if res.Config.PersistenceRoot != root {
		t.Fatalf("persistence root = %q, want %q", res.Config.PersistenceRoot, root)
	}
}

func TestResolveRunPromptConfigClassifiesValidatedProvenance(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	meta, err := metadata.Open(root)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	binding, err := meta.RegisterWorkspaceBinding(t.Context(), workspace)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	containerDir := filepath.Join(root, "projects", binding.ProjectID, "sessions")
	store, err := session.Create(containerDir, filepath.Base(containerDir), workspace, sessioncontract.SessionCategoryMain, meta.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}

	kentSession, err := ResolveRunPromptConfig(Request{
		WorkspaceRoot:             workspace,
		WorkspaceRootExplicit:     true,
		WorkspaceContextSessionID: store.Meta().SessionID,
		LoadOptions:               config.LoadOptions{ConfigRoot: root},
	})
	if err != nil {
		t.Fatalf("ResolveRunPromptConfig Kent session: %v", err)
	}
	if kentSession.CallerContext.Kind != CallerKindKentSession {
		t.Fatalf("caller kind = %q, want Kent session", kentSession.CallerContext.Kind)
	}

	human, err := ResolveRunPromptConfig(Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		LoadOptions:           config.LoadOptions{ConfigRoot: root},
	})
	if err != nil {
		t.Fatalf("ResolveRunPromptConfig human: %v", err)
	}
	if human.CallerContext.Kind != CallerKindHuman {
		t.Fatalf("caller kind = %q, want human", human.CallerContext.Kind)
	}
}

func TestResolveSessionConfigThreadsPersistenceRoot(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()

	cfg, err := ResolveSessionConfig(Request{
		WorkspaceRoot: workspace,
		LoadOptions:   config.LoadOptions{ConfigRoot: root},
	})
	if err != nil {
		t.Fatalf("ResolveSessionConfig: %v", err)
	}
	if cfg.PersistenceRoot != root {
		t.Fatalf("persistence root = %q, want %q", cfg.PersistenceRoot, root)
	}
}
