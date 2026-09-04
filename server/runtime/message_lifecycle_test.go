package runtime

import (
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/runtimeids"
)

func TestCommitPendingUserSteersKeepsUncommittedTailVisibleAfterCommittedFailure(t *testing.T) {
	observerErr := errors.New("queued steer observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	stepID := runtimeTestStepID("queued-steer-observer-failure")
	restoreStep := setTestActiveStep(engine, stepID)
	t.Cleanup(restoreStep)

	queued := queueMessageLifecycleTestSteers(t, engine, "first", "second", "third")

	gate.FailNext(observerErr)
	result, err := engine.messageFlow.CommitPendingUserInjections(stepID, steerUserInjections())
	if !errors.Is(err, observerErr) || !result.receipt.Committed {
		t.Fatalf("first commit = %+v, %v; want committed observer failure", result, err)
	}
	if result.flushed != 1 {
		t.Fatalf("first commit flushed = %d, want 1 durably committed group", result.flushed)
	}
	pending := engine.messageFlow.PendingUserMessages()
	if len(pending) != 2 || pending[0].ID != queued[1].ID || pending[1].ID != queued[2].ID {
		t.Fatalf("pending tail after committed failure = %+v, want second and third steers", pending)
	}

	result, err = engine.messageFlow.CommitPendingUserInjections(stepID, steerUserInjections())
	if err != nil {
		t.Fatalf("retry pending tail: %v", err)
	}
	if result.flushed != 2 {
		t.Fatalf("retry flushed = %d, want 2 remaining groups", result.flushed)
	}
	if pending := engine.messageFlow.PendingUserMessages(); len(pending) != 0 {
		t.Fatalf("pending after retry = %+v, want empty", pending)
	}
}

func TestCommitPendingUserSteersReportsCommittedProgressBeforeUncommittedTailFailure(t *testing.T) {
	observer := newCallbackPersistenceObserver(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(observer))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	stepID := runtimeTestStepID("queued-steer-partial-commit")
	restoreStep := setTestActiveStep(engine, stepID)
	t.Cleanup(restoreStep)
	queued := queueMessageLifecycleTestSteers(t, engine, "first", "second", "third")

	observer.Arm(func() {
		mustBlockTestEventLogAppends(t, store)
	})
	result, err := engine.messageFlow.CommitPendingUserInjections(stepID, steerUserInjections())
	if err == nil {
		t.Fatal("later queued Steer did not surface the blocked event-log append")
	}
	if !result.receipt.Committed || result.flushed != 1 {
		t.Fatalf("commit result = %+v, want one committed group before the uncommitted failure", result)
	}
	pending := engine.messageFlow.PendingUserMessages()
	if len(pending) != 2 || pending[0].ID != queued[1].ID || pending[1].ID != queued[2].ID {
		t.Fatalf("pending tail after partial commit = %+v, want second and third Steers", pending)
	}
}

func TestPendingSteersRemainProjectedWhileTheirBoundaryCommitIsInFlight(t *testing.T) {
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	stepID := runtimeTestStepID("queued-steer-in-flight")
	restoreStep := setTestActiveStep(engine, stepID)
	t.Cleanup(restoreStep)
	queued := queueMessageLifecycleTestSteers(t, engine, "first", "second", "third")
	entered, release := gate.BlockNext()
	t.Cleanup(release)

	type commitResult struct {
		result userInjectionCommitResult
		err    error
	}
	done := make(chan commitResult, 1)
	go func() {
		result, err := engine.messageFlow.CommitPendingUserInjections(stepID, steerUserInjections())
		done <- commitResult{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for the queued Steer commit")
	}

	pending := pendingWorkTestSnapshot(t, engine)
	if len(pending.Items) != len(queued) {
		t.Fatalf("Pending Work during commit = %+v, want all claimed Steers visible", pending.Items)
	}
	for index, item := range pending.Items {
		if item.ID.String() != queued[index].ID {
			t.Fatalf("Pending Work[%d] = %s, want %s", index, item.ID, queued[index].ID)
		}
	}

	release()
	committed := <-done
	if committed.err != nil || committed.result.flushed != len(queued) {
		t.Fatalf("commit = %+v, %v; want every Steer flushed", committed.result, committed.err)
	}
	if pending := pendingWorkTestSnapshot(t, engine); len(pending.Items) != 0 {
		t.Fatalf("Pending Work after commit = %+v, want empty", pending.Items)
	}
}

func TestAutoDrainContinuesTheUncommittedTailAfterCommittedFailure(t *testing.T) {
	observerErr := errors.New("queued steer observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	client := &fakeClient{responses: []llm.Response{finalTextResponse("done")}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	metaStepID := runtimeTestStepID("queued-steer-meta")
	restoreStep := setTestActiveStep(engine, metaStepID)
	if err := engine.ensureMetaContextForRequest(t.Context(), metaStepID); err != nil {
		restoreStep()
		t.Fatalf("prepare model context: %v", err)
	}
	restoreStep()
	queued := queueMessageLifecycleTestSteers(t, engine, "first", "second", "third")
	baselineSequence := store.Meta().LastSequence
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.LastSequence > baselineSequence
	}, observerErr)

	if !engine.scheduleQueuedUserInjectionsIfIdle() {
		t.Fatal("accepted Steers did not schedule their model work")
	}
	waitEngineLifecycleTasks(t, engine)

	if pending := pendingWorkTestSnapshot(t, engine); len(pending.Items) != 0 {
		t.Fatalf("Pending Work after committed retry = %+v, want empty", pending.Items)
	}
	client.mu.Lock()
	requests := append([]llm.Request(nil), client.calls...)
	client.mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want one after the committed failure", len(requests))
	}
	found := make(map[string]bool, len(queued))
	for _, message := range requestMessages(requests[0]) {
		if message.Role == llm.RoleDeveloper &&
			message.MessageType != nil &&
			*message.MessageType == llm.MessageTypeAgentSteer {
			found[messageContent(message)] = true
		}
	}
	for _, item := range queued {
		if !found[messageContent(item.Message)] {
			t.Fatalf("provider request omitted an accepted Agent Steer: %+v", requestMessages(requests[0]))
		}
	}
}

func queueMessageLifecycleTestSteers(t *testing.T, engine *Engine, texts ...string) []QueuedUserMessage {
	t.Helper()
	queued := make([]QueuedUserMessage, 0, len(texts))
	for _, text := range texts {
		steer, err := NewAgentSteer(runtimeids.NewSessionID(), text)
		if err != nil {
			t.Fatalf("create Agent Steer %q: %v", text, err)
		}
		item, err := engine.messageFlow.QueueUserMessageWithID(
			QueuedUserMessage{
				ID:      runtimeids.NewQueueItemID().String(),
				Message: steer.Message(),
			},
			queuedUserMessageAssociation{steerAdmission: engine.nextPendingWorkSteerAdmission()},
		)
		if err != nil {
			t.Fatalf("queue Agent Steer %q: %v", text, err)
		}
		queued = append(queued, item)
	}
	return queued
}
