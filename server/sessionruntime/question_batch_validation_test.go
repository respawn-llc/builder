package sessionruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/tools"
)

func TestQuestionBatchMetadataValidationIsIdenticalForDirectAndObservedResolution(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tools.AskQuestionRequest)
	}{
		{name: "metadata origin", mutate: func(request *tools.AskQuestionRequest) { request.QuestionBatch.Origin = "invalid" }},
		{name: "request origin agreement", mutate: func(request *tools.AskQuestionRequest) { request.Origin = "" }},
		{name: "blank run identity", mutate: func(request *tools.AskQuestionRequest) {
			request.RunID = ""
			request.QuestionBatch.RunID = ""
		}},
		{name: "non-normalized run identity", mutate: func(request *tools.AskQuestionRequest) {
			request.RunID = " run-1 "
			request.QuestionBatch.RunID = " run-1 "
		}},
		{name: "blank step identity", mutate: func(request *tools.AskQuestionRequest) { request.QuestionBatch.StepID = "" }},
		{name: "non-normalized step identity", mutate: func(request *tools.AskQuestionRequest) { request.QuestionBatch.StepID = " " + request.StepID + " " }},
		{name: "request run mismatch", mutate: func(request *tools.AskQuestionRequest) { request.QuestionBatch.RunID = "run-other" }},
		{name: "request step mismatch", mutate: func(request *tools.AskQuestionRequest) { request.QuestionBatch.StepID = "step-other" }},
		{name: "metadata prompt identity", mutate: func(request *tools.AskQuestionRequest) { request.QuestionBatch.PromptID = "other" }},
		{name: "prepared count", mutate: func(request *tools.AskQuestionRequest) { request.QuestionBatch.PreparedPromptCount = 3 }},
		{name: "candidate ordinal", mutate: func(request *tools.AskQuestionRequest) { request.QuestionBatch.CandidateOrdinal = 2 }},
		{name: "candidate identity", mutate: func(request *tools.AskQuestionRequest) {
			request.QuestionBatch.BatchPromptIDs = []string{"other", "ask-2"}
		}},
		{name: "blank candidate id", mutate: func(request *tools.AskQuestionRequest) { request.QuestionBatch.BatchPromptIDs = []string{"", "ask-2"} }},
		{name: "non-normalized candidate id", mutate: func(request *tools.AskQuestionRequest) {
			request.QuestionBatch.BatchPromptIDs = []string{" ask-1 ", "ask-2"}
		}},
		{name: "duplicate candidate id", mutate: func(request *tools.AskQuestionRequest) {
			request.QuestionBatch.BatchPromptIDs = []string{"ask-1", "ask-1"}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := questionBatchValidationRequest(t)
			test.mutate(&request)
			directErr, observedErr := resolveMalformedQuestionBatch(t, request, false), resolveMalformedQuestionBatch(t, request, true)
			var directInvariant, observedInvariant PromptBatchInvariantError
			if !errors.As(directErr, &directInvariant) || !errors.As(observedErr, &observedInvariant) {
				t.Fatalf("errors = direct %v observed %v, want typed batch invariants", directErr, observedErr)
			}
			if directInvariant != observedInvariant {
				t.Fatalf("invariants differ: direct %+v observed %+v", directInvariant, observedInvariant)
			}
		})
	}
}

func TestValidatedQuestionBatchDescriptorOwnsSuccessorIdentityWithoutDirectObservation(t *testing.T) {
	request := questionBatchValidationRequest(t)
	descriptor, err := validateQuestionBatchMetadata(request)
	if err != nil {
		t.Fatalf("validate QuestionBatch: %v", err)
	}
	request.QuestionBatch.BatchPromptIDs[1] = "mutated"
	if got, want := descriptor.successorPromptIDs(), []string{"ask-2"}; !equalPromptBatchStrings(got, want) {
		t.Fatalf("successor IDs = %v, want immutable %v", got, want)
	}
	store, _ := newPromptBatchStore(t)
	stepID := promptBatchStepID(t)
	installPromptBatchEntries(&store, promptBatchEntry(questionBatchValidationRequest(t), time.Unix(1, 0)))
	resolveWatchedPrompt(t, &store, stepID)
	if len(store.promptFollowUps) != 0 {
		t.Fatalf("direct batch allocated follow-up state: %+v", store.promptFollowUps)
	}
}

func questionBatchValidationRequest(t *testing.T) tools.AskQuestionRequest {
	t.Helper()
	request := batchedPromptRequest("ask-1", 0)
	request.StepID = promptBatchStepID(t).String()
	request.QuestionBatch.StepID = request.StepID
	request.Suggestions = []string{"one", "two"}
	return request
}

func resolveMalformedQuestionBatch(t *testing.T, request tools.AskQuestionRequest, watched bool) error {
	t.Helper()
	store, feed := newPromptBatchStore(t)
	stepID := promptBatchStepID(t)
	entry := promptBatchQuestion("ask-1", stepID, time.Unix(1, 0))
	entry.snapshot.Request = request
	installPromptBatchEntries(&store, entry)
	if watched {
		subscription, err := store.subscribePromptFollowUp(stepID, "ask-1")
		if err != nil {
			t.Fatalf("subscribe malformed watched resolution: %v", err)
		}
		defer func() { _ = subscription.Close() }()
	}
	wantFollowUps := len(store.promptFollowUps)
	_, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptDeclined("ask-1"),
	})
	if err == nil {
		t.Fatal("malformed Question batch unexpectedly succeeded")
	}
	if !store.hasPendingID("ask-1") || len(feed.resolvedIDs()) != 0 || len(store.promptFollowUps) != wantFollowUps {
		t.Fatal("malformed Question batch mutated, published, or changed follow-up state")
	}
	return err
}
