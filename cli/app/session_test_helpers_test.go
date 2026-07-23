package app

import (
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/sessioncontract"
)

func createAuthoritativeTestSession(t *testing.T, root string, workspaceContainerName string, workspaceRoot string) (*session.Store, *sessiontest.Persistence) {
	t.Helper()
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(root, workspaceContainerName, workspaceRoot, sessioncontract.SessionCategoryMain, persistence.Options()...)
	if err != nil {
		t.Fatalf("create authoritative session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist authoritative session: %v", err)
	}
	return store, persistence
}
