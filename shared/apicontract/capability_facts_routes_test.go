package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestCapabilityFactsRouteContract(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodCapabilityFactsGet)
	if !ok {
		t.Fatal("capability facts route missing")
	}
	if route.Kind != KindUnary || route.Auth != AuthPreServerAuth || route.Scope != ScopeNone || route.Connection != ConnectionUnscoped || route.Dependency != DependencyCapabilityFacts {
		t.Fatalf("capability facts route = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.CapabilityFactsRequest{}) || route.ResponseType != reflect.TypeOf(serverapi.CapabilityFactsResponse{}) {
		t.Fatalf("capability facts route types = %v / %v", route.RequestType, route.ResponseType)
	}
	if !route.ValidatesRequest {
		t.Fatal("capability facts route must validate its request")
	}
}
