package transport

import (
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestWorkflowCatalogLinkAndGraphRejectMalformedNetworkAndRawRequests(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	t.Cleanup(func() { _ = appCore.Close() })
	t.Cleanup(server.Close)

	raw := appCore.WorkflowClient()
	conn := dialGateway(t, server)
	t.Cleanup(func() { _ = conn.Close() })
	handshakeGateway(t, conn)
	var created serverapi.WorkflowCreateResponse
	callGateway(t, conn, "workflow-validation-create", protocol.MethodWorkflowCreate, serverapi.WorkflowCreateRequest{
		Name: "Validation target",
	}, &created)

	tests := []struct {
		name       string
		method     string
		request    any
		directCall func() error
	}{
		{
			name:    "catalog",
			method:  protocol.MethodWorkflowCreate,
			request: serverapi.WorkflowCreateRequest{Name: " "},
			directCall: func() error {
				_, err := raw.CreateWorkflow(t.Context(), serverapi.WorkflowCreateRequest{Name: " "})
				return err
			},
		},
		{
			name:   "project link",
			method: protocol.MethodWorkflowLinkProject,
			request: serverapi.WorkflowLinkProjectRequest{
				ProjectID:     appCore.ProjectID(),
				WorkflowID:    runtimeids.NewWorkflowID(),
				DefaultPolicy: "invalid",
			},
			directCall: func() error {
				_, err := raw.LinkWorkflowToProject(t.Context(), serverapi.WorkflowLinkProjectRequest{
					ProjectID:     appCore.ProjectID(),
					WorkflowID:    runtimeids.NewWorkflowID(),
					DefaultPolicy: "invalid",
				})
				return err
			},
		},
		{
			name:   "graph",
			method: protocol.MethodWorkflowGraphSave,
			request: serverapi.WorkflowGraphSaveRequest{
				WorkflowID:      created.Workflow.ID,
				ExpectedVersion: -1,
			},
			directCall: func() error {
				_, err := raw.SaveWorkflowGraph(t.Context(), serverapi.WorkflowGraphSaveRequest{
					WorkflowID:      created.Workflow.ID,
					ExpectedVersion: -1,
				})
				var validationErr serverapi.WorkflowRequestValidationError
				if !errors.As(err, &validationErr) ||
					validationErr.Field != "expected_version" ||
					validationErr.Code != serverapi.WorkflowRequestErrorInvalidValue {
					t.Fatalf("raw graph error = %v, want typed expected_version validation error", err)
				}
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			networkErr := callGatewayExpectError(t, conn, "workflow-validation-"+test.name, test.method, test.request)
			if networkErr.Code != protocol.ErrCodeInvalidParams {
				t.Fatalf("network error = %+v, want invalid params", networkErr)
			}
			if err := test.directCall(); err == nil {
				t.Fatal("raw service request unexpectedly succeeded")
			}
		})
	}
}
