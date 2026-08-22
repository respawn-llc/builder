package transport

import (
	"testing"

	"core/shared/clientui"
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

func TestGatewayPromptAnswerBatchRoundTripReturnsTypedAllSkippedSet(t *testing.T) {
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
	selected := 1
	request := serverapi.PromptAnswerBatchRequest{
		SessionID: sessionID,
		StepID:    stepID,
		Entries: []serverapi.PromptAnswerBatchEntry{
			{
				PromptID:       "question-1",
				QuestionAnswer: &serverapi.PromptQuestionAnswer{SelectedOptionNumber: &selected},
			},
			{
				PromptID: "approval-1",
				ApprovalAnswer: &serverapi.PromptApprovalAnswer{
					Decision: clientui.ApprovalDecisionDeny,
				},
			},
			{PromptID: "declined-1", Declined: &serverapi.PromptDeclined{}},
		},
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
	for _, result := range response.Results {
		if result.Outcome != serverapi.PromptAnswerBatchOutcomeSkipped {
			t.Fatalf("all-stale gateway result = %+v", response)
		}
	}
}
