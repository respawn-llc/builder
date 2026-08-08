package promptcontrol

import (
	"context"
	"errors"
	"sync"
	"testing"

	"core/server/requestmemo"
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

	batchCalls    int
	batchSession  runtimeids.SessionID
	batchStep     runtimeids.StepID
	batchCommands []sessionruntime.PromptAnswerCommand
	batchResults  []sessionruntime.PromptAnswerResult
	batchErr      error
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

func (s *stubPromptResponder) ResolvePromptBatch(
	_ context.Context,
	sessionID runtimeids.SessionID,
	stepID runtimeids.StepID,
	commands []sessionruntime.PromptAnswerCommand,
) ([]sessionruntime.PromptAnswerResult, error) {
	s.batchCalls++
	s.batchSession = sessionID
	s.batchStep = stepID
	s.batchCommands = append([]sessionruntime.PromptAnswerCommand(nil), commands...)
	return append([]sessionruntime.PromptAnswerResult(nil), s.batchResults...), s.batchErr
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

func (r *cancellationAfterAcceptanceResponder) ResolvePromptBatch(
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
		ClientRequestID: clientRequestID,
		SessionID:       "session-1",
		AskID:           "ask-1",
	}
}

func approvalAnswerRequest(clientRequestID string) serverapi.ApprovalAnswerRequest {
	commentary := "looks good"
	return serverapi.ApprovalAnswerRequest{
		ClientRequestID: clientRequestID,
		SessionID:       "session-1",
		ApprovalID:      "approval-1",
		Decision:        clientui.ApprovalDecisionAllowOnce,
		Commentary:      &commentary,
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
	answer, ok := responder.resolution.(askquestion.AskQuestionLegacyAnswer)
	if responder.sessionID != "session-1" || responder.promptID != "ask-1" || !ok ||
		answer.Answer == nil || *answer.Answer != "hello" {
		t.Fatalf("unexpected stored resolution: session=%q prompt=%q resolution=%+v", responder.sessionID, responder.promptID, responder.resolution)
	}
}

func TestServiceAnswerAskPreservesExactLegacyQuestionSlots(t *testing.T) {
	service, responder := newPromptControlTestService()
	req := askAnswerRequest("req-exact")
	req.Answer = "  answer  "
	req.FreeformAnswer = "  freeform  "

	if err := service.AnswerAsk(context.Background(), req); err != nil {
		t.Fatalf("AnswerAsk: %v", err)
	}
	answer, ok := responder.resolution.(askquestion.AskQuestionLegacyAnswer)
	if !ok {
		t.Fatalf("resolution type = %T", responder.resolution)
	}
	if answer.Answer == nil || *answer.Answer != req.Answer {
		t.Fatalf("Answer slot = %v, want exact submitted value", answer.Answer)
	}
	if answer.FreeformAnswer == nil || *answer.FreeformAnswer != req.FreeformAnswer {
		t.Fatalf("FreeformAnswer slot = %v, want exact submitted value", answer.FreeformAnswer)
	}
}

func TestServiceAnswerAskPreservesAbsentSelectedOption(t *testing.T) {
	service, responder := newPromptControlTestService()
	req := askAnswerRequest("req-freeform")
	req.FreeformAnswer = "typed"

	if err := service.AnswerAsk(context.Background(), req); err != nil {
		t.Fatalf("AnswerAsk: %v", err)
	}
	answer := responder.resolution.(askquestion.AskQuestionLegacyAnswer)
	if answer.SelectedOptionNumber != nil {
		t.Fatalf("selected option = %v, want nil", *answer.SelectedOptionNumber)
	}
}

func TestServiceAnswerAskMemoizesSelectedOptionByValue(t *testing.T) {
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
	request.SelectedOptionNumber = textutil.Value(1)
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
	if calls := responder.callCount(); calls != 1 {
		t.Fatalf("accepted answer submissions = %d, want 1", calls)
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
	if responder.promptID != "approval-1" {
		t.Fatalf("unexpected prompt id: %q", responder.promptID)
	}
	if responder.err == nil || responder.err.Error() != serverapi.ErrPromptAlreadyResolved.Error() {
		t.Fatalf("unexpected prompt error: %v", responder.err)
	}
	if responder.resolution != nil {
		t.Fatalf("unexpected resolution for prompt error: %+v", responder.resolution)
	}
}

func TestServiceAnswerApprovalPreservesExactCommentary(t *testing.T) {
	service, responder := newPromptControlTestService()
	req := approvalAnswerRequest("req-exact-approval")
	commentary := "  exact commentary  "
	req.Commentary = &commentary

	if err := service.AnswerApproval(context.Background(), req); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}
	approval, ok := responder.resolution.(askquestion.AskQuestionApproval)
	if !ok || approval.Commentary == nil || *approval.Commentary != commentary {
		t.Fatalf("Approval resolution = %+v, want exact commentary", responder.resolution)
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
	if responder.awaits != 0 {
		t.Fatalf("approval unexpectedly awaited a successor %d times", responder.awaits)
	}
}

func TestServiceAnswerPromptBatchTranslatesMixedEntriesAndValidatesReorderedResults(t *testing.T) {
	service, responder := newPromptControlTestService()
	request := promptAnswerBatchRequest(t)
	responder.batchResults = []sessionruntime.PromptAnswerResult{
		{PromptID: "declined-1", Outcome: sessionruntime.PromptAnswerOutcomeSkipped},
		{PromptID: "question-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
		{PromptID: "approval-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
	}

	response, err := service.AnswerPromptBatch(context.Background(), request)
	if err != nil {
		t.Fatalf("AnswerPromptBatch: %v", err)
	}
	if responder.batchCalls != 1 || responder.batchSession != request.SessionID || responder.batchStep != request.StepID {
		t.Fatalf("batch delegation = calls %d session %s step %s", responder.batchCalls, responder.batchSession, responder.batchStep)
	}
	if len(responder.batchCommands) != 3 {
		t.Fatalf("batch commands = %+v", responder.batchCommands)
	}
	question, ok := responder.batchCommands[0].Payload.(sessionruntime.PromptQuestionAnswerCommand)
	if !ok ||
		question.Answer.SelectedOptionNumber == nil ||
		*question.Answer.SelectedOptionNumber != 2 ||
		question.Answer.Freeform == nil ||
		*question.Answer.Freeform != "question commentary" {
		t.Fatalf("question command = %+v", responder.batchCommands[0])
	}
	approval, ok := responder.batchCommands[1].Payload.(sessionruntime.PromptApprovalAnswerCommand)
	if !ok ||
		approval.Answer.Decision != askquestion.AskQuestionApprovalDecisionDeny ||
		approval.Answer.Commentary == nil ||
		*approval.Answer.Commentary != "approval commentary" {
		t.Fatalf("approval command = %+v", responder.batchCommands[1])
	}
	if _, ok := responder.batchCommands[2].Payload.(sessionruntime.PromptDeclinedCommand); !ok {
		t.Fatalf("declined command = %+v", responder.batchCommands[2])
	}
	if err := serverapi.ValidatePromptAnswerBatchResponse(request, response); err != nil {
		t.Fatalf("response correlation: %v", err)
	}
}

func TestServiceAnswerPromptBatchPreservesAbsentOptionalText(t *testing.T) {
	service, responder := newPromptControlTestService()
	request := promptAnswerBatchRequest(t)
	request.Entries[0].QuestionAnswer.Freeform = nil
	request.Entries[1].ApprovalAnswer.Commentary = nil
	responder.batchResults = []sessionruntime.PromptAnswerResult{
		{PromptID: "question-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
		{PromptID: "approval-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
		{PromptID: "declined-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
	}

	if _, err := service.AnswerPromptBatch(context.Background(), request); err != nil {
		t.Fatalf("AnswerPromptBatch: %v", err)
	}
	question, ok := responder.batchCommands[0].Payload.(sessionruntime.PromptQuestionAnswerCommand)
	if !ok {
		t.Fatalf("question command = %+v", responder.batchCommands[0])
	}
	if question.Answer.Freeform != nil {
		t.Fatal("absent Question freeform became present")
	}
	approval, ok := responder.batchCommands[1].Payload.(sessionruntime.PromptApprovalAnswerCommand)
	if !ok {
		t.Fatalf("approval command = %+v", responder.batchCommands[1])
	}
	if approval.Answer.Commentary != nil {
		t.Fatal("absent Approval commentary became present")
	}
}

func TestPromptBatchTranslationInvariantUsesDebugAwarePolicy(t *testing.T) {
	t.Run("production", func(t *testing.T) {
		t.Setenv("KENT_DEBUG", "")
		t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
		if err := reportPromptBatchTranslationInvariant("prompt-1"); err == nil {
			t.Fatal("translation invariant did not surface an error")
		}
	})
	t.Run("debug", func(t *testing.T) {
		t.Setenv("KENT_INVARIANT_MODE", "panic")
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("translation invariant did not panic in debug mode")
			}
		}()
		_ = reportPromptBatchTranslationInvariant("prompt-1")
	})
}

func TestServiceAnswerPromptBatchRejectsMalformedRuntimeResultSets(t *testing.T) {
	request := promptAnswerBatchRequest(t)
	tests := []struct {
		name    string
		results []sessionruntime.PromptAnswerResult
	}{
		{
			name: "missing",
			results: []sessionruntime.PromptAnswerResult{
				{PromptID: "question-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
				{PromptID: "approval-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
			},
		},
		{
			name: "foreign",
			results: []sessionruntime.PromptAnswerResult{
				{PromptID: "question-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
				{PromptID: "approval-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
				{PromptID: "foreign", Outcome: sessionruntime.PromptAnswerOutcomeSkipped},
			},
		},
		{
			name: "duplicate",
			results: []sessionruntime.PromptAnswerResult{
				{PromptID: "question-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
				{PromptID: "question-1", Outcome: sessionruntime.PromptAnswerOutcomeSkipped},
				{PromptID: "declined-1", Outcome: sessionruntime.PromptAnswerOutcomeSkipped},
			},
		},
		{
			name: "invalid outcome",
			results: []sessionruntime.PromptAnswerResult{
				{PromptID: "question-1", Outcome: sessionruntime.PromptAnswerOutcome("later")},
				{PromptID: "approval-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
				{PromptID: "declined-1", Outcome: sessionruntime.PromptAnswerOutcomeSkipped},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, responder := newPromptControlTestService()
			responder.batchResults = test.results
			if _, err := service.AnswerPromptBatch(context.Background(), request); err == nil {
				t.Fatal("malformed runtime result set unexpectedly succeeded")
			}
		})
	}
}

func TestServiceAnswerPromptBatchDoesNotMemoizeRepeatedInvocation(t *testing.T) {
	service, responder := newPromptControlTestService()
	request := promptAnswerBatchRequest(t)
	responder.batchResults = []sessionruntime.PromptAnswerResult{
		{PromptID: "question-1", Outcome: sessionruntime.PromptAnswerOutcomeSkipped},
		{PromptID: "approval-1", Outcome: sessionruntime.PromptAnswerOutcomeSkipped},
		{PromptID: "declined-1", Outcome: sessionruntime.PromptAnswerOutcomeSkipped},
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.AnswerPromptBatch(context.Background(), request); err != nil {
			t.Fatalf("AnswerPromptBatch attempt %d: %v", attempt+1, err)
		}
	}
	if responder.batchCalls != 2 {
		t.Fatalf("batch responder calls = %d, want 2 independent invocations", responder.batchCalls)
	}
}

func promptAnswerBatchRequest(t *testing.T) serverapi.PromptAnswerBatchRequest {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	selected := 2
	questionCommentary := "question commentary"
	approvalCommentary := "approval commentary"
	return serverapi.PromptAnswerBatchRequest{
		SessionID: sessionID,
		StepID:    stepID,
		Entries: []serverapi.PromptAnswerBatchEntry{
			{
				PromptID: "question-1",
				QuestionAnswer: &serverapi.PromptQuestionAnswer{
					SelectedOptionNumber: &selected,
					Freeform:             &questionCommentary,
				},
			},
			{
				PromptID: "approval-1",
				ApprovalAnswer: &serverapi.PromptApprovalAnswer{
					Decision:   clientui.ApprovalDecisionDeny,
					Commentary: &approvalCommentary,
				},
			},
			{PromptID: "declined-1", Declined: &serverapi.PromptDeclined{}},
		},
	}
}
