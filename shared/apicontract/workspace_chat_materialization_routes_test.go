package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkspaceChatMaterializationRouteIsAuthenticatedProjectWorkspaceControl(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodSessionWorkspaceChatMaterialize)
	if !ok {
		t.Fatal("workspace Chat materialization route is missing")
	}
	if route.Kind != KindUnary ||
		route.Auth != AuthServer ||
		route.Scope != ScopeProjectWorkspace ||
		route.Connection != ConnectionControl ||
		route.Dependency != DependencySessionLaunch {
		t.Fatalf("route = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.WorkspaceChatMaterializeRequest{}) ||
		route.ResponseType != reflect.TypeOf(serverapi.WorkspaceChatMaterializeResponse{}) {
		t.Fatalf("route types = %v -> %v", route.RequestType, route.ResponseType)
	}
}
