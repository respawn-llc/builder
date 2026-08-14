package transport

import (
	"context"
	"errors"
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestGatewayPreActivationOperationsHaveExactRegisteredSet(t *testing.T) {
	preActivation := map[string]struct{}{
		protocol.MethodHandshake:              {},
		protocol.MethodServerReadinessGet:     {},
		protocol.MethodAuthGetBootstrapStatus: {},
		protocol.MethodAuthCompleteBootstrap:  {},
		protocol.MethodAuthAcknowledgeNoAuth:  {},
		protocol.MethodAuthGetStatus:          {},
		protocol.MethodCapabilityFactsGet:     {},
		protocol.MethodOnboardingFinalize:     {},
	}
	seen := make(map[string]struct{}, len(preActivation))
	for _, route := range apicontract.Routes() {
		if route.Kind == apicontract.KindNotification {
			continue
		}
		_, want := preActivation[route.Method]
		got := isGatewayPreActivationOperation(route.Method)
		if got != want {
			t.Fatalf("operation %q pre-activation = %t, want %t", route.Method, got, want)
		}
		if got {
			seen[route.Method] = struct{}{}
		}
	}
	if len(seen) != len(preActivation) {
		t.Fatalf("registered pre-activation operations = %v, want %v", seen, preActivation)
	}
}

func TestGatewayStartupPreflightProtectsEveryInboundOperationKind(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(*Gateway) protocol.Response
	}{
		{
			name: "unary",
			invoke: func(gateway *Gateway) protocol.Response {
				return gateway.dispatch(context.Background(), &connectionState{handshakeDone: true}, protocol.Request{
					JSONRPC: protocol.JSONRPCVersion,
					ID:      "unary",
					Method:  protocol.MethodProjectList,
					Params:  mustJSON(t, serverapi.ProjectListRequest{}),
				})
			},
		},
		{
			name: "progress",
			invoke: func(gateway *Gateway) protocol.Response {
				route, ok := apicontract.RouteByMethod(protocol.MethodRunPrompt)
				if !ok {
					t.Fatal("run prompt route is not registered")
				}
				conn := &recordingGatewayConn{}
				gateway.serveRunPrompt(conn, context.Background(), &connectionState{handshakeDone: true}, route, protocol.Request{
					JSONRPC: protocol.JSONRPCVersion,
					ID:      "progress",
					Method:  protocol.MethodRunPrompt,
					Params: mustJSON(t, serverapi.RunPromptRequest{
						Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
						Prompt: "test",
					}),
				})
				return conn.frames[0].Response()
			},
		},
		{
			name: "subscription",
			invoke: func(gateway *Gateway) protocol.Response {
				conn := &recordingGatewayConn{}
				gateway.serveSubscription(conn, context.Background(), &connectionState{handshakeDone: true}, protocol.Request{
					JSONRPC: protocol.JSONRPCVersion,
					ID:      "subscription",
					Method:  protocol.MethodAttentionNotificationSubscribe,
					Params:  mustJSON(t, serverapi.AttentionNotificationSubscribeRequest{}),
				})
				return conn.frames[0].Response()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := &gatewayStartupLifecycleStub{
				readiness: serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil),
			}
			response := test.invoke(&Gateway{deps: deps})
			requireGatewayStartupReason(t, response, serverapi.ErrServerNotReadyOnboardingRequired)
			if deps.serviceLookups != 0 {
				t.Fatalf("unavailable service getter calls = %d, want 0", deps.serviceLookups)
			}
		})
	}
}

func TestGatewayStartupPreflightPreservesActivationFailure(t *testing.T) {
	activationFailure := errors.New("activation failed")
	diagnostic := activationFailure.Error()
	deps := &gatewayStartupLifecycleStub{
		readiness: serverapi.NewServerNotReadyError(
			serverapi.ServerNotReadyActivationFailed,
			serverapi.ServerNotReadyDetails{Diagnostic: &diagnostic},
			activationFailure,
		),
	}
	response := (&Gateway{deps: deps}).dispatch(context.Background(), &connectionState{handshakeDone: true}, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "activation",
		Method:  protocol.MethodProjectList,
		Params:  mustJSON(t, serverapi.ProjectListRequest{}),
	})
	requireGatewayStartupReason(t, response, serverapi.ErrServerNotReadyActivationFailed)
	if deps.serviceLookups != 0 {
		t.Fatalf("unavailable service getter calls = %d, want 0", deps.serviceLookups)
	}
}

type gatewayStartupLifecycleStub struct {
	GatewayDependencies
	readiness      error
	serviceLookups int
}

func (d *gatewayStartupLifecycleStub) RequireCoreActive() error {
	return d.readiness
}

func (d *gatewayStartupLifecycleStub) ProjectViewClient() apicontract.ProjectViewService {
	d.serviceLookups++
	return nil
}

func (d *gatewayStartupLifecycleStub) RunPromptClientForProjectWorkspace(context.Context, string, string) (apicontract.RunPromptService, error) {
	d.serviceLookups++
	return nil, errors.New("run prompt service is unavailable")
}

func (d *gatewayStartupLifecycleStub) AttentionNotificationClient() apicontract.AttentionNotificationService {
	d.serviceLookups++
	return nil
}

func requireGatewayStartupReason(t testing.TB, response protocol.Response, target error) {
	t.Helper()
	if response.Error == nil {
		t.Fatal("startup preflight succeeded")
	}
	decoded := serverapi.DecodeServerNotReadyError(response.Error.Data, response.Error.Message)
	if !errors.Is(decoded, target) {
		t.Fatalf("startup preflight error = %v, want %v", decoded, target)
	}
}
