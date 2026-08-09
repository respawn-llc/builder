package sessionruntime

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestPromptFollowUpBroadcastsSuccessorReadyToEveryPreRegisteredWatcher(t *testing.T) {
	store, _ := newPromptBatchStore(t)
	stepID := promptBatchStepID(t)
	first := promptBatchEntry(questionBatchValidationRequest(t), time.Unix(1, 0))
	installPromptBatchEntries(&store, first)
	firstWatcher := subscribePromptFollowUpForTest(t, &store, stepID, "ask-1")
	secondWatcher := subscribePromptFollowUpForTest(t, &store, stepID, "ask-1")
	selected := 1

	if _, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptQuestionAnswer("ask-1", &selected, nil),
	}); err != nil {
		t.Fatalf("ResolvePromptBatch: %v", err)
	}
	if len(store.promptFollowUps) != 1 {
		t.Fatal("batch return discarded pending follow-up before successor materialization")
	}

	secondRequest := questionBatchValidationRequest(t)
	secondRequest.ID = "ask-2"
	secondRequest.QuestionBatch.PromptID = "ask-2"
	secondRequest.QuestionBatch.CandidateOrdinal = 1
	secondDone := make(chan promptAwaitTestResult, 1)
	go func() {
		resolution, err := store.Await(context.Background(), secondRequest)
		secondDone <- promptAwaitTestResult{resolution: resolution, err: err}
	}()
	requirePromptPending(t, &store, "ask-2")

	requirePromptFollowUpEvent(t, firstWatcher, serverapi.PromptFollowUpSuccessorReady)
	if err := store.Close(context.Canceled); err != nil {
		t.Fatalf("Close: %v", err)
	}
	requirePromptFollowUpEvent(t, secondWatcher, serverapi.PromptFollowUpSuccessorReady)
	if len(store.promptFollowUps) != 0 {
		t.Fatalf("terminal broadcast retained follow-up tombstone: %+v", store.promptFollowUps)
	}
	<-secondDone
}

func TestPromptFollowUpBroadcastsNoPreparedSuccessor(t *testing.T) {
	store, _ := newPromptBatchStore(t)
	stepID := promptBatchStepID(t)
	request := questionBatchValidationRequest(t)
	request.QuestionBatch.BatchPromptIDs = []string{"ask-1"}
	request.QuestionBatch.PreparedPromptCount = 1
	entry := promptBatchEntry(request, time.Unix(1, 0))
	installPromptBatchEntries(&store, entry)
	watcher := subscribePromptFollowUpForTest(t, &store, stepID, "ask-1")
	selected := 1

	if _, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptQuestionAnswer("ask-1", &selected, nil),
	}); err != nil {
		t.Fatalf("ResolvePromptBatch: %v", err)
	}
	requirePromptFollowUpEvent(t, watcher, serverapi.PromptFollowUpNoPreparedSuccessor)
	if len(store.promptFollowUps) != 0 {
		t.Fatalf("terminal broadcast retained follow-up tombstone: %+v", store.promptFollowUps)
	}
}

func TestPromptFollowUpBroadcastsExecutionClosure(t *testing.T) {
	store, _ := newPromptBatchStore(t)
	stepID := promptBatchStepID(t)
	entry := promptBatchEntry(questionBatchValidationRequest(t), time.Unix(1, 0))
	installPromptBatchEntries(&store, entry)
	watcher := subscribePromptFollowUpForTest(t, &store, stepID, "ask-1")

	if err := store.Close(context.Canceled); err != nil {
		t.Fatalf("Close: %v", err)
	}
	requirePromptFollowUpEvent(t, watcher, serverapi.PromptFollowUpExecutionClosed)
}

func TestCanceledPromptFollowUpWatcherDoesNotDeleteSharedPendingState(t *testing.T) {
	store, _ := newPromptBatchStore(t)
	stepID := promptBatchStepID(t)
	request := questionBatchValidationRequest(t)
	request.QuestionBatch.BatchPromptIDs = []string{"ask-1"}
	request.QuestionBatch.PreparedPromptCount = 1
	entry := promptBatchEntry(request, time.Unix(1, 0))
	installPromptBatchEntries(&store, entry)
	firstWatcher := subscribePromptFollowUpForTest(t, &store, stepID, "ask-1")
	if err := firstWatcher.Close(); err != nil {
		t.Fatalf("close first watcher: %v", err)
	}
	if len(store.promptFollowUps) != 1 {
		t.Fatal("watcher cancellation deleted shared pending follow-up state")
	}
	secondWatcher := subscribePromptFollowUpForTest(t, &store, stepID, "ask-1")
	selected := 1

	if _, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptQuestionAnswer("ask-1", &selected, nil),
	}); err != nil {
		t.Fatalf("ResolvePromptBatch: %v", err)
	}
	requirePromptFollowUpEvent(t, secondWatcher, serverapi.PromptFollowUpNoPreparedSuccessor)
	if _, err := firstWatcher.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("closed watcher Next error = %v, want EOF", err)
	}
}

