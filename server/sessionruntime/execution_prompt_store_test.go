package sessionruntime

import (
	"context"
	"testing"
	"time"

	"core/server/tools"
	"core/shared/runtimeids"
)

func TestExecutionPromptStoreAnswerWaitsForPreparedSuccessor(t *testing.T) {
	feed := make(authorityPromptFeed)
	store := newExecutionPromptStoreForTest(t, feed)
	first := batchedPromptRequest("ask-1", 0)
	second := batchedPromptRequest("ask-2", 1)
	firstResult := make(chan executionPromptResult, 1)
	go func() {
		response, err := store.Await(context.Background(), first)
		firstResult <- executionPromptResult{response: response, err: err}
	}()
	pending := <-feed
	if pending.requestID != first.ID || pending.resolved {
		t.Fatalf("first prompt event = %+v, want pending %q", pending, first.ID)
	}
	requirePromptPending(t, &store, first.ID)

	answerDone := make(chan error, 1)
	go func() {
		acceptance, err := store.Accept(
			tools.AskQuestionResponse{RequestID: first.ID, Answer: "one"},
			nil,
		)
		if err != nil {
			answerDone <- err
			return
		}
		answerDone <- acceptance.AwaitSuccessor(context.Background())
	}()
	resolved := <-feed
	if resolved.requestID != first.ID || !resolved.resolved {
		t.Fatalf("first resolution event = %+v, want resolved %q", resolved, first.ID)
	}
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
	requirePromptPending(t, &store, second.ID)
	select {
	case err := <-answerDone:
		t.Fatalf("answer returned before successor was published: %v", err)
	default:
	}
	pending = <-feed
	if pending.requestID != second.ID || pending.resolved {
		t.Fatalf("second prompt event = %+v, want pending %q", pending, second.ID)
	}
	select {
	case err := <-answerDone:
		if err != nil {
			t.Fatalf("successor-aware answer: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("answer did not return after successor became pending")
	}
	submitDone := make(chan error, 1)
	go func() {
		submitDone <- store.Submit(
			tools.AskQuestionResponse{RequestID: second.ID, Answer: "two"},
			nil,
		)
	}()
	resolved = <-feed
	if resolved.requestID != second.ID || !resolved.resolved {
		t.Fatalf("second resolution event = %+v, want resolved %q", resolved, second.ID)
	}
	if err := <-submitDone; err != nil {
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

func TestExecutionPromptStoreClosePublishesLifecycleBeforeReleasingPrompt(t *testing.T) {
	feed := newGatedPromptFeed()
	store := newExecutionPromptStoreForTest(t, feed)
	request := tools.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"}
	awaitDone := make(chan executionPromptResult, 1)
	go func() {
		response, err := store.Await(context.Background(), request)
		awaitDone <- executionPromptResult{response: response, err: err}
	}()
	<-feed.pendingStarted

	closeDone := make(chan struct{})
	go func() {
		store.Close(context.Canceled)
		close(closeDone)
	}()
	select {
	case <-feed.resolutionStarted:
		t.Fatal("prompt resolution started before pending publication completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(feed.allowPending)
	<-feed.pendingPublished
	select {
	case <-feed.resolutionStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for prompt resolution publication")
	}
	select {
	case result := <-awaitDone:
		t.Fatalf("prompt returned before its resolution was published: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(feed.allowResolution)
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out closing prompt store")
	}
	select {
	case result := <-awaitDone:
		if result.err == nil {
			t.Fatalf("closed prompt result = %+v, want cancellation", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for closed prompt")
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
		acceptance, err := store.Accept(
			tools.AskQuestionResponse{RequestID: request.ID, Answer: "one"},
			nil,
		)
		if err != nil {
			answerDone <- err
			return
		}
		answerDone <- acceptance.AwaitSuccessor(context.Background())
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

func TestExecutionPromptStoreAcceptedAnswerRemembersResolvedSuccessor(t *testing.T) {
	store := newExecutionPromptStore(&Authority{}, ExecutionScope{}, nil)
	first := batchedPromptRequest("ask-1", 0)
	second := batchedPromptRequest("ask-2", 1)
	firstResult := make(chan executionPromptResult, 1)
	go func() {
		response, err := store.Await(context.Background(), first)
		firstResult <- executionPromptResult{response: response, err: err}
	}()
	requirePromptPending(t, &store, first.ID)

	acceptance, err := store.Accept(
		tools.AskQuestionResponse{RequestID: first.ID, Answer: "one"},
		nil,
	)
	if err != nil {
		t.Fatalf("accept first prompt response: %v", err)
	}
	select {
	case result := <-firstResult:
		if result.err != nil || result.response.RequestID != first.ID {
			t.Fatalf("first prompt result = %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first prompt response")
	}

	secondResult := make(chan executionPromptResult, 1)
	go func() {
		response, err := store.Await(context.Background(), second)
		secondResult <- executionPromptResult{response: response, err: err}
	}()
	requirePromptPending(t, &store, second.ID)
	if err := store.Submit(
		tools.AskQuestionResponse{RequestID: second.ID, Answer: "two"},
		nil,
	); err != nil {
		t.Fatalf("submit second prompt response: %v", err)
	}
	select {
	case result := <-secondResult:
		if result.err != nil || result.response.RequestID != second.ID {
			t.Fatalf("second prompt result = %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second prompt response")
	}

	for attempt := 1; attempt <= 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := acceptance.AwaitSuccessor(ctx)
		cancel()
		if err != nil {
			t.Fatalf("await resolved successor attempt %d: %v", attempt, err)
		}
	}
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

func newExecutionPromptStoreForTest(t *testing.T, feed ExecutionPromptFeed) executionPromptStore {
	t.Helper()
	sessionID := runtimeids.NewSessionID()
	resource, err := runtimeids.NewSessionResourceRef(sessionID, 1)
	if err != nil {
		t.Fatalf("new session resource: %v", err)
	}
	scope := newAgentExecutionScope(runtimeids.NewExecutionScopeID(), 1, resource, nil)
	return newExecutionPromptStore(&Authority{}, scope, feed)
}

type gatedPromptFeed struct {
	pendingStarted    chan struct{}
	allowPending      chan struct{}
	pendingPublished  chan struct{}
	resolutionStarted chan struct{}
	allowResolution   chan struct{}
}

func newGatedPromptFeed() *gatedPromptFeed {
	return &gatedPromptFeed{
		pendingStarted:    make(chan struct{}),
		allowPending:      make(chan struct{}),
		pendingPublished:  make(chan struct{}),
		resolutionStarted: make(chan struct{}),
		allowResolution:   make(chan struct{}),
	}
}

func (f *gatedPromptFeed) PromptPendingScope(
	_ ExecutionScope,
	_ tools.AskQuestionRequest,
	_ time.Time,
) {
	close(f.pendingStarted)
	<-f.allowPending
	close(f.pendingPublished)
}

func (f *gatedPromptFeed) PromptResolvedScope(
	_ ExecutionScope,
	_ string,
) {
	close(f.resolutionStarted)
	<-f.allowResolution
}
