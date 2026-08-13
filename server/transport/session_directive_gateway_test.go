package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
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

func (s *lifecycleResultGatewayService) ResolveTransition(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionDirective, error) {
	s.last = req
	return serverapi.SelectSessionDirective(serverapi.SessionAuthPreparationKeepCurrent), nil
}

func (s *lifecycleResultGatewayService) GetInitialInputValidated(ctx context.Context, req apicontract.Validated[serverapi.SessionInitialInputRequest], _ apicontract.OptionalAuthorizedSessionInActiveProject) (serverapi.SessionInitialInputResponse, error) {
	return s.GetInitialInput(ctx, req.Value())
}

func (s *lifecycleResultGatewayService) PersistInputDraftValidated(ctx context.Context, req apicontract.Validated[serverapi.SessionPersistInputDraftRequest], _ apicontract.AuthorizedSessionInActiveProject) (serverapi.SessionPersistInputDraftResponse, error) {
	return s.PersistInputDraft(ctx, req.Value())
}

func (s *lifecycleResultGatewayService) RetargetSessionWorkspaceValidated(ctx context.Context, req apicontract.Validated[serverapi.SessionRetargetWorkspaceRequest], _ apicontract.AttachedProjectConstraint) (serverapi.SessionRetargetWorkspaceResponse, error) {
	return s.RetargetSessionWorkspace(ctx, req.Value())
}

func (s *lifecycleResultGatewayService) ResolveTransitionValidated(ctx context.Context, req apicontract.Validated[serverapi.SessionResolveTransitionRequest], _ apicontract.OptionalAuthorizedSessionInActiveProject) (serverapi.SessionResolveTransitionResponse, error) {
	return s.ResolveTransition(ctx, req.Value())
}

type lifecycleResultGatewayDependencies struct {
	*core.Core
	lifecycle apicontract.SessionLifecycleService
}

func (d *lifecycleResultGatewayDependencies) SessionLifecycleClient() apicontract.SessionLifecycleService {
	return d.lifecycle
}

func TestGatewaySessionLifecycleResultRoundTrip(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	service := &lifecycleResultGatewayService{}
	deps := &lifecycleResultGatewayDependencies{
		Core:      appCore,
		lifecycle: service,
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

	var result serverapi.SessionDirective
	callGateway(t, conn, "typed-lifecycle-result", protocol.MethodSessionResolveTransition, serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "gateway-lifecycle-result",
		Transition:      serverapi.SessionTransition{Action: serverapi.SessionTransitionActionResume},
	}, &result)
	if result.Kind() != serverapi.SessionDirectiveSelectSession {
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
		var result serverapi.SessionDirective
		if err := json.Unmarshal([]byte(raw), &result); err == nil {
			t.Fatalf("legacy lifecycle response %s unexpectedly decoded: %+v", raw, result)
		}
	}
}

var _ GatewayDependencies = (*lifecycleResultGatewayDependencies)(nil)
var _ apicontract.SessionLifecycleService = (*lifecycleResultGatewayService)(nil)
