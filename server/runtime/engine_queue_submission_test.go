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
	"core/shared/runtimeids"
	"core/shared/textutil"
)

func TestAcceptedQueueAutoDrainsAfterActiveTurnWithoutClientKick(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("queued work handled"), Phase: textutil.Value(llm.MessagePhaseFinal)}}}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- engine.stepLifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("active turn did not start")
	}
	queued, accepted, err := engine.QueueUserMessageForActiveRun(context.Background(), "queued input", runtimeids.NewRuntimeClientRequestID(), nil)
	if err != nil || !accepted || queued.ID == "" {
		t.Fatalf("accepted queue item = %+v/%t/%v", queued, accepted, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("active turn completion: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if calls := fakeClientCallCount(client); calls != 1 {
		t.Fatalf("queued work model calls = %d, want 1", calls)
	}
	if engine.HasQueuedUserWork() || engine.HasScheduledQueuedUserWork() {
		t.Fatal("accepted queue remained after the active turn")
	}
}

func TestSubmitQueuedUserMessagesPreservesCommittedFlushReceiptOnRunError(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	providerErr := &llm.ProviderAPIError{
		ProviderID: "test",
		Code:       llm.UnifiedErrorCodeProviderContract,
	}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{errors: []error{providerErr}},
		newTestToolRegistry(t),
		Config{Model: "gpt-5"},
	)
	engine.QueueUserMessage("queued input")

	_, receipt, err := engine.SubmitQueuedUserMessagesWithActiveHook(context.Background(), nil)
	if !receipt.Committed || !errors.Is(err, providerErr) {
		t.Fatalf("queued submission receipt=%+v error=%v, want committed provider failure", receipt, err)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("committed queued input retained retry ownership")
	}
	if userMessages := boundedPersistedUserMessageCount(t, store); userMessages != 1 {
		t.Fatalf("persisted queued user messages = %d, want one committed input", userMessages)
	}
}

func TestSubmitQueuedUserMessagesPreservesCommittedFlushReceiptOnStepFinalizationError(t *testing.T) {
	t.Parallel()
	finalizationErr := errors.New("step finalization failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(gate),
	)
	sink := &finishFailureLifecycleSink{gate: gate, failure: finalizationErr}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("completed"),
			},
		}}},
		newTestToolRegistry(t),
		Config{Model: "gpt-5", StepLifecycle: sink},
	)
	engine.QueueUserMessage("queued input")

	_, receipt, err := engine.SubmitQueuedUserMessagesWithActiveHook(context.Background(), nil)
	if !receipt.Committed ||
		!errors.Is(err, errPendingModelRecoveryClear) ||
		!errors.Is(err, finalizationErr) {
		t.Fatalf("queued submission receipt=%+v error=%v", receipt, err)
	}
	if sink.ended == nil || sink.ended.Status != RunStatusCompleted {
		t.Fatalf("queued step finalization = %+v", sink.ended)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("committed queued input retained retry ownership")
	}
	if userMessages := boundedPersistedUserMessageCount(t, store); userMessages != 1 {
		t.Fatalf("persisted queued user messages = %d, want one committed input", userMessages)
	}
}

