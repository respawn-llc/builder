package transport

import (
	"context"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestChatContextRouteScopeUsesSessionTarget(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	executor := newRoutePolicyExecutor(fixture.gateway)
	route := routeForTest(t, protocol.MethodChatContextGet)

	sessionRequest := serverapi.NewSessionChatContextRequest(mustChatContextSessionID(t, fixture.ownSessionID))
	if err := executor.authorizeScope(
		context.Background(),
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		route,
		sessionRequest,
	); err != nil {
		t.Fatalf("Session target scope: %v", err)
	}
	foreignRequest := serverapi.NewSessionChatContextRequest(mustChatContextSessionID(t, fixture.foreignSessionID))
	if err := executor.authorizeScope(
		context.Background(),
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		route,
		foreignRequest,
	); err == nil {
		t.Fatal("foreign Session target unexpectedly passed active-project scope")
	}
}

func mustChatContextSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("parse Session id: %v", err)
	}
	return sessionID
}
