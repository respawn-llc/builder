package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
)

func TestSubmitQueuedUserMessagesStartsTurnFromQueuedInjection(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "after queued steer"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var flushed Event
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.Kind == EventUserMessageFlushed {
				flushed = evt
			}
		},
	})

	queued := eng.QueueUserMessage("steer now")

	msg, err := eng.SubmitQueuedUserMessages(context.Background())
	if err != nil {
		t.Fatalf("submit queued user messages: %v", err)
	}
	if msg.Content != "after queued steer" {
		t.Fatalf("assistant content = %q, want after queued steer", msg.Content)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected one model call for queued submission, got %d", len(client.calls))
	}
	if flushed.UserMessage != "steer now" {
		t.Fatalf("unexpected flushed user message %q", flushed.UserMessage)
	}
	if len(flushed.UserMessageBatchQueueItemIDs) != 1 || flushed.UserMessageBatchQueueItemIDs[0] != queued.ID {
		t.Fatalf("flushed queue ids = %+v, want [%q]", flushed.UserMessageBatchQueueItemIDs, queued.ID)
	}

	hasQueuedUser := false
	for _, message := range requestMessages(client.calls[0]) {
		if message.Role == llm.RoleUser && message.Content == "steer now" {
			hasQueuedUser = true
			break
		}
	}
	if !hasQueuedUser {
		t.Fatalf("expected first request to include queued user message, got %+v", requestMessages(client.calls[0]))
	}
}

func TestQueuedUserMessageStatusEventsCoverAcceptedSubmittedAndFailed(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "after queued steer"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})

	first := eng.QueueUserMessageWithClientRequestID("steer now", "req-1")
	if _, err := eng.SubmitQueuedUserMessages(context.Background()); err != nil {
		t.Fatalf("SubmitQueuedUserMessages: %v", err)
	}
	second := eng.QueueUserMessageWithClientRequestID("restore me", "req-2")
	failed := eng.FailQueuedUserMessages(QueuedUserMessageFailureClosing)

	if len(failed) != 1 || failed[0].ID != second.ID {
		t.Fatalf("failed queued messages = %+v, want second queue item", failed)
	}
	want := []QueuedUserMessageStatus{
		QueuedUserMessageAccepted,
		QueuedUserMessageSubmitted,
		QueuedUserMessageAccepted,
		QueuedUserMessageFailed,
	}
	if len(statuses) != len(want) {
		t.Fatalf("statuses = %+v, want %d events", statuses, len(want))
	}
	for i, status := range want {
		if statuses[i].Status != status {
			t.Fatalf("status[%d] = %q, want %q in %+v", i, statuses[i].Status, status, statuses)
		}
	}
	if statuses[0].QueueItemID != first.ID || statuses[0].ClientRequestID != "req-1" {
		t.Fatalf("accepted status = %+v, want first id/client request", statuses[0])
	}
	if statuses[1].QueueItemID != first.ID || statuses[1].ClientRequestID != "req-1" {
		t.Fatalf("submitted status = %+v, want first id/client request", statuses[1])
	}
	if statuses[3].QueueItemID != second.ID || statuses[3].ClientRequestID != "req-2" || statuses[3].RestoreText != "restore me" || statuses[3].FailureReason != QueuedUserMessageFailureClosing {
		t.Fatalf("failed status = %+v, want correlated restore", statuses[3])
	}
}

func TestQueuedUserMessagesCoalesceFromStoredSteeringIntents(t *testing.T) {
	pending := []queuedUserSteeringIntent{
		{
			message: QueuedUserMessage{ID: "queue-1", Text: "stale metadata"},
			intent:  steerMessagesWithPersistenceIntent(steeringPriorityUser, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: "intent text"}}),
		},
		{
			message: QueuedUserMessage{ID: "queue-2"},
			intent:  steerMessagesWithPersistenceIntent(steeringPriorityUser, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: "second intent"}}),
		},
	}

	messages := normalizeQueuedUserMessages(pending)
	if len(messages) != 2 || messages[0] != "intent text" || messages[1] != "second intent" {
		t.Fatalf("queued messages = %+v, want stored intent content", messages)
	}
	items := queuedUserMessagesForFlush(pending)
	if len(items) != 2 || items[0].ID != "queue-1" || items[1].ID != "queue-2" {
		t.Fatalf("queued message items = %+v, want ids for non-empty stored intents", items)
	}
}

