package sessionview

import (
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/sessioncontract"
)

var sessionViewTestPersistence = sessiontest.NewPersistence()

func newSessionViewStore(t *testing.T, containerDir, containerName, workspaceRoot string) *session.Store {
	t.Helper()
	store, err := session.Create(containerDir, containerName, workspaceRoot, sessioncontract.SessionCategoryMain, sessionViewTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}
