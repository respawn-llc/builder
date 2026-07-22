package metadata

import (
	"context"
	"path/filepath"
	"testing"

	"core/server/session"
	"core/shared/config"
	"core/shared/sessioncontract"
)

func newMetadataTestStore(t *testing.T) (*Store, config.App, Binding) {
	t.Helper()
	return newMetadataTestStoreForBoundWorkspace(t, t.TempDir())
}

func newMetadataTestStoreForBoundWorkspace(t *testing.T, workspace string) (*Store, config.App, Binding) {
	t.Helper()
	store, cfg := newMetadataTestStoreForWorkspace(t, workspace)
	binding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	return store, cfg, binding
}

func createMetadataTestSession(t *testing.T, store *Store, cfg config.App, binding Binding) *session.Store {
	t.Helper()
	projectSessionsDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sess.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	return sess
}

func newMetadataTestStoreForWorkspace(t *testing.T, workspace string) (*Store, config.App) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, ".kent-test"))
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	store, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, cfg
}
