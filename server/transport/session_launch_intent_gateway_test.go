package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type sessionLaunchIntentGatewayService struct {
	last    serverapi.SessionPlanRequest
	planErr error
}

func (s *sessionLaunchIntentGatewayService) PlanSession(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	s.last = req
	if s.planErr != nil {
		return serverapi.SessionPlanResponse{}, s.planErr
	}
	return serverapi.SessionPlanResponse{
		Plan: serverapi.SessionPlan{SessionID: "planned-session"},
	}, nil
}

type sessionLaunchIntentGatewayDependencies struct {
	*core.Core
	launch apicontract.SessionLaunchService
}

func (d *sessionLaunchIntentGatewayDependencies) SessionLaunchClientForProjectWorkspace(context.Context, string, string) (apicontract.SessionLaunchService, error) {
	return d.launch, nil
}

func (d *sessionLaunchIntentGatewayDependencies) SessionLaunchClientForProjectWorkspaceID(context.Context, string, string) (apicontract.SessionLaunchService, error) {
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

	previous := mustGatewayIntentSessionID(t, "previous-session")
	parentAgent := mustGatewayIntentSessionID(t, "parent-agent-session")
	target := mustGatewayIntentSessionID(t, "target-session")
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
			callGateway(t, conn, "plan-"+test.name, protocol.MethodSessionPlan, serverapi.SessionPlanRequest{
				ClientRequestID: "gateway-request-" + test.name,
				Mode:            serverapi.SessionLaunchModeInteractive,
				Intent:          test.intent,
			}, nil)
			assertGatewayIntent(t, service.last.Intent, test.wantKind, test.wantOrigin, test.wantSource, test.wantTarget)
		})
	}
}

func TestGatewayPreservesSubagentLaunchPolicyErrorCodeAndData(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	source := protocol.NewMaxDepthExceededSubagentLaunchPolicyError(1, 0)
	service := &sessionLaunchIntentGatewayService{planErr: source}
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

	respErr := callGatewayExpectError(t, conn, "blocked", protocol.MethodSessionPlan, serverapi.SessionPlanRequest{
		ClientRequestID: "blocked-request",
		Mode:            serverapi.SessionLaunchModeHeadless,
		Intent: serverapi.CreateNewSessionLaunchIntent(
			serverapi.ParentAgentSessionCreateOrigin(mustGatewayIntentSessionID(t, "parent-agent")),
		),
	})
	if respErr.Code != protocol.ErrCodeSubagentLaunchPolicy {
		t.Fatalf("error code = %d, want %d", respErr.Code, protocol.ErrCodeSubagentLaunchPolicy)
	}
	var decoded protocol.SubagentLaunchPolicyError
	if err := json.Unmarshal(respErr.Data, &decoded); err != nil {
		t.Fatalf("decode policy error data: %v", err)
	}
	if decoded.AttemptedDepth == nil || *decoded.AttemptedDepth != 1 ||
		decoded.MaxDepth == nil || *decoded.MaxDepth != 0 {
		t.Fatalf("policy error data = %+v", decoded)
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

func assertGatewayIntent(t *testing.T, got serverapi.SessionLaunchIntent, wantKind serverapi.SessionLaunchIntentKind, wantOrigin serverapi.SessionCreateOriginKind, wantSource *runtimeids.SessionID, wantTarget *runtimeids.SessionID) {
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

var _ apicontract.SessionLaunchService = (*sessionLaunchIntentGatewayService)(nil)
