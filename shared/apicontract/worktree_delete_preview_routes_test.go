package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/worktreecontract"
)

func TestWorktreeDeletePreviewRouteUsesSessionWorktreeBoundary(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodWorktreeDeletePreview)
	if !ok {
		t.Fatalf("route %q is missing", protocol.MethodWorktreeDeletePreview)
	}
	if route.Kind != KindUnary ||
		route.Auth != AuthServer ||
		route.Scope != ScopeSessionActiveProject ||
		route.Connection != ConnectionControl {
		t.Fatalf("delete preview route policy = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(worktreecontract.DeletePreviewRequest{}) ||
		route.ResponseType != reflect.TypeOf(worktreecontract.DeletePreviewResponse{}) {
		t.Fatalf("delete preview route types = %v -> %v", route.RequestType, route.ResponseType)
	}
	if !route.ValidatesRequest {
		t.Fatal("delete preview route does not validate requests")
	}
}
