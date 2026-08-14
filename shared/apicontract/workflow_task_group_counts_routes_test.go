package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkflowProjectTaskGroupCountsRouteContract(t *testing.T) {
	route, ok := RouteByMethod(protocol.MethodWorkflowProjectTaskGroupCounts)
	if !ok {
		t.Fatal("Workflow Project Task group-counts route is missing")
	}
	if route.Kind != KindUnary ||
		route.Auth != AuthPreServerAuth ||
		route.Scope != ScopeProjectView ||
		route.Connection != ConnectionUnscoped ||
		route.RequestType != reflect.TypeOf(serverapi.WorkflowProjectTaskGroupCountsRequest{}) ||
		route.ResponseType != reflect.TypeOf(serverapi.WorkflowProjectTaskGroupCountsResponse{}) {
		t.Fatalf("Workflow Project Task group-counts route = %#v", route)
	}
}
