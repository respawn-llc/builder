package client

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
	"golang.org/x/net/websocket"
)

func TestSessionLifecycleResultRemoteRoundTrip(t *testing.T) {
	want := serverapi.SelectSessionDirective(serverapi.SessionAuthPreparationReauthenticate)
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Errorf("receive session.resolve_transition: %v", err)
			return
		}
		if req.Method != protocol.MethodSessionResolveTransition {
			t.Errorf("method = %q, want %q", req.Method, protocol.MethodSessionResolveTransition)
			return
		}
		var transition serverapi.SessionResolveTransitionRequest
		if err := json.Unmarshal(req.Params, &transition); err != nil {
			t.Errorf("decode transition: %v", err)
			return
		}
		if transition.Transition.Action != serverapi.SessionTransitionActionLogout {
			t.Errorf("transition action = %q, want logout", transition.Transition.Action)
			return
		}
		if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, want)); err != nil {
			t.Errorf("send lifecycle result: %v", err)
		}
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	got, err := remote.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "remote-lifecycle-result",
		Transition:      serverapi.SessionTransition{Action: serverapi.SessionTransitionActionLogout},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal result: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal expected result: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("result JSON = %s, want %s", gotJSON, wantJSON)
	}
}
