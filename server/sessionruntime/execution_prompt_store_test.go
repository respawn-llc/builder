package sessionruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

type promptAwaitTestResult struct {
	resolution tools.AskQuestionResolution
	err        error
}

func testQuestionResolution(answer string) tools.AskQuestionAnswer {
	return tools.AskQuestionAnswer{
		Freeform: &answer,
	}
}

func requireQuestionAnswer(
	t *testing.T,
	resolution tools.AskQuestionResolution,
	want string,
) {
	t.Helper()
	answer, ok := resolution.(tools.AskQuestionAnswer)
	if !ok || answer.Freeform == nil || *answer.Freeform != want {
		t.Fatalf("Question resolution = %+v, want freeform %q", resolution, want)
	}
}

func resolveAuthorityQuestionForTest(
	authority *Authority,
	sessionID runtimeids.SessionID,
	stepID runtimeids.StepID,
	toolCallID string,
	answer tools.AskQuestionAnswer,
) error {
	_, err := authority.ResolvePromptBatch(context.Background(), sessionID, stepID, []PromptAnswerCommand{{
		ToolCallID: clientui.ToolCallID(toolCallID),
		Payload:    PromptQuestionAnswerCommand{Answer: answer},
	}})
	return err
}

func TestExecutionPromptStoreClosePublishesLifecycleBeforeReleasingPrompt(t *testing.T) {
	feed := newGatedPromptFeed()
	store := newExecutionPromptStoreForTest(t, feed)
	request := tools.AskQuestionRequest{ToolCallID: "ask-1", Question: "Proceed?"}
	awaitDone := make(chan promptAwaitTestResult, 1)
	go func() {
		resolution, err := store.Await(context.Background(), request)
		awaitDone <- promptAwaitTestResult{resolution: resolution, err: err}
	}()
	<-feed.pendingStarted

	closeDone := make(chan struct{})
	go func() {
		_ = store.Close(context.Canceled)
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

func TestExecutionPromptStoreCloseFinalizesEveryApprovalOnce(t *testing.T) {
	feed := make(authorityPromptFeed, 4)
	store := newExecutionPromptStoreForTest(t, feed)
	stepID := promptBatchStepID(t)
	ids := []string{"approval-close-first", "approval-close-second"}
	waiters := []chan error{make(chan error, 1), make(chan error, 1)}
	for index, id := range ids {
		go func() {
			_, err := store.Await(context.Background(), tools.AskQuestionRequest{ToolCallID: id, StepID: stepID.String(), Question: "Allow access?", Approval: true, ApprovalOptions: []tools.AskQuestionApprovalOption{{Decision: tools.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}}})
			waiters[index] <- err
		}()
	}
	for range ids {
		if event := <-feed; event.resolved {
			t.Fatalf("Approval resolved before store close: %+v", event)
		}
	}
	_ = store.Close(context.Canceled)
	want := map[string]bool{ids[0]: true, ids[1]: true}
	first, second := <-feed, <-feed
	if !first.resolved || !second.resolved || first.requestID == second.requestID || !want[first.requestID] || !want[second.requestID] {
		t.Fatalf("Approval resolution publications = (%+v, %+v), want each once", first, second)
	}
	for index, done := range waiters {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Approval %q close error = %v", ids[index], err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("Approval %q remained blocked after close", ids[index])
		}
	}
	results, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{promptApprovalAnswer(ids[0], tools.AskQuestionApprovalDecisionAllowOnce, nil), promptApprovalAnswer(ids[1], tools.AskQuestionApprovalDecisionAllowOnce, nil)})
	if err != nil {
		t.Fatalf("stale Approval submissions: %v", err)
	}
	requirePromptBatchResultSet(t, results, map[clientui.ToolCallID]PromptAnswerOutcome{clientui.ToolCallID(ids[0]): PromptAnswerOutcomeSkipped, clientui.ToolCallID(ids[1]): PromptAnswerOutcomeSkipped})
	select {
	case event := <-feed:
		t.Fatalf("duplicate Approval publication: %+v", event)
	default:
	}
}

type failingPromptFeed struct{ err error }

func (f failingPromptFeed) PromptPendingScope(ExecutionScope, tools.AskQuestionRequest, time.Time) error {
	return f.err
}

func (failingPromptFeed) PromptResolvedScope(ExecutionScope, string) error {
	return nil
}

func TestExecutionPromptStoreRejectsPromptWhenPendingPublicationFails(t *testing.T) {
	store := newExecutionPromptStoreForTest(t, failingPromptFeed{err: errors.New("task wake failed")})
	_, err := store.Await(context.Background(), tools.AskQuestionRequest{ToolCallID: "ask-failed", Question: "Proceed?"})
	if err == nil {
		t.Fatal("prompt succeeded after pending publication failure")
	}
	if store.hasPendingID("ask-failed") {
		t.Fatal("failed prompt remained pending in execution store")
	}
}

func batchedPromptRequest(id string, ordinal int) tools.AskQuestionRequest {
	return tools.AskQuestionRequest{
		ToolCallID: id,
		Question:   id,
		Origin:     tools.AskQuestionOriginModelTool,
		RunID:      "run-1",
		StepID:     "step-1",
		QuestionBatch: &tools.AskQuestionBatchMetadata{
			Origin:              tools.AskQuestionOriginModelTool,
			RunID:               "run-1",
			StepID:              "step-1",
			ToolCallID:          id,
			BatchToolCallIDs:    []string{"ask-1", "ask-2"},
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
) error {
	close(f.pendingStarted)
	<-f.allowPending
	close(f.pendingPublished)
	return nil
}

func (f *gatedPromptFeed) PromptResolvedScope(
	_ ExecutionScope,
	_ string,
) error {
	close(f.resolutionStarted)
	<-f.allowResolution
	return nil
}
