package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestChatContextRouteContract(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodChatContextGet)
	if !ok {
		t.Fatal("Chat Context route is missing")
	}
	if route.Kind != KindUnary ||
		route.Auth != AuthPreServerAuth ||
		route.Scope != ScopeSessionActiveProjectIfSet ||
		route.Connection != ConnectionControl ||
		route.Dependency != DependencyChatContext {
		t.Fatalf("route = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.ChatContextRequest{}) ||
		route.ResponseType != reflect.TypeOf(serverapi.ChatContextResponse{}) ||
		!route.ValidatesRequest {
		t.Fatalf("route contract = %+v", route)
	}
}
