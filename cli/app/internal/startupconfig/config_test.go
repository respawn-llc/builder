package startupconfig

import (
	"errors"
	"testing"

	"core/shared/config"
	"core/shared/sessioncontract"
)

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