func TestDrainQueuedUserMessagesBeforeCloseFailsRestoredQueueWhenFlushPersistenceFails(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var statuses []QueuedUserMessageStatusEvent
	var statusMu sync.Mutex
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.QueuedUserMessageStatus != nil {
				statusMu.Lock()
				defer statusMu.Unlock()
				statuses = append(statuses, *event.QueuedUserMessageStatus)
			}
		},
	})
	if err := engine.ensureMetaContextForRequest(context.Background(), "queue-flush"); err != nil {
		t.Fatalf("prepare queued flush context: %v", err)
	}
	queued := mustQueueUserMessageWithClientRequestID(t, engine, "queued input", "request-id")
	blocker := mustBlockTestEventLogAppends(t, store)

	if err := engine.DrainQueuedUserMessagesBeforeClose(context.Background()); err == nil {
		t.Fatal("close drain did not surface an uncommitted queued flush")
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log appends: %v", err)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("close drain retained uncommitted queued user work")
	}
	statusMu.Lock()
	statusSnapshot := append([]QueuedUserMessageStatusEvent(nil), statuses...)
	statusMu.Unlock()
	if len(statusSnapshot) != 3 {
		t.Fatalf("queued user statuses = %+v", statusSnapshot)
	}
	if accepted := statusSnapshot[0]; accepted.Status != QueuedUserMessageAccepted ||
		accepted.QueueItemID != queued.ID ||
		accepted.ClientRequestID != queued.ClientRequestID {
		t.Fatalf("accepted queue status = %+v", accepted)
	}
	if restored := statusSnapshot[1]; restored.Status != QueuedUserMessageAccepted ||
		restored.QueueItemID != queued.ID ||
		restored.ClientRequestID != queued.ClientRequestID {
		t.Fatalf("restored queue status = %+v", restored)
	}
	if failed := statusSnapshot[2]; failed.Status != QueuedUserMessageFailed ||
		failed.QueueItemID != queued.ID ||
		failed.ClientRequestID != queued.ClientRequestID ||
		failed.FailureReason != QueuedUserMessageFailureClosing {
		t.Fatalf("close-drain queue failure = %+v", failed)
	}
	if userMessages := boundedPersistedUserMessageCount(t, store); userMessages != 0 {
		t.Fatalf("uncommitted queued flush persisted %d user messages", userMessages)
	}
}

func TestDrainQueuedUserMessagesBeforeCloseConsumesCommittedFlushObserverFailure(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("queued flush observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(gate),
	)
	var statuses []QueuedUserMessageStatusEvent
	var statusMu sync.Mutex
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.QueuedUserMessageStatus != nil {
				statusMu.Lock()
				defer statusMu.Unlock()
				statuses = append(statuses, *event.QueuedUserMessageStatus)
			}
		},
	})
	if err := engine.ensureMetaContextForRequest(context.Background(), "queue-flush"); err != nil {
		t.Fatalf("prepare queued flush context: %v", err)
	}
	queued := mustQueueUserMessageWithClientRequestID(t, engine, "queued input", "request-id")
	gate.FailNext(observerErr)

	if err := engine.DrainQueuedUserMessagesBeforeClose(context.Background()); !errors.Is(err, observerErr) {
		t.Fatalf("close drain error = %v", err)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("committed close-drain flush retained queued user work")
	}
	statusMu.Lock()
	statusSnapshot := append([]QueuedUserMessageStatusEvent(nil), statuses...)
	statusMu.Unlock()
	if len(statusSnapshot) != 2 {
		t.Fatalf("queued user statuses = %+v", statusSnapshot)
	}
	if accepted := statusSnapshot[0]; accepted.Status != QueuedUserMessageAccepted ||
		accepted.QueueItemID != queued.ID ||
		accepted.ClientRequestID != queued.ClientRequestID {
		t.Fatalf("accepted queue status = %+v", accepted)
	}
	if submitted := statusSnapshot[1]; submitted.Status != QueuedUserMessageSubmitted ||
		submitted.QueueItemID != queued.ID ||
		submitted.ClientRequestID != queued.ClientRequestID {
		t.Fatalf("submitted queue status = %+v", submitted)
	}
	if userMessages := boundedPersistedUserMessageCount(t, store); userMessages != 1 {
		t.Fatalf("committed close-drain flush persisted %d user messages", userMessages)
	}
}