func TestRunWhenIdleRunsImmediatelyWhenIdle(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	ran := false
	if err := eng.RunWhenIdle(context.Background(), func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("RunWhenIdle: %v", err)
	}
	if !ran {
		t.Fatal("expected fn to run when idle")
	}
}

func TestRunWhenIdleRetriesUntilBetweenSteps(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	attempts := 0
	eng.stepLifecycle = &stubExclusiveStepLifecycle{runFn: func(ctx context.Context, _ exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error {
		attempts++
		if attempts == 1 {
			return errExclusiveStepBusy
		}
		return fn(ctx, "stub-step")
	}}

	ran := false
	if err := eng.RunWhenIdle(context.Background(), func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("RunWhenIdle: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected one busy retry before running, got %d attempts", attempts)
	}
	if !ran {
		t.Fatal("expected fn to run after the busy step yielded")
	}
}

func TestSubmitUserMessageOrSteerRunsWhenIdle(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "answered", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	msg, queued, err := eng.SubmitUserMessageOrSteer(context.Background(), "hello", "req-1")
	if err != nil {
		t.Fatalf("SubmitUserMessageOrSteer: %v", err)
	}
	if queued != nil {
		t.Fatalf("expected idle submit to run, got queued item %+v", queued)
	}
	if msg.Content != "answered" {
		t.Fatalf("assistant content = %q, want answered", msg.Content)
	}
}

func TestSubmitUserMessageOrSteerSteersWhenBusy(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.stepLifecycle = &stubExclusiveStepLifecycle{busy: true, runFn: func(context.Context, exclusiveStepOptions, func(stepCtx context.Context, stepID string) error) error {
		return errExclusiveStepBusy
	}}

	msg, queued, err := eng.SubmitUserMessageOrSteer(context.Background(), "steer me", "req-2")
	if err != nil {
		t.Fatalf("SubmitUserMessageOrSteer busy: %v", err)
	}
	if queued == nil {
		t.Fatalf("expected busy submit to steer, got assistant %+v", msg)
	}
	if queued.Text != "steer me" || queued.ClientRequestID != "req-2" {
		t.Fatalf("queued item = %+v, want steered text/request id", queued)
	}
	if len(client.calls) != 0 {
		t.Fatalf("expected no model call for steered submit, got %d", len(client.calls))
	}
}

func TestSubmitQueuedUserMessagesRetriesTransientBusyErrors(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "after queued steer"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	attempts := 0
	eng.stepLifecycle = &stubExclusiveStepLifecycle{runFn: func(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error {
		attempts++
		if attempts == 1 {
			return errExclusiveStepBusy
		}
		return fn(ctx, "stub-step")
	}}
	eng.QueueUserMessage("steer now")

	msg, err := eng.SubmitQueuedUserMessages(context.Background())
	if err != nil {
		t.Fatalf("submit queued user messages: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected busy retry before success, got %d attempts", attempts)
	}
	if msg.Content != "after queued steer" {
		t.Fatalf("assistant content = %q, want after queued steer", msg.Content)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected one model call after retry, got %d", len(client.calls))
	}
	hasQueuedUser := false
	for _, message := range requestMessages(client.calls[0]) {
		if message.Role == llm.RoleUser && message.Content == "steer now" {
			hasQueuedUser = true
			break
		}
	}
	if !hasQueuedUser {
		t.Fatalf("expected retried request to include queued user message, got %+v", requestMessages(client.calls[0]))
	}
}

func TestDrainQueuedUserMessagesBeforeCloseProcessesQueuedSteeringAfterFinalAnswer(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ack queued steer", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "initial"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	queued := eng.QueueUserMessageWithClientRequestID("queued steer", "req-queued")
	if err := eng.DrainQueuedUserMessagesBeforeClose(context.Background()); err != nil {
		t.Fatalf("DrainQueuedUserMessagesBeforeClose: %v", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d, want initial plus queued drain", len(client.calls))
	}
	hasQueuedUser := false
	for _, message := range requestMessages(client.calls[1]) {
		if message.Role == llm.RoleUser && message.Content == "queued steer" {
			hasQueuedUser = true
			break
		}
	}
	if !hasQueuedUser {
		t.Fatalf("expected drained request to include queued user message, got %+v", requestMessages(client.calls[1]))
	}
	if len(statuses) != 2 || statuses[0].Status != QueuedUserMessageAccepted || statuses[1].Status != QueuedUserMessageSubmitted || statuses[1].QueueItemID != queued.ID || statuses[1].ClientRequestID != "req-queued" {
		t.Fatalf("queued statuses = %+v, want accepted then submitted for %q", statuses, queued.ID)
	}
}

func TestDrainQueuedUserMessagesBeforeCloseFailsRestoredQueueWhenFlushPersistenceFails(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "unused"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	persistErr := errors.New("persist queued flush")
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	eng.beforePersistMessage = func(msg llm.Message) error {
		if msg.Role == llm.RoleUser && msg.Content == "queued steer" {
			return persistErr
		}
		return nil
	}
	queued := eng.QueueUserMessageWithClientRequestID("queued steer", "req-queued")

	err := eng.DrainQueuedUserMessagesBeforeClose(context.Background())
	if !errors.Is(err, persistErr) {
		t.Fatalf("DrainQueuedUserMessagesBeforeClose error = %v, want %v", err, persistErr)
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("queued user work remained after close-drain failure")
	}
	if len(statuses) != 2 || statuses[0].Status != QueuedUserMessageAccepted || statuses[1].Status != QueuedUserMessageFailed {
		t.Fatalf("queued statuses = %+v, want accepted then failed", statuses)
	}
	if statuses[1].QueueItemID != queued.ID || statuses[1].ClientRequestID != "req-queued" || statuses[1].RestoreText != "queued steer" || statuses[1].FailureReason != QueuedUserMessageFailureClosing {
		t.Fatalf("failed status = %+v, want correlated close failure restore", statuses[1])
	}
}

func TestSubmitQueuedUserMessagesStopsRetryingWhenContextIsCanceled(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	attempts := 0
	eng.stepLifecycle = &stubExclusiveStepLifecycle{runFn: func(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error {
		attempts++
		return errExclusiveStepBusy
	}}
	eng.QueueUserMessage("steer now")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msg, err := eng.SubmitQueuedUserMessages(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got msg=%+v err=%v", msg, err)
	}
	if attempts != 1 {
		t.Fatalf("expected one busy attempt before cancellation, got %d", attempts)
	}
}

func TestInterruptedRunWithQueuedUserWorkDrainsAfterRunReleases(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	firstDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "start blocking run")
		firstDone <- err
	}()

	client.waitStarted(t)
	eng.QueueUserMessage("queued while interrupted")
	if err := eng.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	client.release()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocking run error = %v, want context.Canceled", err)
	}

	client.waitCallCount(t, 2)
	hasQueuedUser := false
	for _, message := range requestMessages(client.requestAt(1)) {
		if message.Role == llm.RoleUser && message.Content == "queued while interrupted" {
			hasQueuedUser = true
			break
		}
	}
	if !hasQueuedUser {
		t.Fatalf("second model request did not include queued user work: %+v", requestMessages(client.requestAt(1)))
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("queued user work remained after interrupted run recovery")
	}
	waitEngineLifecycleTasks(t, eng)
}

func TestQueuedUserWorkScheduledWhenQueuedAfterRunBecomesIdle(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	blockingMessages := &blockingQueueMessageLifecycle{
		wrapped:      eng.messageFlow,
		queueEntered: make(chan struct{}),
		releaseQueue: make(chan struct{}),
	}
	eng.messageFlow = blockingMessages

	firstDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "start blocking run")
		firstDone <- err
	}()
	client.waitStarted(t)
	queueDone := make(chan struct{})
	go func() {
		eng.QueueUserMessage("queued after release check")
		close(queueDone)
	}()
	blockingMessages.waitQueueEntered(t)
	if err := eng.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	client.release()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocking run error = %v, want context.Canceled", err)
	}
	blockingMessages.release()
	select {
	case <-queueDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for queued steering call to return")
	}

	client.waitCallCount(t, 2)
	hasQueuedUser := false
	for _, message := range requestMessages(client.requestAt(1)) {
		if message.Role == llm.RoleUser && message.Content == "queued after release check" {
			hasQueuedUser = true
			break
		}
	}
	if !hasQueuedUser {
		t.Fatalf("second model request did not include queued user work: %+v", requestMessages(client.requestAt(1)))
	}
	waitEngineLifecycleTasks(t, eng)
}

