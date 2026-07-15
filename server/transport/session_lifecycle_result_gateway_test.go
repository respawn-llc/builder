package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"core/server/core"
	"core/shared/client"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type lifecycleResultGatewayService struct {
	last serverapi.SessionResolveTransitionRequest
}

func (s *lifecycleResultGatewayService) Close() error {
	return nil
}

func (s *lifecycleResultGatewayService) GetInitialInput(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	return serverapi.SessionInitialInputResponse{}, nil
}

func (s *lifecycleResultGatewayService) PersistInputDraft(context.Context, serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	return serverapi.SessionPersistInputDraftResponse{}, nil
}

func (s *lifecycleResultGatewayService) RetargetSessionWorkspace(context.Context, serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	return serverapi.SessionRetargetWorkspaceResponse{}, nil
}

func (s *lifecycleResultGatewayService) ResolveTransition(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionLifecycleResult, error) {
	s.last = req
	return serverapi.SelectSessionLifecycleResult(serverapi.SessionAuthPreparationKeepCurrent), nil
}

type lifecycleResultGatewayDependencies struct {
	*core.Core
	lifecycle client.SessionLifecycleClient
}

func (d *lifecycleResultGatewayDependencies) SessionLifecycleClient() client.SessionLifecycleClient {
	return d.lifecycle
}

func TestGatewaySessionLifecycleResultRoundTrip(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	service := &lifecycleResultGatewayService{}
	deps := &lifecycleResultGatewayDependencies{
		Core:      appCore,
		lifecycle: client.NewLoopbackSessionLifecycleClient(service),
	}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	var result serverapi.SessionLifecycleResult
	callGateway(t, conn, "typed-lifecycle-result", protocol.MethodSessionResolveTransition, serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "gateway-lifecycle-result",
		Transition:      serverapi.SessionTransition{Action: serverapi.SessionTransitionActionResume},
	}, &result)
	if result.Kind() != serverapi.SessionLifecycleResultSelectSession {
		t.Fatalf("result kind = %q, want select session", result.Kind())
	}
	authPreparation, ok := result.AuthPreparation()
	if !ok || authPreparation != serverapi.SessionAuthPreparationKeepCurrent {
		t.Fatalf("auth preparation = %q/%v, want keep current", authPreparation, ok)
	}
	if service.last.Transition.Action != serverapi.SessionTransitionActionResume {
		t.Fatalf("forwarded action = %q, want resume", service.last.Transition.Action)
	}
}

func TestGatewaySessionLifecycleResultRejectsLegacyResponseFields(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"stop","should_continue":true}`,
		`{"kind":"stop","requires_reauth":true}`,
		`{"kind":"stop","next_session_id":"target-session"}`,
		`{"kind":"stop","force_new_session":true}`,
		`{"kind":"stop","parent_session_id":"parent-session"}`,
		`{"kind":"stop","initial_input":"draft"}`,
	} {
		var result serverapi.SessionLifecycleResult
		if err := json.Unmarshal([]byte(raw), &result); err == nil {
			t.Fatalf("legacy lifecycle response %s unexpectedly decoded: %+v", raw, result)
		}
	}
}

var _ GatewayDependencies = (*lifecycleResultGatewayDependencies)(nil)
var _ client.SessionLifecycleClient = (*lifecycleResultGatewayService)(nil)
