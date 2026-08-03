package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestObservationRoutesUseTypedBlockingContracts(t *testing.T) {
	tests := []struct {
		method string
		req    reflect.Type
		resp   reflect.Type
		scope  ScopePolicy
		conn   ConnectionStrategy
	}{
		{protocol.MethodRuntimeLiveWatch, reflect.TypeOf(serverapi.RuntimeLiveWatchRequest{}), reflect.TypeOf(serverapi.RuntimeLiveWatchResponse{}), ScopeRuntimeLiveSessionRequired, ConnectionDedicated},
		{protocol.MethodWorkflowTaskObserve, reflect.TypeOf(serverapi.WorkflowTaskObservationRequest{}), reflect.TypeOf(serverapi.WorkflowTaskObservationResponse{}), ScopeProjectView, ConnectionDedicated},
	}
	for _, test := range tests {
		route, ok := RouteByMethod(test.method)
		if !ok {
			t.Fatalf("route %q missing", test.method)
		}
		if route.RequestType != test.req || route.ResponseType != test.resp || route.Scope != test.scope || route.Connection != test.conn || !route.ValidatesRequest {
			t.Fatalf("route %q = %+v", test.method, route)
		}
	}
}