type blockingQueueMessageLifecycle struct {
	wrapped      messageLifecycle
	queueEntered chan struct{}
	releaseQueue chan struct{}
	once         sync.Once
}

func (m *blockingQueueMessageLifecycle) RestoreMessages() error {
	return m.wrapped.RestoreMessages()
}

func (m *blockingQueueMessageLifecycle) FlushPendingUserInjections(stepID string, queueItemIDs map[string]struct{}) (int, error) {
	return m.wrapped.FlushPendingUserInjections(stepID, queueItemIDs)
}

func (m *blockingQueueMessageLifecycle) DrainPendingUserInjections() []QueuedUserMessage {
	return m.wrapped.DrainPendingUserInjections()
}

func (m *blockingQueueMessageLifecycle) QueueUserMessage(text string, clientRequestID string) QueuedUserMessage {
	if text != "idle explicit queue" {
		m.once.Do(func() {
			close(m.queueEntered)
			<-m.releaseQueue
		})
	}
	return m.wrapped.QueueUserMessage(text, clientRequestID)
}

func (m *blockingQueueMessageLifecycle) DiscardQueuedUserMessage(queueItemID string) (QueuedUserMessage, bool) {
	return m.wrapped.DiscardQueuedUserMessage(queueItemID)
}

func (m *blockingQueueMessageLifecycle) HasPendingUserInjections() bool {
	return m.wrapped.HasPendingUserInjections()
}

