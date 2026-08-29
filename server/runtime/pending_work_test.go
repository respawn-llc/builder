package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type worktreeTechnicalTestError struct{ error }
type worktreeIndeterminateTestError struct{ error }
type worktreeAppliedTestError struct{ error }

func (worktreeTechnicalTestError) WorktreeTechnicalFailure()            {}
func (worktreeIndeterminateTestError) WorktreeTransitionIndeterminate() {}
func (worktreeAppliedTestError) WorktreeTransitionApplied()             {}

func TestPendingWorkProjectsAcceptedMessageAndCompactionOrder(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)

	firstSteer := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "first steer")
	})
	guidance := "keep details"
	admission := runtimeinput.ManualCompactionAdmission{
		Guidance: &guidance,
	}
	requestID := runtimeids.NewCompactionRequestID()
	if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		context.Background(),
		requestID,
		admission,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	selector := "feature/pending-work"
	operationID := clientui.NewWorktreeTransitionID()
	worktree := runtimeinput.PendingWorkWorktreeTransition{
		Transition: runtimeinput.PendingWorkWorktreeTransitionEnter, Selector: &selector}
	if _, err := engine.ScheduleWorktreeTransition(context.Background(), operationID, worktree, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	secondSteer := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "second steer")
	})
	queued := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessage(context.Background(), "post-turn queue")
	})

	snapshot := pendingWorkTestSnapshot(t, engine)
	if len(snapshot.Items) != 5 {
		t.Fatalf("Pending Work = %+v", snapshot.Items)
	}
	if snapshot.Items[0].ID.String() != queued.ID ||
		snapshot.Items[1].ID.String() != firstSteer.ID ||
		snapshot.Items[2].ID.String() != requestID.String() ||
		snapshot.Items[3].ID.String() != operationID.String() ||
		snapshot.Items[4].ID.String() != secondSteer.ID {
		t.Fatalf("Pending Work order = %+v", snapshot.Items)
	}
	if snapshot.Items[2].CanonicalInput != "/compact keep details" ||
		snapshot.Items[3].CanonicalInput != "/wt switch feature/pending-work" {
		t.Fatalf("Pending Work projection = %+v", snapshot.Items)
	}

	releaseMaintenance()
}

func TestPendingWorkCapacityRejectsWithoutMutation(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
	for index := range runtimeinput.PendingWorkCapacity {
		if _, err := engine.messageFlow.QueueUserMessage(fmt.Sprintf("pending %d", index),
			queuedUserMessageAssociation{lane: runtimeinput.PendingWorkLaneQueue}); err != nil {
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
	_, err = engine.RemovePendingWork(t.Context(), before.Items[0].ID)
	pendingWorkTestNoError(t, err)
	leave := runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave}
	run := func(context.Context) error { return nil }
	admittedID := clientui.NewWorktreeTransitionID()
	_, err = engine.ScheduleWorktreeTransition(t.Context(), admittedID, leave, run)
	pendingWorkTestNoError(t, err)
	_, err = engine.RemovePendingWork(t.Context(), pendingWorkTestID(t, admittedID.String()))
	pendingWorkTestNoError(t, err)
	reached, unblock, results := make(chan struct{}, 2), make(chan struct{}), make(chan error, 2)
	accept := CommandAcceptance(func(commit func() (bool, error)) (bool, error) { reached <- struct{}{}; <-unblock; return commit() })
	for range 2 {
		go func() {
			_, err := engine.ScheduleWorktreeTransitionWithAcceptance(
				t.Context(), clientui.NewWorktreeTransitionID(), leave, accept, run)
			results <- err
		}()
	}
	pendingWorkTestWait(t, reached, "first acceptance")
	pendingWorkTestWait(t, reached, "second acceptance")
	close(unblock)
	pendingWorkTestNoError(t, <-results)
	pendingWorkTestNoError(t, <-results)
	pendingWorkTestRequire(t, len(pendingWorkTestSnapshot(t, engine).Items) == runtimeinput.PendingWorkCapacity+1,
		"concurrent Pending Work = %+v", pendingWorkTestSnapshot(t, engine).Items)
	releaseMaintenance()
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
	if err != nil || restoration.Kind != runtimeinput.PendingWorkItemKindMessage ||
		restoration.CanonicalInput != "restore message" {
		t.Fatalf("message removal = %+v/%v", restoration, err)
	}

	guidance := "tighten spacing"
	admission := runtimeinput.ManualCompactionAdmission{
		Guidance: &guidance,
	}
	requestID := runtimeids.NewCompactionRequestID()
	if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		context.Background(),
		requestID,
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
	restoration, err = engine.RemovePendingWork(context.Background(), compaction.ID)
	if err != nil || restoration.Kind != runtimeinput.PendingWorkItemKindManualCompaction ||
		restoration.CanonicalInput != "/compact tighten spacing" {
		t.Fatalf("compaction removal = %+v/%v", restoration, err)
	}
	if _, err := engine.RemovePendingWork(context.Background(), compaction.ID); !errors.Is(err, runtimeinput.ErrPendingWorkNotPending) {
		t.Fatalf("repeated removal = %v", err)
	}

	releaseMaintenance()
}

func TestStoppedHumanInputPublishesPendingWorkChangedWithoutBlockingList(t *testing.T) {
	var interruption *HumanInputInterruptedEvent
	changed, unblock, delivered := make(chan struct{}), make(chan struct{}), make(chan struct{})
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
		case EventPendingWorkChanged:
			close(changed)
			<-unblock
			close(delivered)
		}
	}

	go engine.failStoppedLiveRunQueueItems(map[runtimeids.QueueItemID]struct{}{
		pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
			return runtimeids.ParseQueueItemID(first.ID)
		}): {},
	})
	pendingWorkTestWait(t, changed, "Pending Work Changed")

	if interruption == nil || len(interruption.Items) != 1 || interruption.Items[0].QueueItemID != first.ID {
		t.Fatalf("interruption = %+v", interruption)
	}
	current := pendingWorkTestSnapshot(t, engine)
	if len(current.Items) != 1 || current.Items[0].ID.String() != second.ID {
		t.Fatalf("current Pending Work = %+v", current.Items)
	}
	close(unblock)
	pendingWorkTestWait(t, delivered, "Pending Work Changed delivery")

	releaseMaintenance()
}

