package promptcontrol

import (
	"context"
	"errors"
	"sync"
	"testing"

	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type stubPromptResponder struct {
	calls      int
	awaits     int
	sessionID  string
	promptID   string
	resolution askquestion.AskQuestionResolution
	err        error
	submitErr  error
}

type stubPromptAcceptance struct {
	responder *stubPromptResponder
}

func (a stubPromptAcceptance) AwaitSuccessor(context.Context) error {
	a.responder.awaits++
	return nil
}

func (s *stubPromptResponder) AcceptPromptResolution(
	sessionID string,
	promptID string,
	resolution askquestion.AskQuestionResolution,
	err error,
) (PromptResponseAcceptance, error) {
	s.calls++
	s.sessionID = sessionID
	s.promptID = promptID
	s.resolution = resolution
	s.err = err
	if s.submitErr != nil {
		return nil, s.submitErr
	}
	return stubPromptAcceptance{responder: s}, nil
}

func (*stubPromptResponder) ResolvePromptBatch(
	context.Context,
	runtimeids.SessionID,
	runtimeids.StepID,
	[]sessionruntime.PromptAnswerCommand,
) ([]sessionruntime.PromptAnswerResult, error) {
	panic("unexpected batch resolution")
}

type cancellationAfterAcceptanceResponder struct {
	mu        sync.Mutex
	calls     int
	accepted  chan struct{}
	successor chan struct{}
}

type cancellationAfterAcceptance struct {
	successor <-chan struct{}
}

func (a cancellationAfterAcceptance) AwaitSuccessor(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-a.successor:
		return nil
	}
}

func (r *cancellationAfterAcceptanceResponder) AcceptPromptResolution(
	_ string,
	_ string,
	_ askquestion.AskQuestionResolution,
	_ error,
) (PromptResponseAcceptance, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	r.accepted <- struct{}{}
	return cancellationAfterAcceptance{successor: r.successor}, nil
}

func (*cancellationAfterAcceptanceResponder) ResolvePromptBatch(
	context.Context,
	runtimeids.SessionID,
	runtimeids.StepID,
	[]sessionruntime.PromptAnswerCommand,
) ([]sessionruntime.PromptAnswerResult, error) {
	panic("unexpected batch resolution")
}

func (r *cancellationAfterAcceptanceResponder) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func newPromptControlTestService() (*PromptControlService, *stubPromptResponder) {
	responder := &stubPromptResponder{}
	return NewPromptControlService(responder), responder
}

func askAnswerRequest(clientRequestID string) serverapi.AskAnswerRequest {
	return serverapi.AskAnswerRequest{

		SessionID: "session-1",
		AskID:     "ask-1",
	}
}

