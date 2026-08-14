package transport

import (
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestGatewayRejectsInvalidScopedUnaryBeforeAuthorization(t *testing.T) {
	_, server := newGatewayTestServer(t)
	conn := dialGateway(t, server)
	defer conn.Close()
	handshakeGateway(t, conn)

	for _, testCase := range []struct {
		name   string
		method string
		params any
	}{
		{
			name:   "session main view",
			method: protocol.MethodSessionGetMainView,
			params: serverapi.SessionMainViewRequest{},
		},
		{
			name:   "process get",
			method: protocol.MethodProcessGet,
			params: serverapi.ProcessGetRequest{},
		},
		{
			name:   "process kill",
			method: protocol.MethodProcessKill,
			params: serverapi.ProcessKillRequest{ClientRequestID: "invalid-process-kill"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := callGatewayExpectError(t, conn, "invalid-"+testCase.name, testCase.method, testCase.params)
			if response.Code != protocol.ErrCodeInvalidParams {
				t.Fatalf("error code = %d, want InvalidParams; response=%+v", response.Code, response)
			}
		})
	}
}

func TestGatewayRejectsInvalidSubscriptionBeforeAttachmentAuthorization(t *testing.T) {
	_, server := newGatewayTestServer(t)
	conn := dialGateway(t, server)
	defer conn.Close()
	handshakeGateway(t, conn)

	response := callGatewayExpectError(
		t,
		conn,
		"invalid-transcript-subscription",
		protocol.MethodSessionSubscribeTranscript,
		serverapi.TranscriptSubscribeRequest{},
	)
	if response.Code != protocol.ErrCodeInvalidParams {
		t.Fatalf("error code = %d, want InvalidParams; response=%+v", response.Code, response)
	}
}

func TestGatewayRejectsInvalidRunPromptBeforeProjectAuthorization(t *testing.T) {
	_, server := newUnboundGatewayTestServer(t)
	conn := dialGateway(t, server)
	defer conn.Close()
	handshakeGateway(t, conn)

	response := callGatewayExpectError(t, conn, "invalid-run-prompt", protocol.MethodRunPrompt, serverapi.RunPromptRequest{
		ClientRequestID: "invalid-run-prompt",
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	})
	if response.Code != protocol.ErrCodeInvalidParams {
		t.Fatalf("error code = %d, want InvalidParams; response=%+v", response.Code, response)
	}
}

func TestGatewayMapsRawOwnerValidationToInvalidParams(t *testing.T) {
	_, server := newGatewayTestServer(t)
	conn := dialGateway(t, server)
	defer conn.Close()
	handshakeGateway(t, conn)

	response := callGatewayExpectError(t, conn, "invalid-workflow-create", protocol.MethodWorkflowCreate, serverapi.WorkflowCreateRequest{})
	if response.Code != protocol.ErrCodeInvalidParams {
		t.Fatalf("error code = %d, want InvalidParams; response=%+v", response.Code, response)
	}
}

func TestGatewayPreservesStructuredWorkflowFilterValidation(t *testing.T) {
	_, server := newGatewayTestServer(t)
	conn := dialGateway(t, server)
	defer conn.Close()
	handshakeGateway(t, conn)

	projectID := "project-1"
	response := callGatewayExpectError(t, conn, "invalid-workflow-filter", protocol.MethodWorkflowTaskList, serverapi.WorkflowTaskListRequest{
		ProjectID: &projectID,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: "invalid",
		},
	})
	if response.Code != protocol.ErrCodeWorkflowLabel {
		t.Fatalf("error code = %d, want WorkflowLabel; response=%+v", response.Code, response)
	}
}

func TestGatewayValidatesBeforeProjectAndSessionResolution(t *testing.T) {
	_, server := newUnboundGatewayTestServer(t)
	conn := dialGateway(t, server)
	defer conn.Close()
	handshakeGateway(t, conn)

	for _, testCase := range []struct {
		name   string
		method string
		params any
	}{
		{
			name:   "session plan",
			method: protocol.MethodSessionPlan,
			params: serverapi.SessionPlanRequest{
				Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
			},
		},
		{name: "Goal show", method: protocol.MethodRuntimeGoalShow, params: serverapi.RuntimeGoalShowRequest{SessionID: "../session"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := callGatewayExpectError(t, conn, "invalid-"+testCase.name, testCase.method, testCase.params)
			if response.Code != protocol.ErrCodeInvalidParams {
				t.Fatalf("error code = %d, want InvalidParams; response=%+v", response.Code, response)
			}
		})
	}
}
