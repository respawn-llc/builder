package runtime

import (
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/runtimeids"
)

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
				ID:                    runtimeids.NewQueueItemID().String(),
				Message:               steer.Message(),
				CanonicalPresentation: text,
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
