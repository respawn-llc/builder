package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
)

func TestSubmitQueuedUserMessagesStartsTurnFromQueuedInjection(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("after queued steer")},
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

	queued := mustQueueUserMessage(t, eng, "steer now")

	msg, err := eng.SubmitQueuedUserMessages(context.Background())
	if err != nil {
		t.Fatalf("submit queued user messages: %v", err)
	}
	if messageContent(msg) != "after queued steer" {
		t.Fatalf("assistant content = %q, want after queued steer", messageContent(msg))
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
		if message.Role == llm.RoleUser && messageContent(message) == "steer now" {
			hasQueuedUser = true
			break
		}
	}
	if !hasQueuedUser {
		t.Fatalf("expected first request to include queued user message, got %+v", requestMessages(client.calls[0]))
	}
}

func TestSubmitQueuedUserMessagesPreservesCommittedFlushReceiptOnRunError(t *testing.T) {
	store := mustCreateTestSession(t)
	providerErr := &llm.ProviderAPIError{
		ProviderID: "openai",
		Code:       llm.UnifiedErrorCodeProviderContract,
		Message:    "provider down",
	}
	client := &fakeClient{errors: []error{providerErr}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	mustQueueUserMessage(t, eng, "steer now")

	_, receipt, err := eng.SubmitQueuedUserMessagesWithActiveHook(context.Background(), nil)
	if !receipt.Committed || !errors.Is(err, providerErr) {
		t.Fatalf("queued submission receipt=%+v error=%v, want committed provider error", receipt, err)
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("committed queued flush retained retry ownership")
	}
	userEntries := 0
	for _, entry := range eng.ChatSnapshot().Entries {
		if entry.Role == string(llm.RoleUser) && entry.Text == "steer now" {
			userEntries++
		}
	}
	if userEntries != 1 {
		t.Fatalf("projected queued user messages = %d, want 1", userEntries)
	}
}

func TestSubmitQueuedUserMessagesPreservesCommittedFlushReceiptOnStepFinalizationError(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	finalizationErr := errors.New("step finalization failed")
	eng.stepLifecycle = &stubExclusiveStepLifecycle{runFn: func(ctx context.Context, _ exclusiveStepOptions, run func(context.Context, string) error) error {
		if err := run(ctx, "queued-step"); err != nil {
			return err
		}
		return finalizationErr
	}}
	mustQueueUserMessage(t, eng, "steer now")

	_, receipt, err := eng.SubmitQueuedUserMessagesWithActiveHook(context.Background(), nil)
	if !receipt.Committed || !errors.Is(err, finalizationErr) {
		t.Fatalf("queued submission receipt=%+v error=%v, want committed finalization error", receipt, err)
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("committed queued flush retained retry ownership")
	}
}

func TestQueuedUserMessageStatusEventsCoverAcceptedSubmittedAndFailed(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("after queued steer")},
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

	first := mustQueueUserMessageWithClientRequestID(t, eng, "steer now", "req-1")
	if _, err := eng.SubmitQueuedUserMessages(context.Background()); err != nil {
		t.Fatalf("SubmitQueuedUserMessages: %v", err)
	}
	second := mustQueueUserMessageWithClientRequestID(t, eng, "restore me", "req-2")
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

func TestSubmitUserMessageOrSteerSteersWhenBusy(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.stepLifecycle = &stubExclusiveStepLifecycle{busy: true, runFn: func(context.Context, exclusiveStepOptions, func(stepCtx context.Context, stepID string) error) error {
		return ErrAgentBusy
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

func TestDrainQueuedUserMessagesBeforeCloseProcessesQueuedSteeringAfterFinalAnswer(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ack queued steer"), Phase: textutil.Value(llm.MessagePhaseFinal)},
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
	queued := mustQueueUserMessageWithClientRequestID(t, eng, "queued steer", "req-queued")
	if err := eng.DrainQueuedUserMessagesBeforeClose(context.Background()); err != nil {
		t.Fatalf("DrainQueuedUserMessagesBeforeClose: %v", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d, want initial plus queued drain", len(client.calls))
	}
	hasQueuedUser := false
	for _, message := range requestMessages(client.calls[1]) {
		if message.Role == llm.RoleUser && messageContent(message) == "queued steer" {
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
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("unused")},
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
	if err := eng.ensureMetaContextForRequest(context.Background(), "prep"); err != nil {
		t.Fatalf("prepare request context: %v", err)
	}
	queued := mustQueueUserMessageWithClientRequestID(t, eng, "queued steer", "req-queued")
	mustBlockTestEventLogAppends(t, store)

	err := eng.DrainQueuedUserMessagesBeforeClose(context.Background())
	if err == nil {
		t.Fatal("DrainQueuedUserMessagesBeforeClose did not surface the event-log append failure")
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

func TestDrainQueuedUserMessagesBeforeCloseConsumesCommittedFlushObserverFailure(t *testing.T) {
	observerErr := errors.New("queued flush observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	if err := eng.ensureMetaContextForRequest(context.Background(), "prep"); err != nil {
		t.Fatalf("prepare request context: %v", err)
	}
	queued := mustQueueUserMessageWithClientRequestID(t, eng, "queued steer", "req-queued")
	gate.FailNext(observerErr)

	err := eng.DrainQueuedUserMessagesBeforeClose(context.Background())
	if !errors.Is(err, observerErr) {
		t.Fatalf("DrainQueuedUserMessagesBeforeClose error = %v, want observer error", err)
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("committed queued user work retained retry ownership")
	}
	if len(statuses) != 2 || statuses[0].Status != QueuedUserMessageAccepted || statuses[1].Status != QueuedUserMessageSubmitted {
		t.Fatalf("queued statuses = %+v, want accepted then submitted", statuses)
	}
	if statuses[1].QueueItemID != queued.ID || statuses[1].ClientRequestID != "req-queued" {
		t.Fatalf("submitted status lost queue identity: %+v", statuses[1])
	}
	userEntries := 0
	for _, entry := range eng.ChatSnapshot().Entries {
		if entry.Role == string(llm.RoleUser) {
			userEntries++
		}
	}
	if userEntries != 1 {
		t.Fatalf("committed queued flush projected user entries = %d, want 1", userEntries)
	}
}

func TestIdleQueueUserMessageDoesNotAutoSubmit(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("first done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("queued done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	mustQueueUserMessage(t, eng, "queued while idle")
	time.Sleep(50 * time.Millisecond)
	if got := fakeClientCallCount(client); got != 0 {
		t.Fatalf("idle QueueUserMessage auto-submitted; model calls = %d, want 0", got)
	}
}

func TestQueueUserMessageDuringTerminalPublicationAutoDrainsAfterIdlePublication(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("first done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("queued done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	sink := newBlockingStepLifecycleSink()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5", StepLifecycle: sink})

	firstDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "first")
		firstDone <- err
	}()
	select {
	case <-sink.endedStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for terminal publication")
	}

	mustQueueUserMessage(t, eng, "queued during terminal publication")
	close(sink.releaseEnded)
	if err := <-firstDone; err != nil {
		t.Fatalf("first submit: %v", err)
	}
	waitFakeClientCallCount(t, client, 2)
	waitEngineLifecycleTasks(t, eng)
	if eng.HasQueuedUserWork() {
		t.Fatal("queued user work remained after terminal publication auto drain")
	}
}

func TestCanceledManualReservationReleaseRedrivesQueuedUserWork(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)}}}}
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{Model: "gpt-5"})
	reservation := &exclusiveStepReservation{Kind: exclusiveStepReservationManualCompaction}
	if err := eng.stepLifecycle.AcquireReservation(reservation); err != nil {
		t.Fatalf("acquire canceled compaction reservation: %v", err)
	}
	eng.QueueUserMessageForAutoDrain("queued during canceled compaction", "queued-request")
	eng.stepLifecycle.ReleaseReservation(reservation)
	waitFakeClientCallCount(t, client, 1)
	waitEngineLifecycleTasks(t, eng)
}

type blockingThenQueuedClient struct {
	started        chan struct{}
	releaseC       chan struct{}
	secondStarted  chan struct{}
	releaseSecondC chan struct{}
	firstResponse  *llm.Response
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
	firstResponse := llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("initial work handled"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}
	return &blockingThenQueuedClient{
		started:        make(chan struct{}),
		releaseC:       make(chan struct{}),
		secondStarted:  make(chan struct{}),
		releaseSecondC: make(chan struct{}),
		firstResponse:  &firstResponse,
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
		if c.firstResponse != nil {
			return *c.firstResponse, nil
		}
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
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("queued work handled"), Phase: textutil.Value(llm.MessagePhaseFinal)},
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
