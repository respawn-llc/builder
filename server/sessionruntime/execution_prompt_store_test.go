package sessionruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/tools"
)

func TestExecutionPromptStoreAnswerWaitsForPreparedSuccessor(t *testing.T) {
	store := newExecutionPromptStore(&Authority{}, ExecutionScope{}, nil)
	first := batchedPromptRequest("ask-1", 0)
	second := batchedPromptRequest("ask-2", 1)
	firstResult := make(chan executionPromptResult, 1)
	go func() {
		response, err := store.Await(context.Background(), first)
		firstResult <- executionPromptResult{response: response, err: err}
	}()
	requirePromptPending(t, &store, first.ID)

	answerDone := make(chan error, 1)
	go func() {
		answerDone <- store.SubmitAndAwaitSuccessor(
			context.Background(),
			tools.AskQuestionResponse{RequestID: first.ID, Answer: "one"},
			nil,
		)
	}()
	select {
	case result := <-firstResult:
		if result.err != nil || result.response.RequestID != first.ID {
			t.Fatalf("first prompt result = %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first prompt response")
	}
	select {
	case err := <-answerDone:
		t.Fatalf("answer returned before successor became pending: %v", err)
	default:
	}

	secondResult := make(chan executionPromptResult, 1)
	go func() {
		response, err := store.Await(context.Background(), second)
		secondResult <- executionPromptResult{response: response, err: err}
	}()
	select {
	case err := <-answerDone:
		if err != nil {
			t.Fatalf("successor-aware answer: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("answer did not return after successor became pending")
	}
	if err := store.Submit(tools.AskQuestionResponse{RequestID: second.ID, Answer: "two"}, nil); err != nil {
		t.Fatalf("submit second prompt: %v", err)
	}
	select {
	case result := <-secondResult:
		if result.err != nil || result.response.RequestID != second.ID {
			t.Fatalf("second prompt result = %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second prompt response")
	}
}

func TestExecutionPromptStoreAnswerReturnsWhenExecutionClosesWithoutPreparedSuccessor(t *testing.T) {
	store := newExecutionPromptStore(&Authority{}, ExecutionScope{}, nil)
	request := batchedPromptRequest("ask-1", 0)
	result := make(chan executionPromptResult, 1)
	go func() {
		response, err := store.Await(context.Background(), request)
		result <- executionPromptResult{response: response, err: err}
	}()
	requirePromptPending(t, &store, request.ID)

	answerDone := make(chan error, 1)
	go func() {
		answerDone <- store.SubmitAndAwaitSuccessor(
			context.Background(),
			tools.AskQuestionResponse{RequestID: request.ID, Answer: "one"},
			nil,
		)
	}()
	select {
	case <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for prompt response")
	}
	select {
	case err := <-answerDone:
		t.Fatalf("answer returned before exact execution closed: %v", err)
	default:
	}
	store.Close(context.Canceled)
	select {
	case err := <-answerDone:
		if err != nil {
			t.Fatalf("answer after close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("answer did not return after exact execution closed")
	}
}

func TestExecutionPromptStoreRejectsMalformedPreparedBatch(t *testing.T) {
	store := newExecutionPromptStore(&Authority{}, ExecutionScope{}, nil)
	request := batchedPromptRequest("ask-1", 0)
	request.QuestionBatch.BatchPromptIDs[0] = "different"
	go func() {
		_, _ = store.Await(context.Background(), request)
	}()
	requirePromptPending(t, &store, request.ID)

	err := store.SubmitAndAwaitSuccessor(
		context.Background(),
		tools.AskQuestionResponse{RequestID: request.ID, Answer: "one"},
		nil,
	)
	var invariantErr PromptBatchInvariantError
	if !errors.As(err, &invariantErr) {
		t.Fatalf("malformed batch error = %v, want PromptBatchInvariantError", err)
	}
	store.Close(context.Canceled)
}

func batchedPromptRequest(id string, ordinal int) tools.AskQuestionRequest {
	return tools.AskQuestionRequest{
		ID:       id,
		Question: id,
		Origin:   tools.AskQuestionOriginModelTool,
		RunID:    "run-1",
		StepID:   "step-1",
		QuestionBatch: &tools.AskQuestionBatchMetadata{
			Origin:              tools.AskQuestionOriginModelTool,
			RunID:               "run-1",
			StepID:              "step-1",
			BatchID:             "batch-1",
			PromptID:            id,
			BatchPromptIDs:      []string{"ask-1", "ask-2"},
			CandidateOrdinal:    ordinal,
			PreparedPromptCount: 2,
		},
	}
}

func requirePromptPending(t *testing.T, store *executionPromptStore, requestID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !store.hasPendingID(requestID) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for prompt %q", requestID)
		}
		time.Sleep(time.Millisecond)
	}
}
