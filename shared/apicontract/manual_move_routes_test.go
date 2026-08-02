package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkflowTaskMovePreviewRouteContract(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodWorkflowTaskMovePreview)
	if !ok {
		t.Fatal("workflow task move preview route is missing")
	}
	if route.Kind != KindUnary ||
		route.Auth != AuthServer ||
		route.Scope != ScopeProjectView ||
		route.Connection != ConnectionUnscoped ||
		route.Dependency != DependencyWorkflow ||
		route.RequestType != reflect.TypeOf(serverapi.WorkflowTaskMovePreviewRequest{}) ||
		route.ResponseType != reflect.TypeOf(serverapi.WorkflowTaskMovePreviewResponse{}) ||
		!route.ValidatesRequest {
		t.Fatalf("workflow task move preview route = %+v", route)
	}
}
