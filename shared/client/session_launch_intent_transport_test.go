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

type recordingSessionLaunchService struct {
	last serverapi.SessionPlanRequest
}

func (s *recordingSessionLaunchService) PlanSession(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	s.last = req
	return serverapi.SessionPlanResponse{}, nil
}

func TestLoopbackSessionLaunchPreservesTypedIntent(t *testing.T) {
	parent := mustTransportSessionID(t, "parent-session")
	target := mustTransportSessionID(t, "target-session")

	tests := []struct {
		name   string
		intent serverapi.SessionLaunchIntent
		assert func(t *testing.T, got serverapi.SessionLaunchIntent)
	}{
		{
			name:   "create new without parent",
			intent: serverapi.CreateNewSessionLaunchIntent(nil),
			assert: func(t *testing.T, got serverapi.SessionLaunchIntent) {
				assertTransportIntent(t, got, serverapi.SessionLaunchIntentCreateNew, nil, nil)
			},
		},
		{
			name:   "create new with parent",
			intent: serverapi.CreateNewSessionLaunchIntent(&parent),
			assert: func(t *testing.T, got serverapi.SessionLaunchIntent) {
				assertTransportIntent(t, got, serverapi.SessionLaunchIntentCreateNew, &parent, nil)
			},
		},
		{
			name:   "open existing",
			intent: serverapi.OpenExistingSessionLaunchIntent(target),
			assert: func(t *testing.T, got serverapi.SessionLaunchIntent) {
				assertTransportIntent(t, got, serverapi.SessionLaunchIntentOpenExisting, nil, &target)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &recordingSessionLaunchService{}
			launch := NewLoopbackSessionLaunchClient(service)
			if _, err := launch.PlanSession(context.Background(), serverapi.SessionPlanRequest{
				ClientRequestID: "loopback-request",
				Mode:            serverapi.SessionLaunchModeInteractive,
				Intent:          test.intent,
			}); err != nil {
				t.Fatalf("PlanSession: %v", err)
			}
			test.assert(t, service.last.Intent)
		})
	}
}

func TestRemoteSessionLaunchPreservesTypedIntent(t *testing.T) {
	parent := mustTransportSessionID(t, "parent-session")
	target := mustTransportSessionID(t, "target-session")
	tests := []struct {
		name       string
		intent     serverapi.SessionLaunchIntent
		wantKind   serverapi.SessionLaunchIntentKind
		wantParent *runtimeids.SessionID
		wantTarget *runtimeids.SessionID
	}{
		{
			name:     "create new without parent",
			intent:   serverapi.CreateNewSessionLaunchIntent(nil),
			wantKind: serverapi.SessionLaunchIntentCreateNew,
		},
		{
			name:       "create new with parent",
			intent:     serverapi.CreateNewSessionLaunchIntent(&parent),
			wantKind:   serverapi.SessionLaunchIntentCreateNew,
			wantParent: &parent,
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
				assertTransportIntent(t, plan.Intent, test.wantKind, test.wantParent, test.wantTarget)
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
		Intent:          serverapi.CreateNewSessionLaunchIntent(nil),
	})
	if err == nil {
		t.Fatal("PlanSession accepted a request rejected by the remote gateway")
	}
}

func assertTransportIntent(t *testing.T, got serverapi.SessionLaunchIntent, wantKind serverapi.SessionLaunchIntentKind, wantParent *runtimeids.SessionID, wantSession *runtimeids.SessionID) {
	t.Helper()
	if got.Kind() != wantKind {
		t.Fatalf("intent kind = %q, want %q", got.Kind(), wantKind)
	}
	parent, hasParent := got.ParentID()
	if wantParent == nil {
		if hasParent {
			t.Fatalf("unexpected parent ID %q", parent.String())
		}
	} else if !hasParent || parent != *wantParent {
		t.Fatalf("parent ID = %q/%v, want %q", parent.String(), hasParent, wantParent.String())
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
