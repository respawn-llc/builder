package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkflowAttentionListRouteIsGlobal(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodWorkflowAttentionList)
	if !ok {
		t.Fatal("workflow attention list route missing")
	}
	if route.Kind != KindUnary ||
		route.Auth != AuthServer ||
		route.Scope != ScopeNone ||
		route.Connection != ConnectionUnscoped ||
		route.Dependency != DependencyWorkflow {
		t.Fatalf("workflow attention list route = %+v", route)
	}
	if route.RequestType != reflect.TypeOf(serverapi.WorkflowAttentionListRequest{}) ||
		route.ResponseType != reflect.TypeOf(serverapi.WorkflowAttentionListResponse{}) ||
		ValidationMethodFor(serverapi.WorkflowAttentionListRequest{}) == ValidationMethodNone {
		t.Fatalf("workflow attention list route types/validation = %+v", route)
	}
}