func TestPendingOperationalWorkTechnicalRestoration(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{worktreeTechnicalTestError{errors.New("technical application failure")}, "/wt leave"},
		{errors.New("selector not found"), ""},
		{worktreeAppliedTestError{errors.New("later publication failure")}, ""},
	}
	for _, test := range tests {
		var restored *runtimeinput.PendingWorkTechnicalRestoration
		var engine *Engine
		id := clientui.NewWorktreeTransitionID()
		engine = pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: func(event Event) {
			if event.Kind == EventPendingWorkRestored {
				restored = event.PendingWorkRestoration
			}
		}})
		_, err := engine.ScheduleWorktreeTransition(t.Context(), id,
			runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave},
			func(context.Context) error {
				_, removeErr := engine.RemovePendingWork(t.Context(), pendingWorkTestID(t, id.String()))
				pendingWorkTestRequire(t, errors.Is(removeErr, runtimeinput.ErrPendingWorkNotPending), "after-start removal = %v", removeErr)
				return test.err
			})
		pendingWorkTestNoError(t, err)
		waitEngineLifecycleTasks(t, engine)
		if (restored == nil) != (test.want == "") || restored != nil && restored.CanonicalInput != test.want {
			t.Fatalf("restoration = %+v, want %q", restored, test.want)
		}
	}
}

func TestIndeterminateWorktreeFailureRetiresRuntimeAfterLifecycleRelease(t *testing.T) {
	transitionFailure, retirementFailure := errors.New("rollback target is indeterminate"), errors.New("retire exact Runtime resource")
	retired := make(chan struct{})
	var queuedID string
	var queuedFailed bool
	var engine *Engine
	engine = pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: func(event Event) {
		if status := event.QueuedUserMessageStatus; status != nil && status.QueueItemID == queuedID &&
			status.Status == QueuedUserMessageFailed && status.FailureReason == QueuedUserMessageFailureRuntimeUnavailable {
			queuedFailed = true
		}
	}, LifecycleRuntimeAbort: func() error { engine.lifecycleWG.Wait(); close(retired); return retirementFailure }})
	release := pendingWorkTestHoldMaintenance(t, engine)
	_, err := engine.ScheduleWorktreeTransition(t.Context(), clientui.NewWorktreeTransitionID(),
		runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave},
		func(context.Context) error { return worktreeIndeterminateTestError{transitionFailure} })
	pendingWorkTestNoError(t, err)
	queued, err := engine.QueueUserMessage(t.Context(), "queued after Worktree transition")
	pendingWorkTestNoError(t, err)
	queuedID = queued.ID
	release()
	pendingWorkTestWait(t, retired, "Runtime retirement")
	waitEngineLifecycleTasks(t, engine)
	_, err = engine.QueueUserMessage(t.Context(), "later human work")
	diagnostic := engine.ChatSnapshot().StreamingError
	pendingWorkTestRequire(t, errors.Is(err, ErrEngineClosed) && queuedFailed &&
		strings.Contains(diagnostic, transitionFailure.Error()) && strings.Contains(diagnostic, retirementFailure.Error()),
		"closure/failure/diagnostic = %v/%v/%q", err, queuedFailed, diagnostic)
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

func pendingWorkTestID(t *testing.T, value string) runtimeids.QueueItemID {
	t.Helper()
	id, err := runtimeids.ParseQueueItemID(value)
	pendingWorkTestNoError(t, err)
	return id
}

func pendingWorkTestContains(pending runtimeinput.PendingWork, id runtimeids.QueueItemID) bool {
	for _, item := range pending.Items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func pendingWorkTestMust[T any](t *testing.T, operation func() (T, error)) T {
	t.Helper()
	value, err := operation()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func pendingWorkTestNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func pendingWorkTestRequire(t *testing.T, condition bool, format string, args ...any) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

func pendingWorkTestWait(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	pendingWorkTestWaitValue(t, signal, name)
}

func pendingWorkTestWaitValue[T any](t *testing.T, signal <-chan T, name string) T {
	t.Helper()
	select {
	case value := <-signal:
		return value
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatalf("%s did not complete", name)
		var zero T
		return zero
	}
}
