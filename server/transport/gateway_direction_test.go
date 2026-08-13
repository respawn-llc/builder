package transport

import (
	"context"
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type notificationRejectingGatewayDependencies struct {
	GatewayDependencies
	availabilityChecks int
}

func (d *notificationRejectingGatewayDependencies) RouteDependencyAvailable(apicontract.Dependency) error {
	d.availabilityChecks++
	return nil
}

func TestGatewayRejectsEveryOutboundNotificationBeforeDependencyExecution(t *testing.T) {
	deps := &notificationRejectingGatewayDependencies{}
	gateway := &Gateway{deps: deps}
	state := &connectionState{handshakeDone: true}

	notificationCount := 0
	for _, route := range apicontract.Routes() {
		if route.Kind != apicontract.KindNotification {
			continue
		}
		notificationCount++
		t.Run(route.Method, func(t *testing.T) {
			response := gateway.dispatch(context.Background(), state, protocol.Request{
				JSONRPC: protocol.JSONRPCVersion,
				ID:      "notification-request",
				Method:  route.Method,
				Params:  []byte(`{"malformed-notification-payload":true}`),
			})
			if response.Error == nil || response.Error.Code != protocol.ErrCodeMethodNotFound {
				t.Fatalf("response = %+v, want method-not-found", response)
			}
		})
	}

	if notificationCount == 0 {
		t.Fatal("shared route catalog contains no outbound notifications")
	}
	if deps.availabilityChecks != 0 {
		t.Fatalf("notification requests reached dependency availability %d times", deps.availabilityChecks)
	}
}

func TestOptionalActiveProjectSessionAuthorizationSkipsAllDependenciesWhenSessionIsAbsent(t *testing.T) {
	request := serverapi.SessionInitialInputRequest{}
	_, err := apicontract.WithValidated(
		request,
		apicontract.SemanticValidationRequired,
		func(validated apicontract.Validated[serverapi.SessionInitialInputRequest]) (struct{}, error) {
			authorization, err := authorizeOptionalSessionActiveProject(
				func(req serverapi.SessionInitialInputRequest) string { return req.SessionID },
			)(t.Context(), nil, nil, validated)
			if err != nil {
				return struct{}{}, err
			}
			if _, present := authorization.Authorization(); present {
				t.Fatal("absent Session request produced present authorization")
			}
			return struct{}{}, nil
		},
	)
	if err != nil {
		t.Fatalf("authorize optional absent Session: %v", err)
	}
}
