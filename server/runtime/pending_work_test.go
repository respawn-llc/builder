package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/textutil"
)

type (
	worktreeTechnicalTestError     struct{ error }
	worktreeIndeterminateTestError struct{ error }
	worktreeAppliedTestError       struct{ error }
)

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
	admission := runtimeinput.ManualCompactionAdmission{Guidance: &guidance}
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
	ack, err := engine.ScheduleWorktreeTransition(context.Background(), operationID, runtimeinput.PendingWorkWorktreeTransition{
		Transition: runtimeinput.PendingWorkWorktreeTransitionEnter, Selector: &selector,
	}, func(context.Context) error { return nil })
	pendingWorkTestRequire(t, err == nil && ack.OperationId == operationID.String(), "schedule Worktree transition = %+v/%v", ack, err)
	secondSteer := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "second steer")
	})
	queued := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessage(context.Background(), "post-turn queue")
	})
	items := pendingWorkTestSnapshot(t, engine).Items
	pendingWorkTestRequire(t, len(items) == 5 && items[0].ID.String() == queued.ID &&
		items[1].ID.String() == firstSteer.ID && items[2].ID.String() == requestID.String() &&
		items[3].ID.String() == operationID.String() && items[4].ID.String() == secondSteer.ID,
		"Pending Work order = %+v", items)
	pendingWorkTestRequire(t, items[0].Lane == runtimeinput.PendingWorkLaneQueue &&
		items[2].CanonicalInput == "/compact keep details" &&
		items[3].CanonicalInput == "/wt switch feature/pending-work", "Pending Work projection = %+v", items)

	var changes atomic.Int32
	var restored atomic.Bool
	engine.cfg.OnEvent = func(event Event) {
		if event.Kind == EventPendingWorkChanged {
			changes.Add(1)
		}
		if event.Kind == EventPendingWorkRestored {
			restored.Store(true)
		}
	}
	for _, test := range []struct {
		id        string
		kind      runtimeinput.PendingWorkItemKind
		canonical string
	}{
		{requestID.String(), runtimeinput.PendingWorkItemKindManualCompaction, "/compact keep details"},
		{operationID.String(), runtimeinput.PendingWorkItemKindWorktreeTransition, "/wt switch feature/pending-work"},
	} {
		id := pendingWorkTestID(t, test.id)
		before := changes.Load()
		got, err := engine.RemovePendingWork(t.Context(), id)
		pendingWorkTestRequire(t, err == nil && got.Kind == test.kind && got.CanonicalInput == test.canonical &&
			changes.Load() > before && !restored.Load(), "remove Pending Work = %+v/%v", got, err)
	}
	changedDelivery, unblockDelivery, delivered := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	engine.cfg.OnEvent = func(event Event) {
		if event.Kind == EventPendingWorkChanged {
			close(changedDelivery)
			<-unblockDelivery
		}
	}
	go func() { _, err := engine.QueueUserMessage(t.Context(), "readable"); delivered <- err }()
	pendingWorkTestWait(t, changedDelivery, "Pending Work Changed")
	pendingWorkTestRequire(t, pendingWorkTestSnapshot(t, engine).Items[1].CanonicalInput == "readable",
		"Pending Work list blocked on Changed delivery")
	close(unblockDelivery)
	pendingWorkTestNoError(t, <-delivered)
	engine.cfg.OnEvent = nil
	for range runtimeinput.PendingWorkCapacity - 5 {
		_, err := engine.QueueUserMessage(t.Context(), "capacity")
		pendingWorkTestNoError(t, err)
	}
	leave := runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave}
	run := func(context.Context) error { return nil }
	admittedID := clientui.NewWorktreeTransitionID()
	_, err = engine.ScheduleWorktreeTransition(t.Context(), admittedID, leave, run)
	pendingWorkTestNoError(t, err)
	pendingWorkTestRequire(t, len(pendingWorkTestSnapshot(t, engine).Items) == runtimeinput.PendingWorkCapacity,
		"admitted Pending Work = %+v", pendingWorkTestSnapshot(t, engine).Items)
	_, err = engine.ScheduleWorktreeTransition(t.Context(), clientui.NewWorktreeTransitionID(), leave, run)
	pendingWorkTestRequire(t, errors.Is(err, runtimeinput.ErrPendingWorkCapacity), "capacity rejection = %v", err)
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

	for _, item := range pendingWorkTestSnapshot(t, engine).Items {
		_, err := engine.RemovePendingWork(t.Context(), item.ID)
		pendingWorkTestNoError(t, err)
	}
	releaseMaintenance()
	waitEngineLifecycleTasks(t, engine)
}

