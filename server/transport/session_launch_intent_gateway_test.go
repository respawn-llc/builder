package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"core/server/core"
	"core/shared/client"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type sessionLaunchIntentGatewayService struct {
	last serverapi.SessionPlanRequest
}

func (s *sessionLaunchIntentGatewayService) PlanSession(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	s.last = req
	return serverapi.SessionPlanResponse{
		Plan: serverapi.SessionPlan{SessionID: "planned-session"},
	}, nil
}

type sessionLaunchIntentGatewayDependencies struct {
	*core.Core
	launch client.SessionLaunchClient
}

func (d *sessionLaunchIntentGatewayDependencies) SessionLaunchClientForProjectWorkspace(context.Context, string, string) (client.SessionLaunchClient, error) {
	return d.launch, nil
}

func (d *sessionLaunchIntentGatewayDependencies) SessionLaunchClientForProjectWorkspaceID(context.Context, string, string) (client.SessionLaunchClient, error) {
	return d.launch, nil
}

func TestGatewaySessionPlanRoundTripsTypedLaunchIntent(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	service := &sessionLaunchIntentGatewayService{}
	deps := &sessionLaunchIntentGatewayDependencies{Core: appCore, launch: service}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: appCore.ProjectID()}, nil)

	parent := mustGatewayIntentSessionID(t, "parent-session")
	target := mustGatewayIntentSessionID(t, "target-session")
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
			callGateway(t, conn, "plan-"+test.name, protocol.MethodSessionPlan, serverapi.SessionPlanRequest{
				ClientRequestID: "gateway-request-" + test.name,
				Mode:            serverapi.SessionLaunchModeInteractive,
				Intent:          test.intent,
			}, nil)
			assertGatewayIntent(t, service.last.Intent, test.wantKind, test.wantParent, test.wantTarget)
		})
	}
}

func TestGatewaySessionPlanRejectsLegacyLaunchFlags(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	service := &sessionLaunchIntentGatewayService{}
	deps := &sessionLaunchIntentGatewayDependencies{Core: appCore, launch: service}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: appCore.ProjectID()}, nil)

	for _, raw := range []string{
		`{"client_request_id":"legacy-selected","mode":"interactive","selected_session_id":"target-session"}`,
		`{"client_request_id":"legacy-force-new","mode":"interactive","force_new_session":true}`,
		`{"client_request_id":"legacy-parent","mode":"interactive","parent_session_id":"parent-session"}`,
		`{"client_request_id":"missing-intent","mode":"interactive"}`,
	} {
		var params json.RawMessage = []byte(raw)
		respErr := callGatewayExpectError(t, conn, "reject-"+raw, protocol.MethodSessionPlan, params)
		if respErr.Code != protocol.ErrCodeInvalidParams {
			t.Fatalf("session.plan %s error code = %d, want invalid params", raw, respErr.Code)
		}
	}
}

func assertGatewayIntent(t *testing.T, got serverapi.SessionLaunchIntent, wantKind serverapi.SessionLaunchIntentKind, wantParent *runtimeids.SessionID, wantTarget *runtimeids.SessionID) {
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
	target, hasTarget := got.SessionID()
	if wantTarget == nil {
		if hasTarget {
			t.Fatalf("unexpected target ID %q", target.String())
		}
	} else if !hasTarget || target != *wantTarget {
		t.Fatalf("target ID = %q/%v, want %q", target.String(), hasTarget, wantTarget.String())
	}
}

func mustGatewayIntentSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}

var _ GatewayDependencies = (*sessionLaunchIntentGatewayDependencies)(nil)

var _ client.SessionLaunchClient = (*sessionLaunchIntentGatewayService)(nil)
