package transport

import (
	"context"
	"errors"
	"sync"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

var errRawGoalServiceCalled = errors.New("raw Goal service called")

type countingGoalRouteService struct {
	apicontract.RuntimeControlService

	trusted apicontract.RuntimeGoalTrustedService
	mu      sync.Mutex
	calls   map[string]int
}

func newCountingGoalRouteService(service apicontract.RuntimeControlService) *countingGoalRouteService {
	return &countingGoalRouteService{
		RuntimeControlService: service,
		trusted:               service.(apicontract.RuntimeGoalTrustedService),
		calls:                 make(map[string]int),
	}
}

func (s *countingGoalRouteService) record(method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[method]++
}

func (*countingGoalRouteService) ShowGoal(context.Context, serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, errRawGoalServiceCalled
}

func (*countingGoalRouteService) SetGoal(context.Context, serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return serverapi.RuntimeGoalMutationResponse{}, errRawGoalServiceCalled
}

func (*countingGoalRouteService) PauseGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return serverapi.RuntimeGoalMutationResponse{}, errRawGoalServiceCalled
}

func (*countingGoalRouteService) ResumeGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return serverapi.RuntimeGoalMutationResponse{}, errRawGoalServiceCalled
}

func (*countingGoalRouteService) CompleteGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return serverapi.RuntimeGoalMutationResponse{}, errRawGoalServiceCalled
}

func (*countingGoalRouteService) ClearGoal(context.Context, serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return serverapi.RuntimeGoalMutationResponse{}, errRawGoalServiceCalled
}

func (s *countingGoalRouteService) ShowGoalValidated(ctx context.Context, req apicontract.Validated[serverapi.RuntimeGoalShowRequest]) (serverapi.RuntimeGoalShowResponse, error) {
	s.record(protocol.MethodRuntimeGoalShow)
	return s.trusted.ShowGoalValidated(ctx, req)
}

func (s *countingGoalRouteService) SetGoalValidated(ctx context.Context, req apicontract.Validated[serverapi.RuntimeGoalSetRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
	s.record(protocol.MethodRuntimeGoalSet)
	return s.trusted.SetGoalValidated(ctx, req)
}

func (s *countingGoalRouteService) PauseGoalValidated(ctx context.Context, req apicontract.Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
	s.record(protocol.MethodRuntimeGoalPause)
	return s.trusted.PauseGoalValidated(ctx, req)
}

func (s *countingGoalRouteService) ResumeGoalValidated(ctx context.Context, req apicontract.Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
	s.record(protocol.MethodRuntimeGoalResume)
	return s.trusted.ResumeGoalValidated(ctx, req)
}

func (s *countingGoalRouteService) CompleteGoalValidated(ctx context.Context, req apicontract.Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
	s.record(protocol.MethodRuntimeGoalComplete)
	return s.trusted.CompleteGoalValidated(ctx, req)
}

func (s *countingGoalRouteService) ClearGoalValidated(ctx context.Context, req apicontract.Validated[serverapi.RuntimeGoalClearRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
	s.record(protocol.MethodRuntimeGoalClear)
	return s.trusted.ClearGoalValidated(ctx, req)
}

type countingGoalRouteDependencies struct {
	*core.Core
	service *countingGoalRouteService

	mu             sync.Mutex
	projectIDCalls int
}

func (d *countingGoalRouteDependencies) RuntimeControlClient() apicontract.RuntimeControlService {
	return d.service
}

func (d *countingGoalRouteDependencies) ProjectID() string {
	d.mu.Lock()
	d.projectIDCalls++
	d.mu.Unlock()
	return d.Core.ProjectID()
}

func TestGatewayGoalRoutesAuthorizeEveryFreshAndReplayedRequestBeforeTrustedService(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	store := createGatewayAuthoritativeSession(t, appCore)
	service := newCountingGoalRouteService(appCore.RuntimeControlClient())
	deps := &countingGoalRouteDependencies{Core: appCore, service: service}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	state := &connectionState{handshakeDone: true}
	sessionID := store.Meta().SessionID

	requests := []struct {
		method string
		params any
	}{
		{protocol.MethodRuntimeGoalShow, serverapi.RuntimeGoalShowRequest{SessionID: sessionID}},
		{protocol.MethodRuntimeGoalSet, serverapi.RuntimeGoalSetRequest{ClientRequestID: "goal-set", SessionID: sessionID, Objective: "ship", Actor: "user"}},
		{protocol.MethodRuntimeGoalPause, serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-pause", SessionID: sessionID, Actor: "user"}},
		{protocol.MethodRuntimeGoalResume, serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-resume", SessionID: sessionID, Actor: "user"}},
		{protocol.MethodRuntimeGoalComplete, serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-complete", SessionID: sessionID, Actor: "user"}},
		{protocol.MethodRuntimeGoalClear, serverapi.RuntimeGoalClearRequest{ClientRequestID: "goal-clear", SessionID: sessionID, Actor: "user"}},
	}

	for _, request := range requests {
		for replay := range 2 {
			response := gateway.dispatch(t.Context(), state, protocol.Request{
				JSONRPC: protocol.JSONRPCVersion,
				ID:      request.method,
				Method:  request.method,
				Params:  mustJSON(t, request.params),
			})
			if response.Error != nil {
				t.Fatalf("%s replay %d error = %+v", request.method, replay, response.Error)
			}
		}
	}

	if deps.projectIDCalls != len(requests)*4 {
		t.Fatalf("Goal scope Project checks = %d, want %d", deps.projectIDCalls, len(requests)*4)
	}
	for _, request := range requests {
		if got := service.calls[request.method]; got != 2 {
			t.Fatalf("%s trusted calls = %d, want 2", request.method, got)
		}
	}
}
