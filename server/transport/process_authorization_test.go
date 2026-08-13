package transport

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

var errRawProcessServiceCalled = errors.New("raw Process service called")

type processAuthorizationService struct {
	rawGetCalls           int
	rawKillCalls          int
	rawInlineCalls        int
	rawSubscribeCalls     int
	resolveCalls          int
	trustedGetCalls       int
	trustedKillCalls      int
	trustedInlineCalls    int
	trustedSubscribeCalls int
	candidate             apicontract.ProcessAuthorizationCandidate
	authorizations        []apicontract.AuthorizedProcessInActiveProject
}

func (s *processAuthorizationService) ListProcesses(context.Context, serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
	return serverapi.ProcessListResponse{}, nil
}
func (s *processAuthorizationService) GetProcess(context.Context, serverapi.ProcessGetRequest) (serverapi.ProcessGetResponse, error) {
	s.rawGetCalls++
	return serverapi.ProcessGetResponse{}, errRawProcessServiceCalled
}
func (s *processAuthorizationService) KillProcess(context.Context, serverapi.ProcessKillRequest) (serverapi.ProcessKillResponse, error) {
	s.rawKillCalls++
	return serverapi.ProcessKillResponse{}, errRawProcessServiceCalled
}
func (s *processAuthorizationService) GetInlineOutput(context.Context, serverapi.ProcessInlineOutputRequest) (serverapi.ProcessInlineOutputResponse, error) {
	s.rawInlineCalls++
	return serverapi.ProcessInlineOutputResponse{}, errRawProcessServiceCalled
}
func (s *processAuthorizationService) SubscribeProcessOutput(context.Context, serverapi.ProcessOutputSubscribeRequest) (serverapi.ProcessOutputSubscription, error) {
	s.rawSubscribeCalls++
	return nil, errRawProcessServiceCalled
}
func (s *processAuthorizationService) ResolveProcessAuthorization(context.Context, string) (apicontract.ProcessAuthorizationCandidate, error) {
	s.resolveCalls++
	return s.candidate, nil
}
func (s *processAuthorizationService) capture(authorization apicontract.AuthorizedProcessInActiveProject) {
	s.authorizations = append(s.authorizations, authorization)
}
func (s *processAuthorizationService) GetProcessValidated(_ context.Context, _ apicontract.Validated[serverapi.ProcessGetRequest], authorization apicontract.AuthorizedProcessInActiveProject) (serverapi.ProcessGetResponse, error) {
	s.trustedGetCalls++
	s.capture(authorization)
	process := authorization.Process
	return serverapi.ProcessGetResponse{Process: &process}, nil
}
func (s *processAuthorizationService) KillProcessValidated(_ context.Context, _ apicontract.Validated[serverapi.ProcessKillRequest], authorization apicontract.AuthorizedProcessInActiveProject) (serverapi.ProcessKillResponse, error) {
	s.trustedKillCalls++
	s.capture(authorization)
	return serverapi.ProcessKillResponse{}, nil
}
func (s *processAuthorizationService) GetInlineOutputValidated(_ context.Context, _ apicontract.Validated[serverapi.ProcessInlineOutputRequest], authorization apicontract.AuthorizedProcessInActiveProject) (serverapi.ProcessInlineOutputResponse, error) {
	s.trustedInlineCalls++
	s.capture(authorization)
	return serverapi.ProcessInlineOutputResponse{Output: "output", LogPath: "/tmp/proc.log"}, nil
}
func (s *processAuthorizationService) SubscribeProcessOutputValidated(_ context.Context, _ apicontract.Validated[serverapi.ProcessOutputSubscribeRequest], authorization apicontract.AuthorizedProcessInActiveProject) (serverapi.ProcessOutputSubscription, error) {
	s.trustedSubscribeCalls++
	s.capture(authorization)
	return immediateProcessOutputSubscription{}, nil
}

type immediateProcessOutputSubscription struct{}

func (immediateProcessOutputSubscription) Next(context.Context) (clientui.ProcessOutputChunk, error) {
	return clientui.ProcessOutputChunk{}, io.EOF
}
func (immediateProcessOutputSubscription) Close() error { return nil }

type processAuthorizationDependencies struct {
	*core.Core
	process *processAuthorizationService
}

