package transport

import (
	"testing"

	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestGatewayRegistersPromptAnswerBatchTypedResponseHandler(t *testing.T) {
	if _, exists := gatewayUnaryHandlerEntries[protocol.MethodPromptAnswerBatch]; !exists {
		t.Fatal("gateway prompt answer batch handler missing")
	}
}

func TestGatewayPromptAnswerBatchRoundTrip(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	request := serverapi.PromptAnswerBatchRequest{
		SessionID: sessionID,
		StepID:    stepID,
		Entries:   []serverapi.PromptAnswerBatchEntry{{ToolCallID: "declined-1", Declined: &serverapi.PromptDeclined{}}},
	}

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	if result := attachGatewayProject(t, conn, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()}); result.GetSuccess() == nil {
		t.Fatalf("attach Project failed: %+v", result.GetError())
	}
	var response serverapi.PromptAnswerBatchResponse
	callGateway(t, conn, "prompt-answer-batch", protocol.MethodPromptAnswerBatch, request, &response)
	if err := serverapi.ValidatePromptAnswerBatchResponse(request, response); err != nil {
		t.Fatalf("ValidatePromptAnswerBatchResponse: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Outcome != serverapi.PromptAnswerBatchOutcomeSkipped {
		t.Fatalf("gateway response = %+v", response)
	}
}
