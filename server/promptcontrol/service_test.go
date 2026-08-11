package promptcontrol

import (
	"context"
	"errors"
	"testing"

	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type stubPromptResponder struct {
	batchCalls    int
	batchSession  runtimeids.SessionID
	batchStep     runtimeids.StepID
	batchCommands []sessionruntime.PromptAnswerCommand
	batchResults  []sessionruntime.PromptAnswerResult
	batchErr      error

	followUpCalls   int
	followUpSession runtimeids.SessionID
	followUpStep    runtimeids.StepID
	followUpPrompt  clientui.PromptID
	followUp        serverapi.PromptFollowUpSubscription
	followUpErr     error
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

func (s *stubPromptResponder) SubscribePromptFollowUp(
	_ context.Context,
	sessionID runtimeids.SessionID,
	stepID runtimeids.StepID,
	promptID clientui.PromptID,
) (serverapi.PromptFollowUpSubscription, error) {
	s.followUpCalls++
	s.followUpSession = sessionID
	s.followUpStep = stepID
	s.followUpPrompt = promptID
	return s.followUp, s.followUpErr
}

type stubPromptFollowUpSubscription struct{}

func (*stubPromptFollowUpSubscription) Next(context.Context) (serverapi.PromptFollowUpEvent, error) {
	return serverapi.PromptFollowUpEvent{}, errors.New("unexpected Next")
}

func (*stubPromptFollowUpSubscription) Close() error { return nil }

func newPromptControlTestService() (*PromptControlService, *stubPromptResponder) {
	responder := &stubPromptResponder{}
	return NewPromptControlService(responder), responder
}

func TestServiceSubscribeFollowUpInstallsWatcherBeforeReturning(t *testing.T) {
	service, responder := newPromptControlTestService()
	request := serverapi.PromptFollowUpWatchRequest{
		SessionID: runtimeids.NewSessionID(),
		StepID:    promptControlStepID(t),
		PromptID:  "prompt-1",
	}
	subscription := &stubPromptFollowUpSubscription{}
	responder.followUp = subscription

	got, err := service.SubscribeFollowUp(context.Background(), request)
	if err != nil {
		t.Fatalf("SubscribeFollowUp: %v", err)
	}
	if got != subscription || responder.followUpCalls != 1 ||
		responder.followUpSession != request.SessionID ||
		responder.followUpStep != request.StepID ||
		responder.followUpPrompt != request.PromptID {
		t.Fatalf("follow-up installation = subscription %p responder %+v", got, responder)
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

func promptControlStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	return stepID
}
