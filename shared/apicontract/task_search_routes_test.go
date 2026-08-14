package apicontract_test

import (
	"reflect"
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestTaskSearchRouteContract(t *testing.T) {
	route, ok := apicontract.RouteByMethod(protocol.MethodWorkflowTaskSearch)
	if !ok {
		t.Fatal("task search route is not registered")
	}
	if route.Kind != apicontract.KindUnary ||
		route.Auth != apicontract.AuthServer ||
		route.Scope != apicontract.ScopeNone ||
		route.Connection != apicontract.ConnectionDedicated ||
		route.DedicatedRequestID != apicontract.TaskSearchDedicatedRequestID {
		t.Fatalf("task search route = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.TaskSearchRequest{}) ||
		route.ResponseType != reflect.TypeOf(serverapi.TaskSearchResponse{}) ||
		!route.ValidatesRequest {
		t.Fatalf("task search route types/validation = %+v", route)
	}
}
