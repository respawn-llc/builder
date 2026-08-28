package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
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
func TestPendingWorkProjectsQueueBeforeSharedSteerAdmissionOrder(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	release := pendingWorkTestHoldMaintenance(t, engine)
	defer release()
	first, err := engine.QueueUserMessageForAutoDrain(t.Context(), "first steer")
	pendingWorkTestNoError(t, err)
	guidance := "keep details"
	requestID := runtimeids.NewCompactionRequestID()
	_, err = engine.CompactContextAdmissionForRequestWithAcceptance(t.Context(), requestID, runtimeinput.ManualCompactionAdmission{Guidance: &guidance}, nil)
	pendingWorkTestNoError(t, err)
	selector := "feature/pending-work"
	operationID := clientui.NewWorktreeTransitionID()
	ack, err := engine.ScheduleWorktreeTransition(
		t.Context(), operationID, runtimeinput.PendingWorkWorktreeTransition{
			Transition: runtimeinput.PendingWorkWorktreeTransitionEnter, Selector: &selector,
		},
		func(context.Context) error { return nil },
	)
	if err != nil || ack.OperationId != operationID.String() {
		t.Fatalf("schedule Worktree transition = %+v/%v", ack, err)
	}
	second, err := engine.QueueUserMessageForAutoDrain(t.Context(), "second steer")
	pendingWorkTestNoError(t, err)
	queued, err := engine.QueueUserMessage(t.Context(), "post-turn queue")
	pendingWorkTestNoError(t, err)
	items := pendingWorkTestSnapshot(t, engine).Items
	if len(items) != 5 || items[0].ID.String() != queued.ID || items[1].ID.String() != first.ID ||
		items[2].ID.String() != requestID.String() || items[3].ID.String() != operationID.String() ||
		items[4].ID.String() != second.ID {
		t.Fatalf("Pending Work order = %+v", items)
	}
	if items[0].Lane != runtimeinput.PendingWorkLaneQueue || items[0].CanonicalInput != "post-turn queue" || items[1].CanonicalInput != "first steer" || items[2].CanonicalInput != "/compact keep details" ||
		items[3].CanonicalInput != "/wt switch feature/pending-work" {
		t.Fatalf("Pending Work projection = %+v", items)
	}
}
func TestPendingWorkCapacity(t *testing.T) {
	t.Run("reject at 100", func(t *testing.T) {
		engine, release := pendingWorkTestSeedMessages(t, runtimeinput.PendingWorkCapacity)
		defer release()
		_, err := engine.ScheduleWorktreeTransition(t.Context(), clientui.NewWorktreeTransitionID(),
			runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave}, func(context.Context) error { return nil })
		if !errors.Is(err, runtimeinput.ErrPendingWorkCapacity) || len(pendingWorkTestSnapshot(t, engine).Items) != runtimeinput.PendingWorkCapacity {
			t.Fatalf("capacity rejection = %T %v", err, err)
		}
	})
	t.Run("admit at 99", func(t *testing.T) {
		engine, release := pendingWorkTestSeedMessages(t, runtimeinput.PendingWorkCapacity-1)
		defer release()
		_, err := engine.CompactContextAdmissionForRequestWithAcceptance(t.Context(), runtimeids.NewCompactionRequestID(), runtimeinput.ManualCompactionAdmission{}, nil)
		pendingWorkTestNoError(t, err)
		if got := len(pendingWorkTestSnapshot(t, engine).Items); got != runtimeinput.PendingWorkCapacity {
			t.Fatalf("Pending Work count = %d", got)
		}
	})
	t.Run("concurrent overshoot", func(t *testing.T) {
		engine, release := pendingWorkTestSeedMessages(t, runtimeinput.PendingWorkCapacity-1)
		defer release()
		reached, unblock := make(chan struct{}, 2), make(chan struct{})
		accept := CommandAcceptance(func(commit func() (bool, error)) (bool, error) {
			reached <- struct{}{}
			<-unblock
			return commit()
		})
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := engine.ScheduleWorktreeTransitionWithAcceptance(t.Context(), clientui.NewWorktreeTransitionID(),
					runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave}, accept, func(context.Context) error { return nil })
				results <- err
			}()
		}
		pendingWorkTestWaitValue(t, reached, "first acceptance")
		pendingWorkTestWaitValue(t, reached, "second acceptance")
		close(unblock)
		pendingWorkTestNoError(t, <-results)
		pendingWorkTestNoError(t, <-results)
		if got := len(pendingWorkTestSnapshot(t, engine).Items); got != runtimeinput.PendingWorkCapacity+1 {
			t.Fatalf("Pending Work count = %d", got)
		}
	})
}
func TestPendingOperationalWorkRemovalAndStartLifecycle(t *testing.T) {
	schedulers := []struct {
		name      string
		kind      runtimeinput.PendingWorkItemKind
		canonical string
		run       func(*testing.T, *Engine) runtimeids.QueueItemID
	}{
		{"manual compaction", runtimeinput.PendingWorkItemKindManualCompaction, "/compact", pendingWorkTestScheduleCompaction},
		{"Worktree", runtimeinput.PendingWorkItemKindWorktreeTransition, "/wt leave", func(t *testing.T, engine *Engine) runtimeids.QueueItemID {
			itemID := pendingWorkTestScheduleWorktree(t, engine, func(context.Context) error { return nil })
			return itemID
		}},
	}
	for _, scheduler := range schedulers {
		t.Run(scheduler.name+"/remove before start", func(t *testing.T) {
			var changes atomic.Int32
			engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: pendingWorkChangedCounter(&changes)})
			release := pendingWorkTestHoldMaintenance(t, engine)
			id := scheduler.run(t, engine)
			restoration, err := engine.RemovePendingWork(t.Context(), id)
			pendingWorkTestNoError(t, err)
			if restoration.Kind != scheduler.kind || restoration.CanonicalInput != scheduler.canonical {
				t.Fatalf("Pending Work restoration = %+v", restoration)
			}
			release()
			waitEngineLifecycleTasks(t, engine)
			if pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), id) || changes.Load() != 2 {
				t.Fatalf("Pending Work removal/list notifications = %+v/%d", pendingWorkTestSnapshot(t, engine), changes.Load())
			}
		})
	}
	t.Run("manual compaction not pending after start", func(t *testing.T) {
		var changes atomic.Int32
		started := make(chan bool, 1)
		requestID := runtimeids.NewCompactionRequestID()
		var engine *Engine
		engine = pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: func(event Event) {
			pendingWorkChangedCounter(&changes)(event)
			if event.Compaction != nil && event.Compaction.RequestID != nil && *event.Compaction.RequestID == requestID {
				select {
				case started <- len(pendingWorkTestSnapshot(t, engine).Items) != 0:
				default:
				}
			}
		}})
		_, err := engine.CompactContextAdmissionForRequestWithAcceptance(t.Context(), requestID, runtimeinput.ManualCompactionAdmission{}, nil)
		pendingWorkTestNoError(t, err)
		if pendingWorkTestWaitValue(t, started, "manual compaction start") {
			t.Fatal("started manual compaction remained pending")
		}
		itemID, err := serverapi.PendingWorkItemIDFromCompactionRequest(requestID)
		pendingWorkTestNoError(t, err)
		if _, err := engine.RemovePendingWork(t.Context(), itemID); !errors.Is(err, runtimeinput.ErrPendingWorkNotPending) {
			t.Fatalf("remove started manual compaction = %v", err)
		}
		waitEngineLifecycleTasks(t, engine)
		if changes.Load() != 2 {
			t.Fatalf("Pending Work Changed notifications = %d", changes.Load())
		}
	})
	t.Run("Worktree not pending after start", func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
		itemID := pendingWorkTestScheduleWorktree(t, engine, func(context.Context) error { close(started); <-release; return nil })
		pendingWorkTestWaitValue(t, started, "Worktree start")
		if pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), itemID) {
			t.Fatal("started Worktree transition remained pending")
		}
		if _, err := engine.RemovePendingWork(t.Context(), itemID); !errors.Is(err, runtimeinput.ErrPendingWorkNotPending) {
			t.Fatalf("remove started Worktree transition = %v", err)
		}
		close(release)
		waitEngineLifecycleTasks(t, engine)
	})
}
func TestPendingWorkListIsIndependentlyReadable(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	changed, release := make(chan struct{}), make(chan struct{})
	engine.cfg.OnEvent = func(event Event) {
		if event.Kind == EventPendingWorkChanged {
			close(changed)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { _, err := engine.QueueUserMessage(t.Context(), "first"); done <- err }()
	pendingWorkTestWaitValue(t, changed, "Pending Work Changed")
	snapshot := pendingWorkTestSnapshot(t, engine)
	if len(snapshot.Items) != 1 || snapshot.Items[0].CanonicalInput != "first" {
		t.Fatalf("Pending Work list = %+v", snapshot.Items)
	}
	close(release)
	pendingWorkTestNoError(t, <-done)
}
func TestPendingOperationalWorkTechnicalRestoration(t *testing.T) {
	failure := errors.New("technical application failure")
	tests := []struct {
		name       string
		runErr     error
		discard    bool
		compaction bool
		want       runtimeinput.PendingWorkItemKind
	}{
		{name: "Worktree definitely unapplied", runErr: worktreeTechnicalTestError{failure}, want: runtimeinput.PendingWorkItemKindWorktreeTransition},
		{name: "compaction definitely unapplied", runErr: failure, compaction: true, want: runtimeinput.PendingWorkItemKindManualCompaction},
		{name: "user-correctable", runErr: errors.New("selector not found")},
		{name: "applied later publication", runErr: worktreeAppliedTestError{failure}},
		{name: "indeterminate", runErr: worktreeIndeterminateTestError{worktreeTechnicalTestError{failure}}},
		{name: "discard", runErr: worktreeTechnicalTestError{failure}, discard: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var restorations []runtimeinput.PendingWorkTechnicalRestoration
			observe := func(event Event) {
				if event.Kind == EventPendingWorkRestored {
					restorations = append(restorations, *event.PendingWorkRestoration)
				}
			}
			if test.compaction {
				pendingWorkTestRunTechnicalCompaction(t, observe, test.runErr)
			} else {
				pendingWorkTestRunWorktree(t, observe, test.runErr, test.discard)
			}
			if test.want == "" && len(restorations) != 0 {
				t.Fatalf("technical restorations = %+v, want none", restorations)
			}
			if test.want != "" && (len(restorations) != 1 || restorations[0].Kind != test.want) {
				t.Fatalf("technical restorations = %+v, want one %s", restorations, test.want)
			}
		})
	}
}
func TestIndeterminateWorktreeFailureRetiresRuntimeAfterLifecycleRelease(t *testing.T) {
	transitionFailure := errors.New("rollback target is indeterminate")
	retirementFailure := errors.New("retire exact Runtime resource")
	retired := make(chan struct{})
	var queuedID string
	var queuedFailed atomic.Bool
	var engine *Engine
	engine = pendingWorkTestEngine(t, Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if status := event.QueuedUserMessageStatus; status != nil &&
				status.QueueItemID == queuedID && status.Status == QueuedUserMessageFailed &&
				status.FailureReason == QueuedUserMessageFailureRuntimeUnavailable {
				queuedFailed.Store(true)
			}
		},
		LifecycleRuntimeAbort: func() error {
			engine.lifecycleWG.Wait()
			close(retired)
			return retirementFailure
		},
	})
	release := pendingWorkTestHoldMaintenance(t, engine)
	pendingWorkTestScheduleWorktree(t, engine, func(context.Context) error { return worktreeIndeterminateTestError{transitionFailure} })
	queued, err := engine.QueueUserMessage(t.Context(), "queued after Worktree transition")
	pendingWorkTestNoError(t, err)
	queuedID = queued.ID
	release()
	pendingWorkTestWaitValue(t, retired, "Runtime retirement")
	waitEngineLifecycleTasks(t, engine)

	_, err = engine.QueueUserMessage(t.Context(), "later human work")
	if !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("later human work error = %v", err)
	}
	if !queuedFailed.Load() {
		t.Fatal("queued human work was not failed as Runtime unavailable")
	}
	deadline := time.Now().Add(runtimeTestSynchronizationTimeout)
	for time.Now().Before(deadline) {
		diagnostic := engine.ChatSnapshot().StreamingError
		if strings.Contains(diagnostic, transitionFailure.Error()) && strings.Contains(diagnostic, retirementFailure.Error()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("retirement diagnostic = %q", engine.ChatSnapshot().StreamingError)
}
func pendingWorkTestRunTechnicalCompaction(t *testing.T, observe func(Event), failure error) {
	t.Helper()
	client := &fakeCompactionClient{compactionErrors: []error{failure}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{Model: "gpt-5", CompactionMode: "native", OnEvent: observe})
	err := steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal, steeringMessageEventNone, true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}},
	))
	pendingWorkTestNoError(t, err)
	_, err = engine.CompactContextAdmissionForRequestWithAcceptance(t.Context(), runtimeids.NewCompactionRequestID(),
		runtimeinput.ManualCompactionAdmission{Guidance: textutil.Value("preserve facts")}, nil)
	pendingWorkTestNoError(t, err)
	waitEngineLifecycleTasks(t, engine)
}
func pendingWorkTestRunWorktree(t *testing.T, observe func(Event), runErr error, discard bool) {
	t.Helper()
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: observe})
	var release func()
	if discard {
		release = pendingWorkTestHoldMaintenance(t, engine)
	}
	itemID := pendingWorkTestScheduleWorktree(t, engine, func(context.Context) error { return runErr })
	if discard {
		_, err := engine.RemovePendingWork(t.Context(), itemID)
		pendingWorkTestNoError(t, err)
		release()
	}
	waitEngineLifecycleTasks(t, engine)
}
func pendingWorkTestScheduleCompaction(t *testing.T, engine *Engine) runtimeids.QueueItemID {
	t.Helper()
	requestID := runtimeids.NewCompactionRequestID()
	_, err := engine.CompactContextAdmissionForRequestWithAcceptance(t.Context(), requestID, runtimeinput.ManualCompactionAdmission{}, nil)
	pendingWorkTestNoError(t, err)
	itemID, err := serverapi.PendingWorkItemIDFromCompactionRequest(requestID)
	pendingWorkTestNoError(t, err)
	return itemID
}
func pendingWorkTestScheduleWorktree(t *testing.T, engine *Engine, run func(context.Context) error) runtimeids.QueueItemID {
	t.Helper()
	operationID := clientui.NewWorktreeTransitionID()
	_, err := engine.ScheduleWorktreeTransition(t.Context(), operationID,
		runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionLeave}, run)
	pendingWorkTestNoError(t, err)
	itemID, err := serverapi.PendingWorkItemIDFromWorktreeOperation(operationID)
	pendingWorkTestNoError(t, err)
	return itemID
}
func pendingWorkTestSeedMessages(t *testing.T, count int) (*Engine, func()) {
	t.Helper()
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	release := pendingWorkTestHoldMaintenance(t, engine)
	for index := range count - 2 {
		_, err := engine.QueueUserMessage(t.Context(), fmt.Sprintf("queued %d", index))
		pendingWorkTestNoError(t, err)
	}
	pendingWorkTestScheduleCompaction(t, engine)
	pendingWorkTestScheduleWorktree(t, engine, func(context.Context) error { return nil })
	return engine, release
}
func pendingWorkChangedCounter(changes *atomic.Int32) func(Event) {
	return func(event Event) {
		if event.Kind == EventPendingWorkChanged {
			changes.Add(1)
		}
	}
}
func pendingWorkTestEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	return mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), cfg)
}
func pendingWorkTestHoldMaintenance(t *testing.T, engine *Engine) func() {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- engine.stepLifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindRuntimeMaintenance},
			func(context.Context, string) error { close(started); <-release; return nil })
	}()
	pendingWorkTestWaitValue(t, started, "Runtime maintenance")
	return func() {
		close(release)
		pendingWorkTestNoError(t, <-done)
	}
}
func pendingWorkTestSnapshot(t *testing.T, engine *Engine) runtimeinput.PendingWork {
	t.Helper()
	snapshot, err := engine.PendingWorkSnapshot()
	pendingWorkTestNoError(t, err)
	pendingWorkTestNoError(t, snapshot.Validate())
	return snapshot
}
func pendingWorkTestContains(pending runtimeinput.PendingWork, id runtimeids.QueueItemID) bool {
	for _, item := range pending.Items {
		if item.ID == id {
			return true
		}
	}
	return false
}
func pendingWorkTestNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func pendingWorkTestMust[T any](t *testing.T, operation func() (T, error)) T {
	t.Helper()
	value, err := operation()
	pendingWorkTestNoError(t, err)
	return value
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

var pendingWorkTestWait = pendingWorkTestWaitValue[struct{}]
