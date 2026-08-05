package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestRuntimeLiveControlRouteContracts(t *testing.T) {
	tests := []struct {
		method       string
		scope        ScopePolicy
		connection   ConnectionStrategy
		requestType  reflect.Type
		responseType reflect.Type
	}{
		{
			method:       protocol.MethodRuntimeLiveSteer,
			scope:        ScopeRuntimeLiveSessionRequired,
			connection:   ConnectionControl,
			requestType:  reflect.TypeOf(serverapi.RuntimeLiveSteerRequest{}),
			responseType: reflect.TypeOf(serverapi.RuntimeLiveSteerResponse{}),
		},
		{
			method:       protocol.MethodRuntimeLiveStop,
			scope:        ScopeRuntimeLiveSessionOptional,
			connection:   ConnectionDedicated,
			requestType:  reflect.TypeOf(serverapi.RuntimeLiveStopRequest{}),
			responseType: reflect.TypeOf(serverapi.RuntimeLiveStopResponse{}),
		},
		{
			method:       protocol.MethodRuntimeLiveWait,
			scope:        ScopeRuntimeLiveSessionRequired,
			connection:   ConnectionDedicated,
			requestType:  reflect.TypeOf(serverapi.RuntimeLiveWaitRequest{}),
			responseType: reflect.TypeOf(serverapi.RuntimeLiveWaitResponse{}),
		},
	}

	for _, tt := range tests {
		route, ok := RouteByMethod(tt.method)
		if !ok {
			t.Fatalf("route %q missing", tt.method)
		}
		if route.Kind != KindUnary || route.Auth != AuthServer || route.Scope != tt.scope || route.Connection != tt.connection || route.Dependency != DependencyRuntimeControl {
			t.Fatalf("route %q metadata = %+v", tt.method, route)
		}
		if route.RequestType != tt.requestType || route.ResponseType != tt.responseType || !route.ValidatesRequest {
			t.Fatalf("route %q types = %v/%v validates=%t", tt.method, route.RequestType, route.ResponseType, route.ValidatesRequest)
		}
	}
}

func TestRuntimeLiveControlServiceIsSeparateContract(t *testing.T) {
	liveType := reflect.TypeOf((*RuntimeLiveControlService)(nil)).Elem()
	runtimeType := reflect.TypeOf((*RuntimeControlService)(nil)).Elem()
	for _, method := range []string{"LiveSteer", "LiveStop", "LiveWait"} {
		if _, ok := liveType.MethodByName(method); !ok {
			t.Fatalf("RuntimeLiveControlService missing %s", method)
		}
		if _, ok := runtimeType.MethodByName(method); ok {
			t.Fatalf("RuntimeControlService unexpectedly includes live-control method %s", method)
		}
	}
}
