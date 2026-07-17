package promptcontrol

import (
	"context"
	"errors"
	"testing"

	"core/server/requestmemo"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/textutil"
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

func newPromptControlTestService() (*PromptControlService, *stubPromptResponder) {
	responder := &stubPromptResponder{}
	return NewPromptControlService(responder), responder
}

func askAnswerRequest(clientRequestID string) serverapi.AskAnswerRequest {
	return serverapi.AskAnswerRequest{
		ClientRequestID: clientRequestID,
		SessionID:       "session-1",
		AskID:           "ask-1",
	}
}

func approvalAnswerRequest(clientRequestID string) serverapi.ApprovalAnswerRequest {
	return serverapi.ApprovalAnswerRequest{
		ClientRequestID: clientRequestID,
		SessionID:       "session-1",
		ApprovalID:      "approval-1",
		Decision:        clientui.ApprovalDecisionAllowOnce,
		Commentary:      "looks good",
	}
}

func TestServiceAnswerAskSubmitsResponse(t *testing.T) {
	service, responder := newPromptControlTestService()
	req := askAnswerRequest("req-1")
	req.Answer = "hello"

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

func TestServiceAnswerAskPreservesAbsentSelectedOption(t *testing.T) {
	service, responder := newPromptControlTestService()
	req := askAnswerRequest("req-freeform")
	req.FreeformAnswer = "typed"

	if err := service.AnswerAsk(context.Background(), req); err != nil {
		t.Fatalf("AnswerAsk: %v", err)
	}
	if responder.response.SelectedOptionNumber != nil {
		t.Fatalf("selected option = %v, want nil", *responder.response.SelectedOptionNumber)
	}
}

func TestServiceAnswerAskMemoizesSelectedOptionByValue(t *testing.T) {
	service, responder := newPromptControlTestService()
	request := askAnswerRequest("req-option")
	request.SelectedOptionNumber = textutil.Int(1)
	if err := service.AnswerAsk(context.Background(), request); err != nil {
		t.Fatalf("AnswerAsk first: %v", err)
	}
	request.SelectedOptionNumber = textutil.Int(1)
	if err := service.AnswerAsk(context.Background(), request); err != nil {
		t.Fatalf("AnswerAsk equivalent replay: %v", err)
	}
	if responder.calls != 1 {
		t.Fatalf("responder calls = %d, want 1", responder.calls)
	}
}

func TestServiceAnswerAskDistinguishesAbsentAndPresentSelectedOption(t *testing.T) {
	service, _ := newPromptControlTestService()
	request := askAnswerRequest("req-presence")
	request.FreeformAnswer = "typed"
	if err := service.AnswerAsk(context.Background(), request); err != nil {
		t.Fatalf("AnswerAsk absent selection: %v", err)
	}
	request.SelectedOptionNumber = textutil.Int(1)
	if err := service.AnswerAsk(context.Background(), request); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("AnswerAsk present selection replay error = %v, want payload mismatch", err)
	}
}

func TestServiceAnswerAskDedupesSuccessfulRetry(t *testing.T) {
	service, responder := newPromptControlTestService()
	req := askAnswerRequest("req-1")
	req.Answer = "hello"

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
	service, responder := newPromptControlTestService()
	request := askAnswerRequest("req-1")
	request.Answer = "hello"
	if err := service.AnswerAsk(context.Background(), request); err != nil {
		t.Fatalf("AnswerAsk first: %v", err)
	}
	request.Answer = "different"
	err := service.AnswerAsk(context.Background(), request)
	if !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("AnswerAsk mismatch error = %v, want reused with different parameters", err)
	}
	if responder.calls != 1 {
		t.Fatalf("responder call count = %d, want 1", responder.calls)
	}
}

func TestServiceAnswerApprovalSubmitsPromptError(t *testing.T) {
	service, responder := newPromptControlTestService()
	responder.submitErr = serverapi.ErrPromptAlreadyResolved
	req := approvalAnswerRequest("req-1")
	req.ErrorMessage = serverapi.ErrPromptAlreadyResolved.Error()

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
	service, responder := newPromptControlTestService()
	req := approvalAnswerRequest("req-1")

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
