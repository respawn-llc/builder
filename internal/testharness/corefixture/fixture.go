package corefixture

import (
	"context"
	"path/filepath"
	"testing"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/core"
	"core/server/metadata"
	"core/server/session"
	"core/shared/config"
	"core/shared/sessioncontract"
)

type Fixture struct {
	Core    *core.Core
	Config  config.App
	Binding metadata.Binding
}

func New(t testing.TB) *Fixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(t.TempDir(), "kent-root"))
	workspace := t.TempDir()
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	binding, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	if _, err := authSupport.AuthManager.SwitchMethod(context.Background(), auth.Method{
		Type:   auth.MethodAPIKey,
		APIKey: &auth.APIKeyMethod{Key: "test-key"},
	}, true); err != nil {
		t.Fatalf("SwitchMethod: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(cfg)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })
	appCore, err := core.New(cfg, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })
	return &Fixture{Core: appCore, Config: cfg, Binding: binding}
}

func (f *Fixture) CreateSession(t testing.TB) *session.Store {
	t.Helper()
	store, err := session.Create(
		filepath.Join(f.Config.PersistenceRoot, "projects", f.Binding.ProjectID, "sessions"),
		filepath.Base(f.Config.WorkspaceRoot),
		f.Config.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		f.Core.MetadataStore().AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	return store
}