func TestPromptFollowUpPendingStateDoesNotShadowSamePromptInAnotherStep(t *testing.T) {
	store, _ := newPromptBatchStore(t)
	firstStepID := promptBatchStepID(t)
	firstRequest := questionBatchValidationRequest(t)
	firstRequest.QuestionBatch.BatchPromptIDs = []string{"ask-1", "ask-2"}
	firstRequest.QuestionBatch.PreparedPromptCount = 2
	installPromptBatchEntries(&store, promptBatchEntry(firstRequest, time.Unix(1, 0)))
	firstWatcher := subscribePromptFollowUpForTest(t, &store, firstStepID, "ask-1")
	selected := 1
	if _, err := store.ResolvePromptBatch(context.Background(), firstStepID, []PromptAnswerCommand{
		promptQuestionAnswer("ask-1", &selected, nil),
	}); err != nil {
		t.Fatalf("ResolvePromptBatch first Step: %v", err)
	}

	secondStepID, err := runtimeids.ParseStepID("33333333-3333-4333-8333-333333333333")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	secondRequest := firstRequest
	secondRequest.StepID = secondStepID.String()
	secondRequest.QuestionBatch = nil
	installPromptBatchEntries(&store, promptBatchEntry(secondRequest, time.Unix(2, 0)))
	secondWatcher, err := store.subscribePromptFollowUp(secondStepID, "ask-1")
	if err != nil {
		t.Fatalf("SubscribePromptFollowUp second Step: %v", err)
	}
	if len(store.promptFollowUps) != 2 {
		t.Fatalf("follow-up states = %+v, want one per full Step/prompt key", store.promptFollowUps)
	}

	if _, err := store.ResolvePromptBatch(context.Background(), secondStepID, []PromptAnswerCommand{
		promptQuestionAnswer("ask-1", &selected, nil),
	}); err != nil {
		t.Fatalf("ResolvePromptBatch second Step: %v", err)
	}
	requirePromptFollowUpEvent(t, secondWatcher, serverapi.PromptFollowUpNoPreparedSuccessor)
	if err := store.Close(context.Canceled); err != nil {
		t.Fatalf("Close: %v", err)
	}
	requirePromptFollowUpEvent(t, firstWatcher, serverapi.PromptFollowUpExecutionClosed)
}

func TestPromptFollowUpStateMapUsesFullTypedKey(t *testing.T) {
	mapType := reflect.TypeOf(executionPromptStore{}.promptFollowUps)
	keyType := mapType.Key()
	if keyType != reflect.TypeOf(promptFollowUpKey{}) {
		t.Fatalf("follow-up map key = %v, want promptFollowUpKey", keyType)
	}
	fields := make([]string, 0, keyType.NumField())
	for index := 0; index < keyType.NumField(); index++ {
		fields = append(fields, keyType.Field(index).Name)
	}
	if !reflect.DeepEqual(fields, []string{"sessionID", "stepID", "promptID"}) {
		t.Fatalf("follow-up key fields = %v, want full prompt identity", fields)
	}
}

func TestPromptFollowUpRejectsUnknownFullKeyDuringSubscriptionSetup(t *testing.T) {
	store, _ := newPromptBatchStore(t)
	if _, err := store.subscribePromptFollowUp(
		promptBatchStepID(t),
		clientui.PromptID("unknown"),
	); !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("SubscribePromptFollowUp error = %v, want unknown prompt", err)
	}
}

func subscribePromptFollowUpForTest(
	t *testing.T,
	store *executionPromptStore,
	stepID runtimeids.StepID,
	promptID clientui.PromptID,
) serverapi.PromptFollowUpSubscription {
	t.Helper()
	subscription, err := store.subscribePromptFollowUp(stepID, promptID)
	if err != nil {
		t.Fatalf("SubscribePromptFollowUp: %v", err)
	}
	return subscription
}

func requirePromptFollowUpEvent(
	t *testing.T,
	subscription serverapi.PromptFollowUpSubscription,
	want serverapi.PromptFollowUpEventKind,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	event, err := subscription.Next(ctx)
	if err != nil {
		t.Fatalf("follow-up event: %v", err)
	}
	if event.Kind != want {
		t.Fatalf("follow-up event = %q, want %q", event.Kind, want)
	}
}
