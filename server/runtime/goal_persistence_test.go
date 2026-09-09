package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
)

func TestGoalPersistenceDoesNotWaitForModelBoundary(t *testing.T) {
	engine := mustNewExecTestEngine(t, mustCreateTestSession(t), &fakeClient{}, Config{Model: "gpt-5"})
	if err := engine.pauseRuntimeOperations(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := engine.SetGoal(ctx, "persist before the next step", session.GoalActorUser)
	if err != nil {
		t.Fatalf("set while model boundary is blocked: %v", err)
	}
	if !result.MetadataReceipt.Committed || result.ID == "" || engine.Goal() == nil || engine.Goal().ID != result.ID {
		t.Fatalf("set did not return its durable goal: %+v", result)
	}
	for _, message := range engine.transcriptRuntimeState().SnapshotMessages() {
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeGoal {
			t.Fatal("goal reminder entered the protected model step")
		}
	}
	if err := engine.drainRuntimeOperations(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGoalNoticeFailureDoesNotUndoCommittedGoalOrBlockQueue(t *testing.T) {
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	if err := engine.pauseRuntimeOperations(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := engine.SetGoal(t.Context(), "keep the committed objective", session.GoalActorUser)
	if err != nil || !result.MetadataReceipt.Committed {
		t.Fatalf("SetGoal = %+v, %v", result, err)
	}
	gate.FailNext(errors.New("goal notice observer failed"))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := engine.drainRuntimeOperations(ctx); err != nil {
		t.Fatalf("notice failure blocked the queue: %v", err)
	}
	if goal := engine.Goal(); goal == nil || goal.ID != result.ID {
		t.Fatalf("notice failure changed committed goal: %+v", goal)
	}
	if snapshot := engine.ChatSnapshot(); snapshot.StreamingError == "" {
		t.Fatal("notice failure was not surfaced")
	}
	count := 0
	for _, message := range engine.transcriptRuntimeState().SnapshotMessages() {
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeGoal {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("goal notices = %d, want one committed notice without replay", count)
	}
}