func (d *processAuthorizationDependencies) ProcessViewClient() apicontract.ProcessViewService {
	return d.process
}
func (d *processAuthorizationDependencies) ProcessControlClient() apicontract.ProcessControlService {
	return d.process
}
func (d *processAuthorizationDependencies) ProcessOutputClient() apicontract.ProcessOutputService {
	return d.process
}

func TestGatewayCarriesOneTypedProcessAuthorizationFactToAllProcessOwners(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	sessionStore := createGatewayAuthoritativeSession(t, appCore)
	process := clientui.BackgroundProcess{
		ID:             "proc-1",
		OwnerSessionID: sessionStore.Meta().SessionID,
		OwnerRunID:     "run-1",
		Command:        "sleep 1",
	}
	service := &processAuthorizationService{candidate: apicontract.ProcessAuthorizationCandidate{
		ProcessID:      process.ID,
		OwnerSessionID: process.OwnerSessionID,
		Process:        process,
	}}
	gateway, err := NewGateway(
		&processAuthorizationDependencies{Core: appCore, process: service},
		protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"},
	)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	state := &connectionState{handshakeDone: true, attachedProject: appCore.ProjectID()}

	for _, request := range []protocol.Request{
		{JSONRPC: protocol.JSONRPCVersion, ID: "get", Method: protocol.MethodProcessGet, Params: mustJSON(t, serverapi.ProcessGetRequest{ProcessID: process.ID})},
		{JSONRPC: protocol.JSONRPCVersion, ID: "kill", Method: protocol.MethodProcessKill, Params: mustJSON(t, serverapi.ProcessKillRequest{ClientRequestID: "kill-1", ProcessID: process.ID})},
		{JSONRPC: protocol.JSONRPCVersion, ID: "inline", Method: protocol.MethodProcessInlineOutput, Params: mustJSON(t, serverapi.ProcessInlineOutputRequest{ProcessID: process.ID, MaxChars: 10})},
	} {
		response := gateway.dispatch(t.Context(), state, request)
		if response.Error != nil {
			t.Fatalf("%s dispatch: %+v", request.Method, response.Error)
		}
	}

	conn := &processAuthorizationConn{}
	route, _ := apicontract.RouteByMethod(protocol.MethodProcessSubscribeOutput)
	executeProcessOutputSubscription(gateway, conn, t.Context(), state, route, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "subscribe",
		Method:  protocol.MethodProcessSubscribeOutput,
		Params:  mustJSON(t, serverapi.ProcessOutputSubscribeRequest{ProcessID: process.ID}),
	})

	if service.resolveCalls != 4 {
		t.Fatalf("authorization snapshots = %d, want 4", service.resolveCalls)
	}
	if service.rawGetCalls != 0 || service.rawKillCalls != 0 || service.rawInlineCalls != 0 || service.rawSubscribeCalls != 0 {
		t.Fatalf("raw calls: get=%d kill=%d inline=%d subscribe=%d", service.rawGetCalls, service.rawKillCalls, service.rawInlineCalls, service.rawSubscribeCalls)
	}
	if service.trustedGetCalls != 1 || service.trustedKillCalls != 1 || service.trustedInlineCalls != 1 || service.trustedSubscribeCalls != 1 {
		t.Fatalf("trusted calls: get=%d kill=%d inline=%d subscribe=%d", service.trustedGetCalls, service.trustedKillCalls, service.trustedInlineCalls, service.trustedSubscribeCalls)
	}
	if len(service.authorizations) != 4 {
		t.Fatalf("authorization facts = %d, want 4", len(service.authorizations))
	}
	want := apicontract.AuthorizedProcessInActiveProject{ProcessID: process.ID, OwnerSessionID: process.OwnerSessionID, Process: process}
	for index, got := range service.authorizations {
		if got != want {
			t.Errorf("authorization fact %d = %+v, want %+v", index, got, want)
		}
	}
}

type processAuthorizationConn struct {
	frames []rpcwire.Frame
}

func (c *processAuthorizationConn) Send(_ context.Context, frame rpcwire.Frame) error {
	c.frames = append(c.frames, frame)
	return nil
}
func (*processAuthorizationConn) Events() <-chan rpcwire.Event { return nil }
func (*processAuthorizationConn) Closed() <-chan struct{}      { return nil }
func (*processAuthorizationConn) Close() error                 { return nil }
