package sessionruntime

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestPromptFollowUpSingleOwnerLifecycle(t *testing.T) {
	t.Run("no successor", func(t *testing.T) {
		store, stepID, subscription := newWatchedPrompt(t, []string{"ask-1"})
		resolveWatchedPrompt(t, store, stepID)
		requirePromptFollowUpTerminal(t, store, subscription, serverapi.PromptFollowUpNoPreparedSuccessor)
	})
	t.Run("successor ready", func(t *testing.T) {
		store, stepID, subscription := newWatchedPrompt(t, []string{"ask-1", "ask-2"})
		resolveWatchedPrompt(t, store, stepID)
		request := questionBatchValidationRequest(t)
		request.ID = "ask-2"
		request.QuestionBatch.PromptID = "ask-2"
		request.QuestionBatch.CandidateOrdinal = 1
		done := make(chan struct{})
		go func() {
			_, _ = store.Await(context.Background(), request)
			close(done)
		}()
		requirePromptPending(t, store, "ask-2")
		requirePromptFollowUpTerminal(t, store, subscription, serverapi.PromptFollowUpSuccessorReady)
		if err := store.Close(context.Canceled); err != nil {
			t.Fatalf("Close: %v", err)
		}
		<-done
	})
	t.Run("duplicate rejected", func(t *testing.T) {
		store, stepID, subscription := newWatchedPrompt(t, []string{"ask-1"})
		defer func() { _ = subscription.Close() }()
		if _, err := store.subscribePromptFollowUp(stepID, "ask-1"); err == nil {
			t.Fatal("concurrent duplicate follow-up subscription succeeded")
		}
	})
	t.Run("cancellation cleans state", func(t *testing.T) {
		store, stepID, subscription := newWatchedPrompt(t, []string{"ask-1"})
		if err := subscription.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if len(store.promptFollowUps) != 0 {
			t.Fatalf("canceled subscription retained state: %+v", store.promptFollowUps)
		}
		if _, err := subscription.Next(context.Background()); !errors.Is(err, io.EOF) {
			t.Fatalf("closed subscription Next error = %v, want EOF", err)
		}
		fresh := subscribePromptFollowUpForTest(t, store, stepID, "ask-1")
		_ = fresh.Close()
	})
	t.Run("unknown and resolved keys rejected", func(t *testing.T) {
		store, _ := newPromptBatchStore(t)
		stepID := promptBatchStepID(t)
		if _, err := store.subscribePromptFollowUp(stepID, "unknown"); !errors.Is(err, serverapi.ErrPromptNotFound) {
			t.Fatalf("unknown subscription error = %v", err)
		}
		request := questionBatchValidationRequest(t)
		request.QuestionBatch = nil
		installPromptBatchEntries(&store, promptBatchEntry(request, time.Unix(1, 0)))
		resolveWatchedPrompt(t, &store, stepID)
		if _, err := store.subscribePromptFollowUp(stepID, "ask-1"); !errors.Is(err, serverapi.ErrPromptNotFound) {
			t.Fatalf("resolved subscription error = %v", err)
		}
	})
	t.Run("retirement preserves unread event", func(t *testing.T) {
		store, stepID, subscription := newWatchedPrompt(t, []string{"ask-1", "ask-2"})
		resolveWatchedPrompt(t, store, stepID)
		if err := store.Close(context.Canceled); err != nil {
			t.Fatalf("Close: %v", err)
		}
		requirePromptFollowUpTerminal(t, store, subscription, serverapi.PromptFollowUpExecutionClosed)
	})
}

func newWatchedPrompt(
	t *testing.T,
	promptIDs []string,
) (*executionPromptStore, runtimeids.StepID, serverapi.PromptFollowUpSubscription) {
	t.Helper()
	store, _ := newPromptBatchStore(t)
	stepID := promptBatchStepID(t)
	request := questionBatchValidationRequest(t)
	request.QuestionBatch.BatchPromptIDs = promptIDs
	request.QuestionBatch.PreparedPromptCount = len(promptIDs)
	installPromptBatchEntries(&store, promptBatchEntry(request, time.Unix(1, 0)))
	return &store, stepID, subscribePromptFollowUpForTest(t, &store, stepID, "ask-1")
}

func resolveWatchedPrompt(t *testing.T, store *executionPromptStore, stepID runtimeids.StepID) {
	t.Helper()
	selected := 1
	if _, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptQuestionAnswer("ask-1", &selected, nil),
	}); err != nil {
		t.Fatalf("ResolvePromptBatch: %v", err)
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

func requirePromptFollowUpTerminal(
	t *testing.T,
	store *executionPromptStore,
	subscription serverapi.PromptFollowUpSubscription,
	want serverapi.PromptFollowUpEventKind,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	event, err := subscription.Next(ctx)
	if err != nil || event.Kind != want {
		t.Fatalf("follow-up event = %+v, error %v, want %q", event, err, want)
	}
	if len(store.promptFollowUps) != 0 {
		t.Fatalf("terminal outcome retained state: %+v", store.promptFollowUps)
	}
}