func (m *blockingQueueMessageLifecycle) waitQueueEntered(t *testing.T) {
	t.Helper()
	select {
	case <-m.queueEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for queued steering call to enter")
	}
}

func (m *blockingQueueMessageLifecycle) release() {
	close(m.releaseQueue)
}

func TestIdleQueueUserMessageDoesNotAutoSubmit(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "queued done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	eng.QueueUserMessage("queued while idle")
	time.Sleep(50 * time.Millisecond)
	if got := fakeClientCallCount(client); got != 0 {
		t.Fatalf("idle QueueUserMessage auto-submitted; model calls = %d, want 0", got)
	}
}

func TestDiscardedBusyQueuedUserWorkDoesNotAuthorizeLaterIdleQueue(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	firstDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "start blocking run")
		firstDone <- err
	}()
	client.waitStarted(t)
	queued := eng.QueueUserMessage("discard me")
	if !eng.DiscardQueuedUserMessage(queued.ID) {
		t.Fatal("expected busy queued steering to be discarded")
	}
	if err := eng.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	client.release()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocking run error = %v, want context.Canceled", err)
	}
	waitEngineLifecycleTasks(t, eng)

	eng.QueueUserMessage("idle explicit queue")
	time.Sleep(50 * time.Millisecond)
	if got := client.callCount(); got != 1 {
		t.Fatalf("discarded busy marker authorized idle queue; model calls = %d, want 1", got)
	}
}

func TestEmptyBusyQueuedUserWorkDoesNotAuthorizeLaterIdleQueue(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	firstDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "start blocking run")
		firstDone <- err
	}()
	client.waitStarted(t)
	eng.QueueUserMessage("   ")
	if err := eng.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	client.release()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocking run error = %v, want context.Canceled", err)
	}
	waitEngineLifecycleTasks(t, eng)

	eng.QueueUserMessage("idle explicit queue")
	time.Sleep(50 * time.Millisecond)
	if got := client.callCount(); got != 1 {
		t.Fatalf("empty busy marker authorized idle queue; model calls = %d, want 1", got)
	}
}

