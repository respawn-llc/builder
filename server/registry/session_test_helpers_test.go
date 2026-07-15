package registry

import (
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
)

var registryTestPersistence = sessiontest.NewPersistence()

func newRegistryTestSession(t *testing.T, containerDir, containerName, workspaceRoot string) *session.Store {
	t.Helper()
	store, err := session.Create(containerDir, containerName, workspaceRoot, registryTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return store
}
