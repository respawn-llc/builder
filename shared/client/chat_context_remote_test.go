package client

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
)

func TestRemoteGetChatContextUsesSoleContractAndValidatesResponse(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	want := serverapi.ChatContextResponse{Context: serverapi.ChatContext{
		ContextWindowTokens:      100,
		UsedTokens:               125,
		RemainingTokens:          -25,
		AutomaticThresholdTokens: 80,
		AutoCompactionEnabled:    true,
		CompactionMode:           serverapi.ChatContextCompactionModeProviderNative,
		CompletedCompactionCount: 3,
		CompactionRunning:        true,
	}}
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request protocol.Request
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			t.Errorf("receive Chat Context request: %v", err)
			return
		}
		if request.Method != protocol.MethodChatContextGet {
			t.Errorf("method = %q, want %q", request.Method, protocol.MethodChatContextGet)
			return
		}
		var decoded serverapi.ChatContextRequest
		if err := json.Unmarshal(request.Params, &decoded); err != nil {
			t.Errorf("decode Chat Context request: %v", err)
			return
		}
		if got, selected := decoded.Target.SessionID(); !selected || got != sessionID {
			t.Errorf("Session target = (%s, %t), want (%s, true)", got, selected, sessionID)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, want)); err != nil {
			t.Errorf("send Chat Context response: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	got, err := remote.GetChatContext(t.Context(), serverapi.NewSessionChatContextRequest(sessionID))
	if err != nil {
		t.Fatalf("GetChatContext: %v", err)
	}
	if got != want {
		t.Fatalf("response = %+v, want %+v", got, want)
	}
}

func TestRemoteGetChatContextRejectsInvalidResponse(t *testing.T) {
	response := serverapi.ChatContextResponse{Context: serverapi.ChatContext{
		ContextWindowTokens:      100,
		UsedTokens:               40,
		RemainingTokens:          61,
		AutomaticThresholdTokens: 80,
		CompactionMode:           serverapi.ChatContextCompactionModeLocal,
	}}
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		acceptRemoteHandshake(t, ws)
		var request protocol.Request
		if err := websocket.JSON.Receive(ws, &request); err != nil {
			t.Errorf("receive Chat Context request: %v", err)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(request.ID, response)); err != nil {
			t.Errorf("send Chat Context response: %v", err)
		}
	})
	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	_, err = remote.GetChatContext(t.Context(), serverapi.NewWorkspaceChatContextRequest())
	var invalidResponse *InvalidResponseError
	if err == nil || !errors.As(err, &invalidResponse) {
		t.Fatalf("GetChatContext error = %v, want InvalidResponseError", err)
	}
}

var _ apicontract.ChatContextService = (*Remote)(nil)