func TestAutoDrainDoesNotSubmitLaterIdleQueuedUserWork(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	blockingMessages := &blockingQueueMessageLifecycle{
		wrapped:      eng.messageFlow,
		queueEntered: make(chan struct{}),
		releaseQueue: make(chan struct{}),
	}
	eng.messageFlow = blockingMessages

	firstDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "start blocking run")
		firstDone <- err
	}()
	client.waitStarted(t)
	queueDone := make(chan struct{})
	go func() {
		eng.QueueUserMessage("busy marked queue")
		close(queueDone)
	}()
	blockingMessages.waitQueueEntered(t)
	if err := eng.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	client.release()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocking run error = %v, want context.Canceled", err)
	}
	eng.QueueUserMessage("idle explicit queue")
	blockingMessages.release()
	select {
	case <-queueDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for queued steering call to return")
	}
	client.waitCallCount(t, 2)
	waitEngineLifecycleTasks(t, eng)
	request := requestMessages(client.requestAt(1))
	hasBusyMarked := false
	hasIdleExplicit := false
	for _, message := range request {
		if message.Role != llm.RoleUser {
			continue
		}
		if message.Content == "busy marked queue" {
			hasBusyMarked = true
		}
		if message.Content == "idle explicit queue" {
			hasIdleExplicit = true
		}
	}
	if !hasBusyMarked || hasIdleExplicit {
		t.Fatalf("auto drain request user messages = %+v, want busy marked only", request)
	}
	if !eng.HasQueuedUserWork() {
		t.Fatal("expected idle explicit queue to remain pending")
	}
}

func TestAutoDrainReviewerFollowUpDoesNotSubmitLaterIdleQueuedUserWork(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenQueuedResponseClient(llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "reviewed queued work", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	})
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["Double-check queued work."]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})
	blockingMessages := &blockingQueueMessageLifecycle{
		wrapped:      eng.messageFlow,
		queueEntered: make(chan struct{}),
		releaseQueue: make(chan struct{}),
	}
	eng.messageFlow = blockingMessages

	firstDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "start blocking run")
		firstDone <- err
	}()
	client.waitStarted(t)
	queueDone := make(chan struct{})
	go func() {
		eng.QueueUserMessage("busy marked queue")
		close(queueDone)
	}()
	blockingMessages.waitQueueEntered(t)
	if err := eng.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	client.release()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocking run error = %v, want context.Canceled", err)
	}
	eng.QueueUserMessage("idle explicit queue")
	blockingMessages.release()
	select {
	case <-queueDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for queued steering call to return")
	}
	waitEngineLifecycleTasks(t, eng)

	if got := fakeClientCallCount(reviewerClient); got != 1 {
		t.Fatalf("reviewer calls = %d, want 1", got)
	}
	for idx := 1; idx < client.callCount(); idx++ {
		for _, message := range requestMessages(client.requestAt(idx)) {
			if message.Role == llm.RoleUser && message.Content == "idle explicit queue" {
				t.Fatalf("model request %d included explicit idle queue during auto drain: %+v", idx, requestMessages(client.requestAt(idx)))
			}
		}
	}
	if !eng.HasQueuedUserWork() {
		t.Fatal("expected idle explicit queue to remain pending after reviewed auto drain")
	}
}

func TestAutoDrainLeavesLaterIdleQueuedUserWorkVisibleDuringTurn(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenBlockedQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	blockingMessages := &blockingQueueMessageLifecycle{
		wrapped:      eng.messageFlow,
		queueEntered: make(chan struct{}),
		releaseQueue: make(chan struct{}),
	}
	eng.messageFlow = blockingMessages

	firstDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "start blocking run")
		firstDone <- err
	}()
	client.waitStarted(t)
	queueDone := make(chan struct{})
	go func() {
		eng.QueueUserMessage("busy marked queue")
		close(queueDone)
	}()
	blockingMessages.waitQueueEntered(t)
	if err := eng.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	client.release()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocking run error = %v, want context.Canceled", err)
	}
	explicit := eng.QueueUserMessage("idle explicit queue")
	blockingMessages.release()
	select {
	case <-queueDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for queued steering call to return")
	}
	client.waitSecondStarted(t)
	if !eng.HasQueuedUserWork() {
		t.Fatal("expected idle explicit queue to remain visible during auto drain")
	}
	if !eng.DiscardQueuedUserMessage(explicit.ID) {
		t.Fatal("expected idle explicit queue to remain discardable during auto drain")
	}
	client.releaseSecond()
	waitEngineLifecycleTasks(t, eng)

	request := requestMessages(client.requestAt(1))
	hasBusyMarked := false
	hasIdleExplicit := false
	for _, message := range request {
		if message.Role != llm.RoleUser {
			continue
		}
		if message.Content == "busy marked queue" {
			hasBusyMarked = true
		}
		if message.Content == "idle explicit queue" {
			hasIdleExplicit = true
		}
	}
	if !hasBusyMarked || hasIdleExplicit {
		t.Fatalf("auto drain request user messages = %+v, want busy marked only", request)
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("queued user work remained after discarding explicit queue")
	}
}

