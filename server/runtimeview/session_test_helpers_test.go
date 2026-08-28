package runtimeview

import (
	"strings"
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/sessioncontract"
)

var runtimeViewTestPersistence = sessiontest.NewPersistence()

func runtimeStepIDPointer(raw string) *string {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		panic("runtime test Step identity must not be empty")
	}
	return &normalized
}

func newRuntimeViewSession(t *testing.T, containerDir, containerName, workspaceRoot string) *session.Store {
	t.Helper()
	store, err := session.Create(containerDir, containerName, workspaceRoot, sessioncontract.SessionCategoryMain, runtimeViewTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}

func newRuntimeViewParentAgentChild(t *testing.T, containerDir, containerName, workspaceRoot string) (*session.Store, string) {
	t.Helper()
	parent := newRuntimeViewSession(t, containerDir, containerName, workspaceRoot)
	child, err := session.NewLazy(containerDir, containerName, workspaceRoot, sessioncontract.SessionCategoryMain, runtimeViewTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create lazy child: %v", err)
	}
	if err := session.InitializeCreationContext(child, parent, session.SessionCreationSourceParentAgent, session.ChildContextOptions{}); err != nil {
		t.Fatalf("initialize child provenance: %v", err)
	}
	return child, parent.Meta().SessionID
}
