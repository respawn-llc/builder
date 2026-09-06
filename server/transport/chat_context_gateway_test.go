package transport

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/chatcontext"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type chatContextSessionOwnerFixture struct {
	context   serverapi.ChatContext
	err       error
	calls     int
	sessionID runtimeids.SessionID
}

func (o *chatContextSessionOwnerFixture) ReadSessionChatContext(_ context.Context, sessionID runtimeids.SessionID) (serverapi.ChatContext, error) {
	o.calls++
	o.sessionID = sessionID
	return o.context, o.err
}

type chatContextGatewayDependencies struct {
	GatewayDependencies
	sessionOwner chatcontext.SessionOwner
	sessionCalls int
}

func (d *chatContextGatewayDependencies) SessionChatContextOwner() chatcontext.SessionOwner {
	d.sessionCalls++
	return d.sessionOwner
}

func TestGatewayChatContextDispatchesOnlySessionOwner(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	want := validGatewayChatContext()
	sessionOwner := &chatContextSessionOwnerFixture{context: want}
	deps := &chatContextGatewayDependencies{
		GatewayDependencies: appCore,
		sessionOwner:        sessionOwner,
	}
	store := createGatewayAuthoritativeSession(t, appCore)
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse Session id: %v", err)
	}
	registration, err := productionGatewayRegistration()
	if err != nil {
		t.Fatalf("production Gateway registration: %v", err)
	}
	response := (&Gateway{deps: deps, registration: registration}).dispatch(
		t.Context(),
		&connectionState{handshakeDone: true, attachedProject: appCore.ProjectID()},
		chatContextProtocolRequest(t, serverapi.NewSessionChatContextRequest(sessionID)),
	)
	var got serverapi.ChatContextResponse
	decodeGatewayChatContextResponse(t, response, &got)
	if got.Context != want {
		t.Fatalf("Context = %+v, want %+v", got.Context, want)
	}
	if sessionOwner.calls != 1 || sessionOwner.sessionID != sessionID || deps.sessionCalls != 1 {
		t.Fatalf("calls = Session owner %d (%s), Session resolver %d", sessionOwner.calls, sessionOwner.sessionID, deps.sessionCalls)
	}
}

func TestGatewayChatContextValidatesTargetBeforeSessionDispatch(t *testing.T) {
	appCore, server, _ := newGatewayTestServerWithAuth(t, false)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	malformed := map[string]any{"target": map[string]any{}}
	if got := callGatewayExpectError(t, conn, "malformed-context", protocol.MethodChatContextGet, malformed); got.Code != protocol.ErrCodeInvalidParams {
		t.Fatalf("malformed Context error = %+v, want invalid params", got)
	}

	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse Session id: %v", err)
	}
	var sessionResponse serverapi.ChatContextResponse
	callGateway(t, conn, "session-context", protocol.MethodChatContextGet, serverapi.NewSessionChatContextRequest(sessionID), &sessionResponse)
	if err := sessionResponse.Validate(); err != nil {
		t.Fatalf("pre-auth Session Context response: %v", err)
	}
}

func chatContextProtocolRequest(t *testing.T, request serverapi.ChatContextRequest) protocol.Request {
	t.Helper()
	params, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal Chat Context request: %v", err)
	}
	return protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "chat-context",
		Method:  protocol.MethodChatContextGet,
		Params:  params,
	}
}

func decodeGatewayChatContextResponse(t *testing.T, response protocol.Response, out *serverapi.ChatContextResponse) {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("response error = %+v", response.Error)
	}
	bytes, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatalf("marshal response result: %v", err)
	}
	if err := json.Unmarshal(bytes, out); err != nil {
		t.Fatalf("decode response result: %v", err)
	}
}

func validGatewayChatContext() serverapi.ChatContext {
	return serverapi.ChatContext{
		ContextWindowTokens:      100,
		UsedTokens:               40,
		RemainingTokens:          60,
		AutomaticThresholdTokens: 80,
		AutoCompactionEnabled:    true,
		CompactionMode:           serverapi.ChatContextCompactionModeLocal,
		CompletedCompactionCount: 2,
		ManualCompactAvailable:   true,
	}
}

var _ GatewayDependencies = (*chatContextGatewayDependencies)(nil)
