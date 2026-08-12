package client

import (
	"context"
	"encoding/json"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
)

func TestRemoteMaterializeWorkspaceChatUsesDedicatedEmptyRequest(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		request := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			t.Errorf("receive materialization request: %v", err)
			return
		}
		if request.Method != protocol.MethodSessionWorkspaceChatMaterialize {
			t.Errorf("method = %q, want %q", request.Method, protocol.MethodSessionWorkspaceChatMaterialize)
			return
		}
		var params map[string]json.RawMessage
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Errorf("decode materialization params: %v", err)
			return
		}
		if len(params) != 0 {
			t.Errorf("materialization params = %s, want empty object", request.Params)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, serverapi.WorkspaceChatMaterializeResponse{
			SessionID: sessionID,
		})); err != nil {
			t.Errorf("send materialization response: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	response, err := remote.MaterializeWorkspaceChat(context.Background(), serverapi.WorkspaceChatMaterializeRequest{})
	if err != nil {
		t.Fatalf("MaterializeWorkspaceChat: %v", err)
	}
	if response.SessionID != sessionID {
		t.Fatalf("Session identity = %q, want %q", response.SessionID.String(), sessionID.String())
	}
}
