package runtimeview

import (
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
)

var runtimeViewTestPersistence = sessiontest.NewPersistence()

func newRuntimeViewSession(t *testing.T, containerDir, containerName, workspaceRoot string) *session.Store {
	t.Helper()
	store, err := session.Create(containerDir, containerName, workspaceRoot, runtimeViewTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}