type blockingThenQueuedClient struct {
	started        chan struct{}
	releaseC       chan struct{}
	secondStarted  chan struct{}
	releaseSecondC chan struct{}
	queuedResponse llm.Response
	mu             sync.Mutex
	calls          []llm.Request
}

func newBlockingThenQueuedClient() *blockingThenQueuedClient {
	return &blockingThenQueuedClient{
		started:  make(chan struct{}),
		releaseC: make(chan struct{}),
	}
}

func newBlockingThenBlockedQueuedClient() *blockingThenQueuedClient {
	return &blockingThenQueuedClient{
		started:        make(chan struct{}),
		releaseC:       make(chan struct{}),
		secondStarted:  make(chan struct{}),
		releaseSecondC: make(chan struct{}),
	}
}

func newBlockingThenQueuedResponseClient(response llm.Response) *blockingThenQueuedClient {
	return &blockingThenQueuedClient{
		started:        make(chan struct{}),
		releaseC:       make(chan struct{}),
		queuedResponse: response,
	}
}

func (c *blockingThenQueuedClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req)
	call := len(c.calls)
	if call == 1 {
		close(c.started)
	}
	c.mu.Unlock()
	if call == 1 {
		<-c.releaseC
		return llm.Response{}, ctx.Err()
	}
	if call == 2 && c.secondStarted != nil {
		close(c.secondStarted)
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-c.releaseSecondC:
		}
	}
	if c.queuedResponse.Assistant.Role != "" || c.queuedResponse.Assistant.Content != "" || len(c.queuedResponse.Assistant.ToolCalls) > 0 {
		return c.queuedResponse, nil
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "queued work handled", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (c *blockingThenQueuedClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}, nil
}

func (c *blockingThenQueuedClient) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-c.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for blocking model call")
	}
}

func (c *blockingThenQueuedClient) release() {
	close(c.releaseC)
}

func (c *blockingThenQueuedClient) waitSecondStarted(t *testing.T) {
	t.Helper()
	if c.secondStarted == nil {
		t.Fatal("second model call blocking is not configured")
	}
	select {
	case <-c.secondStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second model call")
	}
}

func (c *blockingThenQueuedClient) releaseSecond() {
	close(c.releaseSecondC)
}

func (c *blockingThenQueuedClient) waitCallCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		c.mu.Lock()
		got := len(c.calls)
		c.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("model calls = %d, want at least %d", got, want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (c *blockingThenQueuedClient) requestAt(index int) llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index < 0 || index >= len(c.calls) {
		return llm.Request{}
	}
	return c.calls[index]
}

func (c *blockingThenQueuedClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func waitFakeClientCallCount(t *testing.T, client *fakeClient, want int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		client.mu.Lock()
		got := len(client.calls)
		client.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("model calls = %d, want at least %d", got, want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func fakeClientCallCount(client *fakeClient) int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return len(client.calls)
}

func fakeClientRequestAt(client *fakeClient, index int) llm.Request {
	client.mu.Lock()
	defer client.mu.Unlock()
	if index < 0 || index >= len(client.calls) {
		return llm.Request{}
	}
	return client.calls[index]
}

func waitEngineLifecycleTasks(t *testing.T, eng *Engine) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		eng.lifecycleWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for engine lifecycle tasks")
	}
}

func TestHasQueuedUserWorkDetectsBackgroundNotices(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	steps := &stubExclusiveStepLifecycle{busy: true}
	eng.stepLifecycle = steps
	eng.backgroundFlow = &defaultBackgroundNoticeScheduler{engine: eng, steps: steps}
	if eng.HasQueuedUserWork() {
		t.Fatal("did not expect queued work in fresh engine")
	}

	eng.HandleBackgroundShellUpdate(BackgroundShellEvent{ID: "42", Type: "completed", State: "done"}, true)
	if !eng.HasQueuedUserWork() {
		t.Fatal("expected queued work after background notice was queued")
	}
}
