package sessionview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/sessioncontract"
)

var sessionViewTestPersistence = sessiontest.NewPersistence()

type testSessionResolver struct {
	store *session.Store
}

func newTestSessionResolver(store *session.Store) SessionStoreResolver {
	if store == nil {
		return nil
	}
	return testSessionResolver{store: store}
}

func (r testSessionResolver) ResolveSessionStore(_ context.Context, sessionID string) (*session.Store, error) {
	if r.store == nil {
		return nil, errors.New("session store is required")
	}
	if strings.TrimSpace(sessionID) != strings.TrimSpace(r.store.Meta().SessionID) {
		return nil, fmt.Errorf("session %q not available", strings.TrimSpace(sessionID))
	}
	return r.store, nil
}

type testRuntimeResolver struct {
	engine *runtime.Engine
}

func newTestRuntimeResolver(engine *runtime.Engine) RuntimeResolver {
	if engine == nil {
		return nil
	}
	return testRuntimeResolver{engine: engine}
}

func (r testRuntimeResolver) ResolveRuntime(_ context.Context, sessionID string) (*runtime.Engine, error) {
	if r.engine == nil {
		return nil, nil
	}
	if strings.TrimSpace(sessionID) != strings.TrimSpace(r.engine.SessionID()) {
		return nil, fmt.Errorf("session %q not available", strings.TrimSpace(sessionID))
	}
	return r.engine, nil
}

func newSessionViewStore(t *testing.T, containerDir, containerName, workspaceRoot string) *session.Store {
	t.Helper()
	store, err := session.Create(containerDir, containerName, workspaceRoot, sessioncontract.SessionCategoryMain, sessionViewTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}

func newSessionViewParentAgentChild(t *testing.T, containerDir, containerName, workspaceRoot string) (*session.Store, string) {
	t.Helper()
	parent := newSessionViewStore(t, containerDir, containerName, workspaceRoot)
	child, err := session.NewLazy(containerDir, containerName, workspaceRoot, sessioncontract.SessionCategoryMain, sessionViewTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create lazy child: %v", err)
	}
	if err := session.InitializeCreationContext(child, parent, session.SessionCreationSourceParentAgent, session.ChildContextOptions{}); err != nil {
		t.Fatalf("initialize child provenance: %v", err)
	}
	return child, parent.Meta().SessionID
}
