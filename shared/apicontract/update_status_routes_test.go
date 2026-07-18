package apicontract_test

import (
	"reflect"
	"testing"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestUpdateStatusRouteContract(t *testing.T) {
	route, ok := apicontract.RouteByMethod(protocol.MethodServerUpdateStatusGet)
	if !ok {
		t.Fatal("update status route is not registered")
	}
	if route.Kind != apicontract.KindUnary ||
		route.Auth != apicontract.AuthServer ||
		route.Scope != apicontract.ScopeNone ||
		route.Connection != apicontract.ConnectionUnscoped ||
		route.Dependency != apicontract.DependencyServerStatus {
		t.Fatalf("update status route = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.UpdateStatusRequest{}) ||
		route.ResponseType != reflect.TypeOf(serverapi.UpdateStatusResponse{}) ||
		!route.ValidatesRequest {
		t.Fatalf("update status route types/validation = %+v", route)
	}
}
