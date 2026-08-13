package transport

import (
	"context"
	"errors"
	"testing"

	"core/server/core"
	"core/server/metadata"
	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

var errRawRuntimeLiveControlCalled = errors.New("raw Runtime Live Control service called")

type runtimeLiveAuthorityRoutingService struct {
	steerCalls int
	waitCalls  int
}

func (*runtimeLiveAuthorityRoutingService) LiveSteer(context.Context, serverapi.RuntimeLiveSteerRequest) (serverapi.RuntimeLiveSteerResponse, error) {
	return serverapi.RuntimeLiveSteerResponse{}, errRawRuntimeLiveControlCalled
}

func (*runtimeLiveAuthorityRoutingService) LiveStop(context.Context, serverapi.RuntimeLiveStopRequest) (serverapi.RuntimeLiveStopResponse, error) {
	return serverapi.RuntimeLiveStopResponse{}, nil
}

func (*runtimeLiveAuthorityRoutingService) LiveWait(context.Context, serverapi.RuntimeLiveWaitRequest) (serverapi.RuntimeLiveWaitResponse, error) {
	return serverapi.RuntimeLiveWaitResponse{}, errRawRuntimeLiveControlCalled
}

func (*runtimeLiveAuthorityRoutingService) LiveWatch(context.Context, serverapi.RuntimeLiveWatchRequest) (serverapi.RuntimeLiveWatchResponse, error) {
	return serverapi.RuntimeLiveWatchResponse{}, nil
}

func (*runtimeLiveAuthorityRoutingService) LiveStopValidated(
	context.Context,
	apicontract.Validated[serverapi.RuntimeLiveStopRequest],
) (serverapi.RuntimeLiveStopResponse, error) {
	return serverapi.RuntimeLiveStopResponse{}, nil
}

func (*runtimeLiveAuthorityRoutingService) LiveWatchValidated(
	context.Context,
	apicontract.Validated[serverapi.RuntimeLiveWatchRequest],
) (serverapi.RuntimeLiveWatchResponse, error) {
	return serverapi.RuntimeLiveWatchResponse{}, nil
}

func (s *runtimeLiveAuthorityRoutingService) LiveSteerValidated(
	_ context.Context,
	_ apicontract.Validated[serverapi.RuntimeLiveSteerRequest],
) (serverapi.RuntimeLiveSteerResponse, error) {
	s.steerCalls++
	return serverapi.RuntimeLiveSteerResponse{}, serverapi.ErrRuntimeUnavailable
}

func (s *runtimeLiveAuthorityRoutingService) LiveWaitValidated(
	_ context.Context,
	_ apicontract.Validated[serverapi.RuntimeLiveWaitRequest],
) (serverapi.RuntimeLiveWaitResponse, error) {
	s.waitCalls++
	return serverapi.RuntimeLiveWaitResponse{}, serverapi.ErrRuntimeNoActiveRun
}

type runtimeLiveAuthorityRoutingDependencies struct {
	*core.Core
	live *runtimeLiveAuthorityRoutingService
}

func (*runtimeLiveAuthorityRoutingDependencies) MetadataStore() *metadata.Store {
	return nil
}

func (d *runtimeLiveAuthorityRoutingDependencies) RuntimeLiveControlClient() apicontract.RuntimeLiveControlService {
	return d.live
}

func TestGatewayRoutesRuntimeLiveSteerAndWaitDirectlyToRuntimeAuthority(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	live := &runtimeLiveAuthorityRoutingService{}
	gateway, err := NewGateway(
		&runtimeLiveAuthorityRoutingDependencies{Core: appCore, live: live},
		protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"},
	)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	state := &connectionState{handshakeDone: true}
	sessionID := runtimeids.NewSessionID().String()

	steer := gateway.dispatch(t.Context(), state, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "steer",
		Method:  protocol.MethodRuntimeLiveSteer,
		Params: mustJSON(t, serverapi.RuntimeLiveSteerRequest{
			ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
			SessionID:       sessionID,
			Text:            "steer",
		}),
	})
	if steer.Error == nil || steer.Error.Code != protocol.ErrCodeRuntimeUnavailable {
		t.Fatalf("LiveSteer response = %+v error=%+v, want Runtime Authority unavailable", steer, steer.Error)
	}

	wait := gateway.dispatch(t.Context(), state, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "wait",
		Method:  protocol.MethodRuntimeLiveWait,
		Params:  mustJSON(t, serverapi.RuntimeLiveWaitRequest{SessionID: sessionID}),
	})
	if wait.Error == nil || wait.Error.Code != protocol.ErrCodeRuntimeNoActiveRun {
		t.Fatalf("LiveWait response = %+v, want Runtime Authority no-active-run", wait)
	}
	if live.steerCalls != 1 || live.waitCalls != 1 {
		t.Fatalf("trusted Runtime Authority calls: steer=%d wait=%d, want 1 each", live.steerCalls, live.waitCalls)
	}
}