func TestPendingOperationalWorkTechnicalRestoration(t *testing.T) {
	failure := errors.New("technical application failure")
	tests := []struct {
		name      string
		kind      runtimeinput.PendingWorkItemKind
		runErr    error
		wantInput string
	}{
		{"Worktree definitely unapplied", runtimeinput.PendingWorkItemKindWorktreeTransition, worktreeTechnicalTestError{failure}, "/wt leave"},
		{"compaction definitely unapplied", runtimeinput.PendingWorkItemKindManualCompaction, failure, "/compact preserve facts"},
		{"user-correctable", runtimeinput.PendingWorkItemKindWorktreeTransition, errors.New("selector not found"), ""},
		{"applied later publication", runtimeinput.PendingWorkItemKindWorktreeTransition, worktreeAppliedTestError{failure}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			restorations := pendingWorkTestRunFailure(t, test.kind, test.runErr)
			if test.wantInput == "" {
				pendingWorkTestRequire(t, len(restorations) == 0, "technical restorations = %+v, want none", restorations)
			} else if len(restorations) != 1 || restorations[0].Kind != test.kind || restorations[0].CanonicalInput != test.wantInput {
				t.Fatalf("technical restorations = %+v, want %s/%q", restorations, test.kind, test.wantInput)
			}
		})
	}
}

func TestIndeterminateWorktreeFailureRetiresRuntimeAfterLifecycleRelease(t *testing.T) {
	transitionFailure := errors.New("rollback target is indeterminate")
	retirementFailure := errors.New("retire exact Runtime resource")
	retired := make(chan struct{})
	var queuedID string
	var queuedFailed, restored atomic.Bool
	var engine *Engine
	engine = pendingWorkTestEngine(t, Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if status := event.QueuedUserMessageStatus; status != nil &&
				status.QueueItemID == queuedID && status.Status == QueuedUserMessageFailed &&
				status.FailureReason == QueuedUserMessageFailureRuntimeUnavailable {
				queuedFailed.Store(true)
			}
			if event.Kind == EventPendingWorkRestored {
				restored.Store(true)
			}
		},
		LifecycleRuntimeAbort: func() error {
			engine.lifecycleWG.Wait()
			close(retired)
			return retirementFailure
		},
	})
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
	pendingWorkTestRequire(t, errors.Is(err, ErrEngineClosed), "later human work error = %v", err)
	pendingWorkTestRequire(t, queuedFailed.Load() && !restored.Load(), "queued failure/restoration = %v/%v", queuedFailed.Load(), restored.Load())
	diagnostic := engine.ChatSnapshot().StreamingError
	pendingWorkTestRequire(t, strings.Contains(diagnostic, transitionFailure.Error()) &&
		strings.Contains(diagnostic, retirementFailure.Error()), "retirement diagnostic = %q", diagnostic)
}

func pendingWorkTestRunFailure(t *testing.T, kind runtimeinput.PendingWorkItemKind, runErr error) []runtimeinput.PendingWorkTechnicalRestoration {
	t.Helper()
	var restorations []runtimeinput.PendingWorkTechnicalRestoration
	var changes atomic.Int32
	startedRemoval := make(chan error, 1)
	observe := func(event Event) {
		if event.Kind == EventPendingWorkChanged {
			changes.Add(1)
		}
		if event.Kind == EventPendingWorkRestored {
			restorations = append(restorations, *event.PendingWorkRestoration)
		}
	}
	var engine *Engine
	var itemID runtimeids.QueueItemID
	if kind == runtimeinput.PendingWorkItemKindManualCompaction {
		requestID := runtimeids.NewCompactionRequestID()
		itemID = pendingWorkTestID(t, requestID.String())
		engine = mustNewTestEngine(t, mustCreateTestSession(t), &fakeCompactionClient{compactionErrors: []error{runErr}},
			tools.NewRegistry(), Config{Model: "gpt-5", CompactionMode: "native", OnEvent: func(event Event) {
				observe(event)
				if event.Compaction != nil && event.Compaction.RequestID != nil && *event.Compaction.RequestID == requestID {
					_, err := engine.RemovePendingWork(t.Context(), itemID)
					startedRemoval <- err
				}
			}})
		pendingWorkTestNoError(t, steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(steeringPriorityNormal,
			steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})))
		_, err := engine.CompactContextAdmissionForRequestWithAcceptance(t.Context(), requestID,
			runtimeinput.ManualCompactionAdmission{Guidance: textutil.Value("preserve facts")}, nil)
		pendingWorkTestNoError(t, err)
	} else {
		engine = pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: observe})
		operationID := clientui.NewWorktreeTransitionID()
		itemID = pendingWorkTestID(t, operationID.String())
		_, err := engine.ScheduleWorktreeTransition(t.Context(), operationID,
			runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave},
			func(context.Context) error {
				_, err := engine.RemovePendingWork(t.Context(), itemID)
				startedRemoval <- err
				return runErr
			})
		pendingWorkTestNoError(t, err)
	}
	err := pendingWorkTestWaitValue(t, startedRemoval, "operation start")
	pendingWorkTestRequire(t, errors.Is(err, runtimeinput.ErrPendingWorkNotPending), "remove started %s = %v", kind, err)
	waitEngineLifecycleTasks(t, engine)
	pendingWorkTestRequire(t, changes.Load() == 2, "Pending Work Changed notifications = %d", changes.Load())
	return restorations
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
