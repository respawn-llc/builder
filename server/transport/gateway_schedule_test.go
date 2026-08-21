package transport

import (
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
)

func TestGatewayExclusiveScheduleHasExactOperationSet(t *testing.T) {
	exclusive := map[string]struct{}{
		protocol.MethodHandshake:              {},
		protocol.MethodAuthGetBootstrapStatus: {},
		protocol.MethodAuthCompleteBootstrap:  {},
		protocol.MethodAuthAcknowledgeNoAuth:  {},
		protocol.MethodAuthGetStatus:          {},
		protocol.MethodAttachProject:          {},
		protocol.MethodAttachSession:          {},
	}
	seen := make(map[string]struct{}, len(exclusive))
	for _, route := range apicontract.Routes() {
		if route.Kind == apicontract.KindNotification {
			continue
		}
		_, want := exclusive[route.Method]
		got := isGatewayExclusiveRequest(protocol.Request{Method: route.Method})
		if got != want {
			t.Fatalf("operation %q exclusive = %t, want %t", route.Method, got, want)
		}
		if got {
			seen[route.Method] = struct{}{}
		}
	}
	if len(seen) != len(exclusive) {
		t.Fatalf("exclusive registered operations = %v, want %v", seen, exclusive)
	}
	if isGatewayExclusiveRequest(protocol.Request{Method: "unknown.operation"}) {
		t.Fatal("unknown operation is exclusive")
	}
}
