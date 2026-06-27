package promptcontrol

import (
	"context"
	"errors"
	"testing"

	"core/server/requestmemo"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type stubPromptResponder struct {
	calls     int
	sessionID string
	response  askquestion.AskQuestionResponse
	err       error
	submitErr error
}

func (s *stubPromptResponder) SubmitPromptResponse(sessionID string, resp askquestion.AskQuestionResponse, err error) error {
	s.calls++
	s.sessionID = sessionID
	s.response = resp
	s.err = err
	return s.submitErr
}

func TestServiceAnswerAskSubmitsResponse(t *testing.T) {
	responder := &stubPromptResponder{}
	service := NewPromptControlService(responder)
	req := serverapi.AskAnswerRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
		AskID:           "ask-1",
		Answer:          "hello",
	}

	if err := service.AnswerAsk(context.Background(), req); err != nil {
		t.Fatalf("AnswerAsk: %v", err)
	}
	if responder.calls != 1 {
		t.Fatalf("responder call count = %d, want 1", responder.calls)
	}
	if responder.sessionID != "session-1" || responder.response.RequestID != "ask-1" || responder.response.Answer != "hello" {
		t.Fatalf("unexpected stored response: session=%q response=%+v", responder.sessionID, responder.response)
	}
}

func TestServiceAnswerAskDedupesSuccessfulRetry(t *testing.T) {
	responder := &stubPromptResponder{}
	service := NewPromptControlService(responder)
	req := serverapi.AskAnswerRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
		AskID:           "ask-1",
		Answer:          "hello",
	}

	if err := service.AnswerAsk(context.Background(), req); err != nil {
		t.Fatalf("AnswerAsk first: %v", err)
	}
	responder.submitErr = serverapi.ErrPromptAlreadyResolved
	if err := service.AnswerAsk(context.Background(), req); err != nil {
		t.Fatalf("AnswerAsk replay: %v", err)
	}
	if responder.calls != 1 {
		t.Fatalf("responder call count = %d, want 1", responder.calls)
	}
}

func TestServiceAnswerAskRejectsClientRequestIDPayloadMismatch(t *testing.T) {
	responder := &stubPromptResponder{}
	service := NewPromptControlService(responder)
	if err := service.AnswerAsk(context.Background(), serverapi.AskAnswerRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
		AskID:           "ask-1",
		Answer:          "hello",
	}); err != nil {
		t.Fatalf("AnswerAsk first: %v", err)
	}
	err := service.AnswerAsk(context.Background(), serverapi.AskAnswerRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
		AskID:           "ask-1",
		Answer:          "different",
	})
	if !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("AnswerAsk mismatch error = %v, want reused with different parameters", err)
	}
	if responder.calls != 1 {
		t.Fatalf("responder call count = %d, want 1", responder.calls)
	}
}

func TestServiceAnswerApprovalSubmitsPromptError(t *testing.T) {
	responder := &stubPromptResponder{submitErr: serverapi.ErrPromptAlreadyResolved}
	service := NewPromptControlService(responder)
	req := serverapi.ApprovalAnswerRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
		ApprovalID:      "approval-1",
		Decision:        clientui.ApprovalDecisionAllowOnce,
		Commentary:      "looks good",
		ErrorMessage:    serverapi.ErrPromptAlreadyResolved.Error(),
	}

	err := service.AnswerApproval(context.Background(), req)
	if !errors.Is(err, serverapi.ErrPromptAlreadyResolved) {
		t.Fatalf("AnswerApproval error = %v, want ErrPromptAlreadyResolved", err)
	}
	if responder.calls != 1 {
		t.Fatalf("responder call count = %d, want 1", responder.calls)
	}
	if responder.response.RequestID != "approval-1" {
		t.Fatalf("unexpected response: %+v", responder.response)
	}
	if responder.err == nil || responder.err.Error() != serverapi.ErrPromptAlreadyResolved.Error() {
		t.Fatalf("unexpected prompt error: %v", responder.err)
	}
	if responder.response.Approval != nil {
		t.Fatalf("unexpected approval payload for prompt error: %+v", responder.response.Approval)
	}
}

func TestServiceAnswerApprovalDedupesSuccessfulRetry(t *testing.T) {
	responder := &stubPromptResponder{}
	service := NewPromptControlService(responder)
	req := serverapi.ApprovalAnswerRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
		ApprovalID:      "approval-1",
		Decision:        clientui.ApprovalDecisionAllowOnce,
		Commentary:      "looks good",
	}

	if err := service.AnswerApproval(context.Background(), req); err != nil {
		t.Fatalf("AnswerApproval first: %v", err)
	}
	responder.submitErr = serverapi.ErrPromptAlreadyResolved
	if err := service.AnswerApproval(context.Background(), req); err != nil {
		t.Fatalf("AnswerApproval replay: %v", err)
	}
	if responder.calls != 1 {
		t.Fatalf("responder call count = %d, want 1", responder.calls)
	}
}
