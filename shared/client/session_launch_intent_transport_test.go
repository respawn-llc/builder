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

func TestRemoteSessionLaunchPreservesTypedIntent(t *testing.T) {
	previous := mustTransportSessionID(t, "previous-session")
	parentAgent := mustTransportSessionID(t, "parent-agent-session")
	target := mustTransportSessionID(t, "target-session")
	tests := []struct {
		name       string
		intent     serverapi.SessionLaunchIntent
		wantKind   serverapi.SessionLaunchIntentKind
		wantOrigin serverapi.SessionCreateOriginKind
		wantSource *runtimeids.SessionID
		wantTarget *runtimeids.SessionID
	}{
		{
			name:       "independent creation",
			intent:     serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
			wantKind:   serverapi.SessionLaunchIntentCreateNew,
			wantOrigin: serverapi.SessionCreateOriginIndependent,
		},
		{
			name:       "previous session creation",
			intent:     serverapi.CreateNewSessionLaunchIntent(serverapi.PreviousSessionCreateOrigin(previous)),
			wantKind:   serverapi.SessionLaunchIntentCreateNew,
			wantOrigin: serverapi.SessionCreateOriginPreviousSession,
			wantSource: &previous,
		},
		{
			name:       "parent agent creation",
			intent:     serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(parentAgent)),
			wantKind:   serverapi.SessionLaunchIntentCreateNew,
			wantOrigin: serverapi.SessionCreateOriginParentAgent,
			wantSource: &parentAgent,
		},
		{
			name:       "open existing",
			intent:     serverapi.OpenExistingSessionLaunchIntent(target),
			wantKind:   serverapi.SessionLaunchIntentOpenExisting,
			wantTarget: &target,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newRemoteTestServer(t, func(ws *websocket.Conn) {
				req := acceptRemoteHandshake(t, ws)
				if err := websocket.JSON.Receive(ws, &req); err != nil {
					t.Errorf("receive session.plan: %v", err)
					return
				}
				if req.Method != protocol.MethodSessionPlan {
					t.Errorf("method = %q, want %q", req.Method, protocol.MethodSessionPlan)
					return
				}
				var plan serverapi.SessionPlanRequest
				if err := json.Unmarshal(req.Params, &plan); err != nil {
					t.Errorf("decode session.plan params: %v", err)
					return
				}
				assertTransportIntent(t, plan.Intent, test.wantKind, test.wantOrigin, test.wantSource, test.wantTarget)
				if err := websocket.JSON.Send(ws, protocol.NewSuccessResponse(req.ID, serverapi.SessionPlanResponse{})); err != nil {
					t.Errorf("send session.plan response: %v", err)
				}
			})

			remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
			if err != nil {
				t.Fatalf("DialRemoteURL: %v", err)
			}
			defer func() { _ = remote.Close() }()

			if _, err := remote.PlanSession(context.Background(), serverapi.SessionPlanRequest{
				ClientRequestID: "remote-request-" + test.name,
				Mode:            serverapi.SessionLaunchModeInteractive,
				Intent:          test.intent,
			}); err != nil {
				t.Fatalf("PlanSession: %v", err)
			}
		})
	}
}

func TestRemoteSessionLaunchPropagatesTypedIntentRejection(t *testing.T) {
	server := newRemoteTestServer(t, func(ws *websocket.Conn) {
		req := acceptRemoteHandshake(t, ws)
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			t.Errorf("receive session.plan: %v", err)
			return
		}
		if req.Method != protocol.MethodSessionPlan {
			t.Errorf("method = %q, want %q", req.Method, protocol.MethodSessionPlan)
			return
		}
		_ = websocket.JSON.Send(ws, protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, "invalid session launch intent"))
	})

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	_, err = remote.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "legacy-request",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	})
	if err == nil {
		t.Fatal("PlanSession accepted a request rejected by the remote gateway")
	}
}

func assertTransportIntent(t *testing.T, got serverapi.SessionLaunchIntent, wantKind serverapi.SessionLaunchIntentKind, wantOrigin serverapi.SessionCreateOriginKind, wantSource *runtimeids.SessionID, wantSession *runtimeids.SessionID) {
	t.Helper()
	if got.Kind() != wantKind {
		t.Fatalf("intent kind = %q, want %q", got.Kind(), wantKind)
	}
	origin, hasOrigin := got.CreateOrigin()
	if wantKind == serverapi.SessionLaunchIntentOpenExisting {
		if hasOrigin {
			t.Fatalf("unexpected creation origin %+v", origin)
		}
	} else {
		if !hasOrigin || origin.Kind() != wantOrigin {
			t.Fatalf("creation origin = %+v/%v, want %q", origin, hasOrigin, wantOrigin)
		}
		source, hasSource := origin.SessionID()
		if wantSource == nil {
			if hasSource {
				t.Fatalf("unexpected creation source %q", source.String())
			}
		} else if !hasSource || source != *wantSource {
			t.Fatalf("creation source = %q/%v, want %q", source.String(), hasSource, wantSource.String())
		}
	}
	session, hasSession := got.SessionID()
	if wantSession == nil {
		if hasSession {
			t.Fatalf("unexpected session ID %q", session.String())
		}
	} else if !hasSession || session != *wantSession {
		t.Fatalf("session ID = %q/%v, want %q", session.String(), hasSession, wantSession.String())
	}
}

func mustTransportSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
