package apicontract

import (
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestChatSettingsReadRouteContract(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodChatSettingsRead)
	if !ok {
		t.Fatal("Chat settings read route is missing")
	}
	if route.Kind != KindUnary ||
		route.Auth != AuthServer ||
		route.Scope != ScopeNone ||
		route.Connection != ConnectionControl ||
		route.Dependency != DependencyChatSettings ||
		route.RequestType != typeOf[serverapi.ChatSettingsReadRequest]() ||
		route.ResponseType != typeOf[serverapi.ChatSettingsReadResponse]() {
		t.Fatalf("route = %+v", route)
	}
}
