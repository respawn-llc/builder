package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkflowDependencyRoutesExposeTypedRequestsAndResponses(t *testing.T) {
	tests := []struct {
		method string
		req    any
		resp   any
	}{
		{protocol.MethodWorkflowTaskDependencyAdd, serverapi.WorkflowTaskDependencyAddRequest{}, serverapi.WorkflowTaskDependencyAddResponse{}},
		{protocol.MethodWorkflowTaskDependencyRemove, serverapi.WorkflowTaskDependencyRemoveRequest{}, serverapi.WorkflowTaskDependencyRemoveResponse{}},
		{protocol.MethodWorkflowTaskDependencyList, serverapi.WorkflowTaskDependencyListRequest{}, serverapi.WorkflowTaskDependencyListResponse{}},
	}
	for _, tt := range tests {
		route, ok := RouteByMethod(tt.method)
		if !ok {
			t.Fatalf("RouteByMethod(%q) not found", tt.method)
		}
		if route.RequestType.Name() != reflect.TypeOf(tt.req).Name() || route.ResponseType.Name() != reflect.TypeOf(tt.resp).Name() {
			t.Fatalf("route %q types = %s/%s", tt.method, route.RequestType, route.ResponseType)
		}
	}
}
