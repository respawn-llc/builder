package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestPromptCommandCatalogRouteIsAuthenticatedWorkspaceControlRead(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodPromptCommandCatalogGet)
	if !ok {
		t.Fatal("prompt command catalog route is missing")
	}
	if route.Kind != KindUnary ||
		route.Auth != AuthServer ||
		route.Scope != ScopeProjectWorkspace ||
		route.Connection != ConnectionControl {
		t.Fatalf("route = %+v", route)
	}
	if route.RequestType != typeOf[serverapi.PromptCommandCatalogRequest]() ||
		route.ResponseType != typeOf[serverapi.PromptCommandCatalogResponse]() {
		t.Fatalf("route types = %v -> %v", route.RequestType, route.ResponseType)
	}
}

func typeOf[T any]() reflect.Type {
	var value T
	return reflect.TypeOf(value)
}
