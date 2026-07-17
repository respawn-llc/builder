package startupconfig

import (
	"errors"
	"path/filepath"
	"testing"

	"core/server/metadata"
	"core/server/session"
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
