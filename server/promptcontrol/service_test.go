package promptcontrol

import (
	"context"
	"errors"
	"reflect"
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

func TestServiceAnswerPromptBatchTranslatesQuestionApprovalAndDeclined(t *testing.T) {
	tests := []struct {
		name    string
		entry   serverapi.PromptAnswerBatchEntry
		assert  func(*testing.T, sessionruntime.PromptAnswerPayload)
		outcome sessionruntime.PromptAnswerOutcome
	}{
		{
			name: "question",
			entry: func() serverapi.PromptAnswerBatchEntry {
				selected := 2
				freeform := "question commentary"
				return serverapi.PromptAnswerBatchEntry{
					PromptID: "question-1",
					QuestionAnswer: &serverapi.PromptQuestionAnswer{
						SelectedOptionNumber: &selected,
						Freeform:             &freeform,
					},
				}
			}(),
			assert: func(t *testing.T, payload sessionruntime.PromptAnswerPayload) {
				t.Helper()
				answer, ok := payload.(sessionruntime.PromptQuestionAnswerCommand)
				if !ok || answer.Answer.SelectedOptionNumber == nil ||
					*answer.Answer.SelectedOptionNumber != 2 ||
					answer.Answer.Freeform == nil ||
					*answer.Answer.Freeform != "question commentary" {
					t.Fatalf("question payload = %+v", payload)
				}
			},
			outcome: sessionruntime.PromptAnswerOutcomeResolved,
		},
		{
			name: "approval",
			entry: func() serverapi.PromptAnswerBatchEntry {
				commentary := "approval commentary"
				return serverapi.PromptAnswerBatchEntry{
					PromptID: "approval-1",
					ApprovalAnswer: &serverapi.PromptApprovalAnswer{
						Decision:   clientui.ApprovalDecisionDeny,
						Commentary: &commentary,
					},
				}
			}(),
			assert: func(t *testing.T, payload sessionruntime.PromptAnswerPayload) {
				t.Helper()
				answer, ok := payload.(sessionruntime.PromptApprovalAnswerCommand)
				if !ok ||
					answer.Answer.Decision != askquestion.AskQuestionApprovalDecisionDeny ||
					answer.Answer.Commentary == nil ||
					*answer.Answer.Commentary != "approval commentary" {
					t.Fatalf("approval payload = %+v", payload)
				}
			},
			outcome: sessionruntime.PromptAnswerOutcomeSkipped,
		},
		{
			name:  "declined",
			entry: serverapi.PromptAnswerBatchEntry{PromptID: "declined-1", Declined: &serverapi.PromptDeclined{}},
			assert: func(t *testing.T, payload sessionruntime.PromptAnswerPayload) {
				t.Helper()
				if _, ok := payload.(sessionruntime.PromptDeclinedCommand); !ok {
					t.Fatalf("declined payload = %+v", payload)
				}
			},
			outcome: sessionruntime.PromptAnswerOutcomeResolved,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, responder := newPromptControlTestService()
			request := serverapi.PromptAnswerBatchRequest{
				SessionID: runtimeids.NewSessionID(),
				StepID:    promptControlStepID(t),
				Entries:   []serverapi.PromptAnswerBatchEntry{test.entry},
			}
			responder.batchResults = []sessionruntime.PromptAnswerResult{{
				PromptID: test.entry.PromptID,
				Outcome:  test.outcome,
			}}

			response, err := service.AnswerPromptBatch(context.Background(), request)
			if err != nil {
				t.Fatalf("AnswerPromptBatch: %v", err)
			}
			if responder.batchCalls != 1 ||
				responder.batchSession != request.SessionID ||
				responder.batchStep != request.StepID ||
				len(responder.batchCommands) != 1 ||
				responder.batchCommands[0].PromptID != test.entry.PromptID {
				t.Fatalf("batch delegation = %+v", responder)
			}
			test.assert(t, responder.batchCommands[0].Payload)
			if err := serverapi.ValidatePromptAnswerBatchResponse(request, response); err != nil {
				t.Fatalf("response correlation: %v", err)
			}
		})
	}
}

func TestServiceAnswerPromptBatchPreservesMixedResolvedAndSkippedResults(t *testing.T) {
	service, responder := newPromptControlTestService()
	selected := 1
	request := serverapi.PromptAnswerBatchRequest{
		SessionID: runtimeids.NewSessionID(),
		StepID:    promptControlStepID(t),
		Entries: []serverapi.PromptAnswerBatchEntry{
			{PromptID: "question-1", QuestionAnswer: &serverapi.PromptQuestionAnswer{SelectedOptionNumber: &selected}},
			{PromptID: "approval-1", ApprovalAnswer: &serverapi.PromptApprovalAnswer{Decision: clientui.ApprovalDecisionAllowOnce}},
		},
	}
	responder.batchResults = []sessionruntime.PromptAnswerResult{
		{PromptID: "approval-1", Outcome: sessionruntime.PromptAnswerOutcomeSkipped},
		{PromptID: "question-1", Outcome: sessionruntime.PromptAnswerOutcomeResolved},
	}

	response, err := service.AnswerPromptBatch(context.Background(), request)
	if err != nil {
		t.Fatalf("AnswerPromptBatch: %v", err)
	}
	if err := serverapi.ValidatePromptAnswerBatchResponse(request, response); err != nil {
		t.Fatalf("response correlation: %v", err)
	}
}

func TestServiceAnswerPromptBatchRejectsMalformedRuntimeResultSets(t *testing.T) {
	selected := 1
	request := serverapi.PromptAnswerBatchRequest{
		SessionID: runtimeids.NewSessionID(),
		StepID:    promptControlStepID(t),
		Entries: []serverapi.PromptAnswerBatchEntry{{
			PromptID:       "question-1",
			QuestionAnswer: &serverapi.PromptQuestionAnswer{SelectedOptionNumber: &selected},
		}},
	}
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
	selected := 1
	request := serverapi.PromptAnswerBatchRequest{
		SessionID: runtimeids.NewSessionID(),
		StepID:    promptControlStepID(t),
		Entries: []serverapi.PromptAnswerBatchEntry{{
			PromptID:       "question-1",
			QuestionAnswer: &serverapi.PromptQuestionAnswer{SelectedOptionNumber: &selected},
		}},
	}
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

func TestPromptControlServiceHasOnlyBatchAndFollowUpMethods(t *testing.T) {
	serviceType := reflect.TypeOf((*PromptControlService)(nil))
	if got := serviceType.NumMethod(); got != 2 {
		t.Fatalf("PromptControlService method count = %d, want 2", got)
	}
	for _, method := range []string{"AnswerPromptBatch", "SubscribeFollowUp"} {
		if _, exists := serviceType.MethodByName(method); !exists {
			t.Fatalf("PromptControlService missing %s", method)
		}
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