func TestQueuedFlushStopFailsTheRemainingTypedTail(t *testing.T) {
	store := mustCreateTestSession(t)
	var statuses []QueuedUserMessageStatusEvent
	var statusMu sync.Mutex
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.QueuedUserMessageStatus != nil {
				statusMu.Lock()
				defer statusMu.Unlock()
				statuses = append(statuses, *event.QueuedUserMessageStatus)
			}
		},
	})
	engine.pauseQueuedUserAutoDrain()
	t.Cleanup(engine.resumeQueuedUserAutoDrain)
	engine.liveRun.beginStep(&RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  time.Now().UTC(),
	})
	first, accepted, err := engine.QueueUserMessageForActiveRun(context.Background(), "human", runtimeids.NewRuntimeClientRequestID(), nil)
	if err != nil || !accepted {
		t.Fatalf("queue human = %+v/%t/%v", first, accepted, err)
	}
	steer, err := NewAgentSteer(runtimeids.NewSessionID(), "agent")
	if err != nil {
		t.Fatalf("NewAgentSteer: %v", err)
	}
	second, accepted, err := engine.QueueAgentSteerForActiveRun(context.Background(), steer, runtimeids.NewRuntimeClientRequestID(), nil)
	if err != nil || !accepted {
		t.Fatalf("queue agent = %+v/%t/%v", second, accepted, err)
	}
	engine.liveRun.mu.Lock()
	engine.liveRun.markStoppedQueueItemsLocked(map[runtimeids.QueueItemID]struct{}{mustQueueItemID(second.ID): {}})
	engine.liveRun.mu.Unlock()

	_, err = engine.messageFlow.FlushPendingUserInjections("018fdd67-89ab-4cde-8123-456789abc002", allPendingUserInjectionSelection{})
	if err != nil {
		t.Fatalf("FlushPendingUserInjections: %v", err)
	}
	statusMu.Lock()
	statusSnapshot := append([]QueuedUserMessageStatusEvent(nil), statuses...)
	statusMu.Unlock()
	for _, status := range statusSnapshot {
		if status.QueueItemID == second.ID && status.Status == QueuedUserMessageFailed {
			if len(engine.messageFlow.PendingUserMessages()) != 0 {
				t.Fatal("stopped queue tail remained pending")
			}
			return
		}
	}
	t.Fatalf("statuses = %+v, want failed tail item %q", statusSnapshot, second.ID)
}

func TestQueuedFlushStopAfterCommittedGroupSkipsFollowUpModelAndClearsStoppedIDs(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("must not run")}}}}
	var engine *Engine
	var statuses []QueuedUserMessageStatusEvent
	var statusMu sync.Mutex
	var firstID string
	var tailID string
	engine = mustNewTestEngine(t, store, client, newTestToolRegistry(t), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.QueuedUserMessageStatus == nil {
				return
			}
			status := *event.QueuedUserMessageStatus
			statusMu.Lock()
			statuses = append(statuses, status)
			currentFirstID, currentTailID := firstID, tailID
			statusMu.Unlock()
			if status.Status != QueuedUserMessageSubmitted || status.QueueItemID != currentFirstID || currentTailID == "" {
				return
			}
			engine.liveRun.mu.Lock()
			engine.liveRun.markStoppedQueueItemsLocked(map[runtimeids.QueueItemID]struct{}{mustQueueItemID(currentTailID): {}})
			engine.liveRun.mu.Unlock()
		},
	})
	first, err := engine.QueueUserMessage("human")
	if err != nil {
		t.Fatalf("queue human: %v", err)
	}
	statusMu.Lock()
	firstID = first.ID
	statusMu.Unlock()
	steer, err := NewAgentSteer(runtimeids.NewSessionID(), "agent")
	if err != nil {
		t.Fatalf("NewAgentSteer: %v", err)
	}
	second, err := engine.messageFlow.QueueUserMessageWithID(QueuedUserMessage{
		ID:      runtimeids.NewQueueItemID().String(),
		Message: steer.Message(),
	})
	if err != nil {
		t.Fatalf("queue agent: %v", err)
	}
	statusMu.Lock()
	tailID = second.ID
	statusMu.Unlock()

	_, _, err = engine.SubmitQueuedUserMessagesWithActiveHook(context.Background(), nil)
	if err != nil {
		t.Fatalf("SubmitQueuedUserMessagesWithActiveHook: %v", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("model calls after stopped tail = %d, want zero", len(client.calls))
	}
	if len(engine.messageFlow.PendingUserMessages()) != 0 {
		t.Fatal("stopped tail remained pending")
	}
	engine.liveRun.mu.Lock()
	defer engine.liveRun.mu.Unlock()
	if len(engine.liveRun.stoppedQueueItems) != 0 {
		t.Fatalf("stopped queue IDs = %d, want zero", len(engine.liveRun.stoppedQueueItems))
	}
	_ = first
	_ = second
}

func boundedPersistedUserMessageCount(t *testing.T, store *session.Store) int {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded queued-input records: %v", err)
	}
	userMessages := 0
	for _, record := range window.Records {
		payload, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if !ok {
			continue
		}
		message := llmMessageFromSessionRecord(payload)
		if message.Role == llm.RoleUser {
			userMessages++
		}
	}
	return userMessages
}
