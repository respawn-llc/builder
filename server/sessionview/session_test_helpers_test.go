package sessionview

import (
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
)

var sessionViewTestPersistence = sessiontest.NewPersistence()

func newSessionViewStore(t *testing.T, containerDir, containerName, workspaceRoot string) *session.Store {
	t.Helper()
	store, err := session.Create(containerDir, containerName, workspaceRoot, sessionViewTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}
