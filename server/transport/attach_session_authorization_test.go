package transport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"core/server/metadata"
	"core/server/session"
	"core/shared/protocol"
	"core/shared/sessioncontract"
)

func TestAttachSessionAuthorizationFactPopulatesConnectionAndResponse(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolved := resolveGatewayTestConfig(t, workspace)
	binding := registerGatewayTestBinding(t, resolved.Config)
	store, err := metadata.Open(resolved.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	sessionStore, err := session.Create(
		filepath.Join(resolved.Config.PersistenceRoot, "projects", binding.ProjectID, "sessions"),
		"attach-session",
		resolved.Config.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sessionStore.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	appCore, _ := newGatewayTestServerForConfig(t, resolved.Config)
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ServerID: "server", ProtocolVersion: protocol.Version})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	state := &connectionState{handshakeDone: true, attachedProject: binding.ProjectID}
	request := protocol.AttachSessionRequest{SessionID: sessionStore.Meta().SessionID}
	response := gateway.dispatch(context.Background(), state, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "attach-session",
		Method:  protocol.MethodAttachSession,
		Params:  mustJSON(t, request),
	})
	if response.Error != nil {
		t.Fatalf("Attach Session error: %+v", response.Error)
	}

	if state.attachedSession == nil || state.attachedSession.String() != request.SessionID {
		t.Fatalf("attached Session = %v, want %q", state.attachedSession, request.SessionID)
	}
	if state.attachedProject != binding.ProjectID ||
		state.attachedWorkspaceID != binding.WorkspaceID ||
		state.attachedWorkspaceRoot != binding.CanonicalRoot {
		t.Fatalf("attached state = project %q workspace %q root %q, want project %q workspace %q root %q",
			state.attachedProject, state.attachedWorkspaceID, state.attachedWorkspaceRoot,
			binding.ProjectID, binding.WorkspaceID, binding.CanonicalRoot)
	}

	var attached protocol.AttachResponse
	if err := json.Unmarshal(response.Result, &attached); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	got, ok := attached.Session()
	if !ok {
		t.Fatal("response does not contain a Session attachment")
	}
	if got.SessionID != request.SessionID ||
		got.ProjectID != binding.ProjectID ||
		got.WorkspaceID != binding.WorkspaceID ||
		got.WorkspaceRoot != binding.CanonicalRoot {
		t.Fatalf("attachment response = %+v", got)
	}

}