func approvalAnswerRequest(clientRequestID string) serverapi.ApprovalAnswerRequest {
	commentary := "looks good"
	return serverapi.ApprovalAnswerRequest{

		SessionID:  "session-1",
		ApprovalID: "approval-1",
		Decision:   clientui.ApprovalDecisionAllowOnce,
		Commentary: &commentary,
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
	if responder.awaits != 1 {
		t.Fatalf("successor-aware responder call count = %d, want 1", responder.awaits)
	}
	answer, ok := responder.resolution.(askquestion.AskQuestionAnswer)
	if responder.sessionID != "session-1" || responder.promptID != "ask-1" ||
		!ok || answer.Freeform == nil || *answer.Freeform != "hello" {
		t.Fatalf(
			"unexpected stored resolution: session=%q prompt=%q resolution=%+v",
			responder.sessionID,
			responder.promptID,
			responder.resolution,
		)
	}
}

func TestServiceAnswerAskPreservesAbsentSelectedOption(t *testing.T) {
	service, responder := newPromptControlTestService()
	req := askAnswerRequest("req-freeform")
	req.FreeformAnswer = "typed"

	if err := service.AnswerAsk(context.Background(), req); err != nil {
		t.Fatalf("AnswerAsk: %v", err)
	}
	answer := responder.resolution.(askquestion.AskQuestionAnswer)
	if answer.SelectedOptionNumber != nil {
		t.Fatalf("selected option = %v, want nil", *answer.SelectedOptionNumber)
	}
}

func TestServiceAnswerAskTreatsRepeatedSelectedOptionAsNewAnswer(t *testing.T) {
	service, responder := newPromptControlTestService()
	request := askAnswerRequest("req-option")
	request.SelectedOptionNumber = textutil.Value(1)
	if err := service.AnswerAsk(context.Background(), request); err != nil {
		t.Fatalf("AnswerAsk first: %v", err)
	}
	request.SelectedOptionNumber = textutil.Value(1)
	if err := service.AnswerAsk(context.Background(), request); err != nil {
		t.Fatalf("AnswerAsk equivalent replay: %v", err)
	}
	if responder.calls != 2 {
		t.Fatalf("responder calls = %d, want 2", responder.calls)
	}
}

func TestServiceAnswerAskTreatsChangedSelectedOptionAsNewAnswer(t *testing.T) {
	service, responder := newPromptControlTestService()
	request := askAnswerRequest("req-presence")
	request.FreeformAnswer = "typed"
	if err := service.AnswerAsk(context.Background(), request); err != nil {
		t.Fatalf("AnswerAsk absent selection: %v", err)
	}
	request.SelectedOptionNumber = textutil.Value(1)
	if err := service.AnswerAsk(context.Background(), request); err != nil {
		t.Fatalf("AnswerAsk present selection: %v", err)
	}
	answer := responder.resolution.(askquestion.AskQuestionAnswer)
	if responder.calls != 2 || answer.SelectedOptionNumber == nil ||
		*answer.SelectedOptionNumber != 1 {
		t.Fatalf("responder = calls:%d resolution:%+v", responder.calls, responder.resolution)
	}
}

func TestServiceAnswerAskRepeatedCallReturnsPromptOwnerOutcome(t *testing.T) {
	service, responder := newPromptControlTestService()
	req := askAnswerRequest("req-1")
	req.Answer = "hello"

	if err := service.AnswerAsk(context.Background(), req); err != nil {
		t.Fatalf("AnswerAsk first: %v", err)
	}
	responder.submitErr = serverapi.ErrPromptAlreadyResolved
	if err := service.AnswerAsk(context.Background(), req); !errors.Is(err, serverapi.ErrPromptAlreadyResolved) {
		t.Fatalf("AnswerAsk repeated error = %v, want already resolved", err)
	}
	if responder.calls != 2 {
		t.Fatalf("responder call count = %d, want 2", responder.calls)
	}
}

func TestServiceAnswerAskRetryAfterCanceledSuccessorWaitDoesNotResubmitAcceptedAnswer(t *testing.T) {
	responder := &cancellationAfterAcceptanceResponder{
		accepted:  make(chan struct{}, 2),
		successor: make(chan struct{}),
	}
	service := NewPromptControlService(responder)
	req := askAnswerRequest("req-canceled-successor-wait")
	req.Answer = "hello"

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- service.AnswerAsk(ctx, req)
	}()
	<-responder.accepted
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first AnswerAsk error = %v, want cancellation", err)
	}

	close(responder.successor)
	if err := service.AnswerAsk(context.Background(), req); err != nil {
		t.Fatalf("retry AnswerAsk: %v", err)
	}
	if calls := responder.callCount(); calls != 2 {
		t.Fatalf("accepted answer submissions = %d, want 2", calls)
	}
}

func TestServiceAnswerAskTreatsChangedPayloadAsNewAnswer(t *testing.T) {
	service, responder := newPromptControlTestService()
	request := askAnswerRequest("req-1")
	request.Answer = "hello"
	if err := service.AnswerAsk(context.Background(), request); err != nil {
		t.Fatalf("AnswerAsk first: %v", err)
	}
	request.Answer = "different"
	if err := service.AnswerAsk(context.Background(), request); err != nil {
		t.Fatalf("AnswerAsk changed payload: %v", err)
	}
	answer := responder.resolution.(askquestion.AskQuestionAnswer)
	if responder.calls != 2 || answer.Freeform == nil || *answer.Freeform != "different" {
		t.Fatalf("responder call count/resolution = %d/%+v", responder.calls, responder.resolution)
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
	if responder.promptID != "approval-1" {
		t.Fatalf("unexpected prompt ID: %q", responder.promptID)
	}
	if responder.err == nil || responder.err.Error() != serverapi.ErrPromptAlreadyResolved.Error() {
		t.Fatalf("unexpected prompt error: %v", responder.err)
	}
	if responder.resolution != nil {
		t.Fatalf("unexpected approval resolution for prompt error: %+v", responder.resolution)
	}
}

func TestServiceAnswerApprovalRepeatedCallReturnsPromptOwnerOutcome(t *testing.T) {
	service, responder := newPromptControlTestService()
	req := approvalAnswerRequest("req-1")

	if err := service.AnswerApproval(context.Background(), req); err != nil {
		t.Fatalf("AnswerApproval first: %v", err)
	}
	responder.submitErr = serverapi.ErrPromptAlreadyResolved
	if err := service.AnswerApproval(context.Background(), req); !errors.Is(err, serverapi.ErrPromptAlreadyResolved) {
		t.Fatalf("AnswerApproval repeated error = %v, want already resolved", err)
	}
	if responder.calls != 2 {
		t.Fatalf("responder call count = %d, want 2", responder.calls)
	}
	if responder.awaits != 0 {
		t.Fatalf("approval unexpectedly awaited a successor %d times", responder.awaits)
	}
}
