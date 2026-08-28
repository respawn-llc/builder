package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

func TestPendingWorkProjectsAcceptedMessageAndCompactionOrder(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)

	firstSteer := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "first steer")
	})
	guidance := "keep details"
	admission := runtimeinput.ManualCompactionAdmission{
		Guidance:         &guidance,
		RestorationInput: "/compact   keep details",
	}
	if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		context.Background(),
		runtimeids.NewCompactionRequestID(),
		admission,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	secondSteer := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "second steer")
	})
	queued := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessage(context.Background(), "post-turn queue")
	})

	snapshot := pendingWorkTestSnapshot(t, engine)
	if len(snapshot.Items) != 4 {
		t.Fatalf("Pending Work = %+v", snapshot.Items)
	}
	if snapshot.Items[0].ID.String() != firstSteer.ID ||
		snapshot.Items[1].Kind != runtimeinput.PendingWorkItemKindManualCompaction ||
		snapshot.Items[2].ID.String() != secondSteer.ID ||
		snapshot.Items[3].ID.String() != queued.ID {
		t.Fatalf("Pending Work order = %+v", snapshot.Items)
	}
	if snapshot.Items[1].ManualCompaction == nil ||
		snapshot.Items[1].ManualCompaction.Guidance == nil ||
		*snapshot.Items[1].ManualCompaction.Guidance != guidance ||
		snapshot.Items[1].ManualCompaction.RestorationInput != admission.RestorationInput {
		t.Fatalf("manual compaction = %+v", snapshot.Items[1])
	}
	if snapshot.Items[3].Lane != runtimeinput.PendingWorkLaneQueue {
		t.Fatalf("post-turn item lane = %q", snapshot.Items[3].Lane)
	}

	releaseMaintenance()
}

func TestPendingWorkCapacityRejectsWithoutMutation(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	for index := range runtimeinput.PendingWorkCapacity {
		if _, err := engine.messageFlow.QueueUserMessage(fmt.Sprintf("pending %d", index)); err != nil {
			t.Fatal(err)
		}
	}
	before := pendingWorkTestSnapshot(t, engine)

	_, err := engine.QueueUserMessage(context.Background(), "rejected")
	var typed *serverapi.PendingWorkCapacityError
	if !errors.Is(err, runtimeinput.ErrPendingWorkCapacity) || !errors.As(err, &typed) {
		t.Fatalf("capacity error = %T %v", err, err)
	}
	after := pendingWorkTestSnapshot(t, engine)
	if len(after.Items) != len(before.Items) {
		t.Fatalf("Pending Work changed from %d to %d", len(before.Items), len(after.Items))
	}
	for index := range before.Items {
		if after.Items[index].ID != before.Items[index].ID {
			t.Fatalf("item %d changed from %s to %s", index, before.Items[index].ID, after.Items[index].ID)
		}
	}
}

func TestRemovePendingWorkRestoresTypedMessageAndCompactionInput(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)

	message := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "restore message")
	})
	messageID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
		return runtimeids.ParseQueueItemID(message.ID)
	})
	restoration, err := engine.RemovePendingWork(context.Background(), messageID)
	if err != nil || restoration.Message == nil || restoration.Message.Text != "restore message" {
		t.Fatalf("message removal = %+v/%v", restoration, err)
	}

	guidance := "tighten  spacing"
	admission := runtimeinput.ManualCompactionAdmission{
		Guidance:         &guidance,
		RestorationInput: "/compact   tighten  spacing",
	}
	if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		context.Background(),
		runtimeids.NewCompactionRequestID(),
		admission,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	var compaction runtimeinput.PendingWorkItem
	for _, item := range pendingWorkTestSnapshot(t, engine).Items {
		if item.Kind == runtimeinput.PendingWorkItemKindManualCompaction {
			compaction = item
		}
	}
	if compaction.ID.IsZero() {
		t.Fatal("manual compaction is absent from Pending Work")
	}
	hydrated := hydrationSnapshot(t, engine).PendingWork
	if len(hydrated.Items) != 1 || hydrated.Items[0].ID != compaction.ID {
		t.Fatalf("hydrated Pending Work = %+v", hydrated.Items)
	}
	restoration, err = engine.RemovePendingWork(context.Background(), compaction.ID)
	if err != nil || restoration.ManualCompaction == nil ||
		restoration.ManualCompaction.Input != admission.RestorationInput {
		t.Fatalf("compaction removal = %+v/%v", restoration, err)
	}
	if _, err := engine.RemovePendingWork(context.Background(), compaction.ID); !errors.Is(err, runtimeinput.ErrPendingWorkNotPending) {
		t.Fatalf("repeated removal = %v", err)
	}

	releaseMaintenance()
}

func TestStoppedHumanInputPublishesCapturedPendingWorkReplacement(t *testing.T) {
	var interruption *HumanInputInterruptedEvent
	var replacement *runtimeinput.PendingWork
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
	first := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "stopped")
	})
	second := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "retained")
	})
	engine.cfg.OnEvent = func(event Event) {
		switch event.Kind {
		case EventHumanInputInterrupted:
			interruption = event.HumanInputInterrupted
			if _, err := engine.messageFlow.QueueUserMessage("admitted by interruption observer"); err != nil {
				t.Fatal(err)
			}
		case EventPendingWorkReplaced:
			replacement = event.PendingWork
		}
	}

	engine.failStoppedLiveRunQueueItems(map[runtimeids.QueueItemID]struct{}{
		pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
			return runtimeids.ParseQueueItemID(first.ID)
		}): {},
	})

	if interruption == nil || len(interruption.Items) != 1 || interruption.Items[0].QueueItemID != first.ID {
		t.Fatalf("interruption = %+v", interruption)
	}
	if replacement == nil || len(replacement.Items) != 1 || replacement.Items[0].ID.String() != second.ID {
		t.Fatalf("captured replacement = %+v", replacement)
	}
	current := pendingWorkTestSnapshot(t, engine)
	if len(current.Items) != 2 {
		t.Fatalf("current Pending Work = %+v", current.Items)
	}

	releaseMaintenance()
}

func pendingWorkTestEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	return mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), cfg)
}

func pendingWorkTestHoldMaintenance(t *testing.T, engine *Engine) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- engine.stepLifecycle.Run(
			context.Background(),
			exclusiveStepOptions{ActiveKind: ActiveKindRuntimeMaintenance},
			func(context.Context, string) error {
				close(started)
				<-release
				return nil
			},
		)
	}()
	pendingWorkTestWait(t, started, "Runtime maintenance")
	return func() {
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func pendingWorkTestSnapshot(t *testing.T, engine *Engine) runtimeinput.PendingWork {
	t.Helper()
	snapshot := pendingWorkTestMust(t, engine.PendingWorkSnapshot)
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func pendingWorkTestMust[T any](t *testing.T, operation func() (T, error)) T {
	t.Helper()
	value, err := operation()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func pendingWorkTestWait(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatalf("%s did not complete", name)
	}
}
