package apicontract

import (
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkflowLabelRoutesUseExistingWorkflowBoundary(t *testing.T) {
	tests := []struct {
		method       string
		requestType  reflect.Type
		responseType reflect.Type
		auth         AuthPolicy
	}{
		{
			method:       protocol.MethodWorkflowProjectLabelCreate,
			requestType:  reflect.TypeOf(serverapi.WorkflowProjectLabelCreateRequest{}),
			responseType: reflect.TypeOf(serverapi.WorkflowProjectLabelCreateResponse{}),
			auth:         AuthServer,
		},
		{
			method:       protocol.MethodWorkflowProjectLabelList,
			requestType:  reflect.TypeOf(serverapi.WorkflowProjectLabelCatalogRequest{}),
			responseType: reflect.TypeOf(serverapi.WorkflowProjectLabelCatalogResponse{}),
			auth:         AuthPreServerAuth,
		},
		{
			method:       protocol.MethodWorkflowProjectLabelRename,
			requestType:  reflect.TypeOf(serverapi.WorkflowProjectLabelRenameRequest{}),
			responseType: reflect.TypeOf(serverapi.WorkflowProjectLabelRenameResponse{}),
			auth:         AuthServer,
		},
		{
			method:       protocol.MethodWorkflowProjectLabelDelete,
			requestType:  reflect.TypeOf(serverapi.WorkflowProjectLabelDeleteRequest{}),
			responseType: reflect.TypeOf(serverapi.WorkflowProjectLabelDeleteResponse{}),
			auth:         AuthServer,
		},
		{
			method:       protocol.MethodWorkflowTaskLabelsGet,
			requestType:  reflect.TypeOf(serverapi.WorkflowTaskLabelsGetRequest{}),
			responseType: reflect.TypeOf(serverapi.WorkflowTaskLabelsGetResponse{}),
			auth:         AuthPreServerAuth,
		},
		{
			method:       protocol.MethodWorkflowTaskLabelsUpdate,
			requestType:  reflect.TypeOf(serverapi.WorkflowTaskLabelsUpdateRequest{}),
			responseType: reflect.TypeOf(serverapi.WorkflowTaskLabelsUpdateResponse{}),
			auth:         AuthServer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			route, ok := RouteByMethod(tt.method)
			if !ok {
				t.Fatalf("route %q is missing", tt.method)
			}
			if route.Kind != KindUnary ||
				route.Auth != tt.auth ||
				route.Scope != ScopeProjectView ||
				route.Connection != ConnectionUnscoped ||
				route.Dependency != DependencyWorkflow ||
				route.RequestType != tt.requestType ||
				route.ResponseType != tt.responseType ||
				route.ValidationMethod == ValidationMethodNone {
				t.Fatalf("route %q = %+v", tt.method, route)
			}
		})
	}
}
