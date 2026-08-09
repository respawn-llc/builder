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

func (*stubPromptFollowUpSubscription) Close() error {
	return nil
}

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

func TestServiceAnswerPromptBatchTranslatesMixedEntriesAndCorrelatesResults(t *testing.T) {
	service, responder := newPromptControlTestService()
	selected := 2
	request := serverapi.PromptAnswerBatchRequest{
		SessionID: runtimeids.NewSessionID(),
		StepID:    promptControlStepID(t),
		Entries: []serverapi.PromptAnswerBatchEntry{
			{PromptID: "question-1", QuestionAnswer: &serverapi.PromptQuestionAnswer{SelectedOptionNumber: &selected}},
			{PromptID: "approval-1", ApprovalAnswer: &serverapi.PromptApprovalAnswer{
				Decision: clientui.ApprovalDecisionDeny,
			}},
			{PromptID: "declined-1", Declined: &serverapi.PromptDeclined{}},
		},
	}
	responder.batchResults = []sessionruntime.PromptAnswerResult{
		{PromptID: "declined-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
		{PromptID: "approval-1", Outcome: sessionruntime.PromptAnswerOutcomeSkipped},
		{PromptID: "question-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
	}
	response, err := service.AnswerPromptBatch(context.Background(), request)
	if err != nil {
		t.Fatalf("AnswerPromptBatch: %v", err)
	}
	if responder.batchCalls != 1 || responder.batchSession != request.SessionID ||
		responder.batchStep != request.StepID || len(responder.batchCommands) != 3 {
		t.Fatalf("batch delegation = %+v", responder)
	}
	question, questionOK := responder.batchCommands[0].Payload.(sessionruntime.PromptQuestionAnswerCommand)
	approval, approvalOK := responder.batchCommands[1].Payload.(sessionruntime.PromptApprovalAnswerCommand)
	_, declinedOK := responder.batchCommands[2].Payload.(sessionruntime.PromptDeclinedCommand)
	if !questionOK || question.Answer.SelectedOptionNumber == nil ||
		*question.Answer.SelectedOptionNumber != selected || !approvalOK ||
		approval.Answer.Decision != askquestion.AskQuestionApprovalDecisionDeny ||
		!declinedOK {
		t.Fatalf("translated commands = %+v", responder.batchCommands)
	}
	if err := serverapi.ValidatePromptAnswerBatchResponse(request, response); err != nil {
		t.Fatalf("response correlation: %v", err)
	}
}

func TestServiceAnswerPromptBatchRejectsMalformedRuntimeResultSets(t *testing.T) {
	request := promptControlQuestionRequest(t)
	tests := []struct {
		name    string
		results []sessionruntime.PromptAnswerResult
	}{
		{name: "missing"},
		{name: "foreign", results: []sessionruntime.PromptAnswerResult{{PromptID: "foreign", Outcome: sessionruntime.PromptAnswerOutcomeResolved}}},
		{name: "invalid outcome", results: []sessionruntime.PromptAnswerResult{{PromptID: "question-1", Outcome: "later"}}},
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
	request := promptControlQuestionRequest(t)
	responder.batchResults = []sessionruntime.PromptAnswerResult{{
		PromptID: "question-1",
		Outcome:  sessionruntime.PromptAnswerOutcomeSkipped,
	}}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.AnswerPromptBatch(context.Background(), request); err != nil {
			t.Fatalf("AnswerPromptBatch attempt %d: %v", attempt+1, err)
		}
	}
	if responder.batchCalls != 2 {
		t.Fatalf("batch responder calls = %d, want 2 independent invocations", responder.batchCalls)
	}
}

func promptControlQuestionRequest(t *testing.T) serverapi.PromptAnswerBatchRequest {
	t.Helper()
	selected := 1
	return serverapi.PromptAnswerBatchRequest{
		SessionID: runtimeids.NewSessionID(),
		StepID:    promptControlStepID(t),
		Entries: []serverapi.PromptAnswerBatchEntry{{
			PromptID:       "question-1",
			QuestionAnswer: &serverapi.PromptQuestionAnswer{SelectedOptionNumber: &selected},
		}},
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
