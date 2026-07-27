package workflowrunner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/config"
)

func TestSchedulerSelectsOldestRunnableRunAndRespectsConcurrency(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	first := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	second := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	starter := &recordingStarter{}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 1})
	registerAutomaticStarts(t, scheduler, []workflow.RunID{first.RunID, second.RunID})

	if err := scheduler.Process(ctx); err != nil {
		t.Fatalf("Process: %v", err)
	}
	started := starter.requests()
	if len(started) != 1 || started[0].RunID != first.RunID {
		t.Fatalf("started = %+v, want first run %s", started, first.RunID)
	}
	if scheduler.ActiveCount() != 1 {
		t.Fatalf("active count = %d, want 1", scheduler.ActiveCount())
	}
	runs, err := store.ListRuns(ctx, second.TaskID)
	if err != nil {
		t.Fatalf("ListRuns second: %v", err)
	}
	if runs[0].StartedAt != nil {
		t.Fatalf("second run was durably started despite concurrency cap: %+v", runs[0])
	}
}

func TestSchedulerConcurrentProcessStartsOneRuntimePerRun(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	firstRun := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	secondRun := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	starter := &recordingStarter{}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 1})
	registerAutomaticStarts(t, scheduler, []workflow.RunID{firstRun.RunID, secondRun.RunID})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = scheduler.Process(ctx)
		}()
	}
	wg.Wait()

	started := starter.requests()
	if len(started) != 1 || started[0].RunID != firstRun.RunID {
		t.Fatalf("started = %+v, want one start for %s", started, firstRun.RunID)
	}
	if scheduler.ActiveCount() != 1 {
		t.Fatalf("active count = %d, want concurrency cap 1", scheduler.ActiveCount())
	}
}

func TestSchedulerStartDoesNotRebuildPersistedRunnableWork(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	startedRun := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	starter := &recordingStarter{}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 1})

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started := starter.requests(); len(started) != 0 {
		t.Fatalf("started = %+v, want no reconstructed automatic intent", started)
	}
	runs, err := store.ListRuns(ctx, startedRun.TaskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if runs[0].StartedAt != nil {
		t.Fatalf("run = %+v, want unstarted without an in-memory intent", runs[0])
	}
}

func TestSchedulerStartProcessesRunnableWorkCreatedAfterStartup(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	starter := &recordingStarter{}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 1}, WithSchedulerProcessInterval(10*time.Millisecond))
	t.Cleanup(func() { _ = scheduler.Close() })
	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	startedRun := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	registerAutomaticStarts(t, scheduler, []workflow.RunID{startedRun.RunID})

	if testsetup.Until(time.Now().Add(2*time.Second), 10*time.Millisecond, func() bool {
		started := starter.requests()
		return len(started) == 1 && started[0].RunID == startedRun.RunID
	}) {
		return
	}
	t.Fatalf("scheduler did not process post-start runnable run; requests=%+v", starter.requests())
}

func TestSchedulerProcessesSharedAutomaticIntentImmediately(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	intents := workflowexecution.NewAutomaticIntents()
	starter := &recordingStarter{}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 1},
		WithAutomaticIntents(intents),
		WithSchedulerProcessInterval(time.Hour),
	)
	t.Cleanup(func() { _ = scheduler.Close() })
	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	startedRun := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	registerAutomaticStarts(t, intents, []workflow.RunID{startedRun.RunID})

	if testsetup.Until(time.Now().Add(2*time.Second), 10*time.Millisecond, func() bool {
		started := starter.requests()
		return len(started) == 1 && started[0].RunID == startedRun.RunID
	}) {
		return
	}
	t.Fatalf("scheduler did not process shared automatic intent; requests=%+v", starter.requests())
}

func TestSchedulerSharedAutomaticIntentSkipsStaleHeadWithoutWaitingForTicker(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	intents := workflowexecution.NewAutomaticIntents()
	starter := &recordingStarter{}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 1},
		WithAutomaticIntents(intents),
		WithSchedulerProcessInterval(time.Hour),
	)
	t.Cleanup(func() { _ = scheduler.Close() })
	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	runnable := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	registerAutomaticStarts(t, intents, []workflow.RunID{"run-does-not-exist", runnable.RunID})

	if testsetup.Until(time.Now().Add(2*time.Second), 10*time.Millisecond, func() bool {
		started := starter.requests()
		return len(started) == 1 && started[0].RunID == runnable.RunID
	}) {
		return
	}
	t.Fatalf("scheduler did not skip stale shared intent; requests=%+v", starter.requests())
}

func TestSchedulerRetriesRecoverableClaimFailureWithoutNewIntent(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	startedRun := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	starter := &recordingStarter{}
	scheduler := newSchedulerTestService(t, &failingClaimStore{
		SchedulerStore: store,
		failures:       4,
	}, starter, SchedulerConfig{Concurrency: 1})
	registerAutomaticStarts(t, scheduler, []workflow.RunID{startedRun.RunID})

	if err := scheduler.Process(ctx); err == nil {
		t.Fatal("expected temporary claim failure")
	}
	if err := scheduler.Process(ctx); err != nil {
		t.Fatalf("retry Process: %v", err)
	}
	started := starter.requests()
	if len(started) != 1 || started[0].RunID != startedRun.RunID {
		t.Fatalf("started = %+v, want retry to start %s", started, startedRun.RunID)
	}
}

func TestSchedulerDoesNotScheduleCanceledOrInterruptedTasks(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	canceledTask, err := store.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Canceled", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask canceled: %v", err)
	}
	canceledRun, err := store.StartTask(ctx, canceledTask.ID)
	if err != nil {
		t.Fatalf("StartTask canceled: %v", err)
	}
	if _, err := store.CancelTask(ctx, canceledTask.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	interrupted := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	if err := store.InterruptRun(ctx, interrupted.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	starter := &recordingStarter{}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 5})
	registerAutomaticStarts(t, scheduler, []workflow.RunID{canceledRun.RunID, interrupted.RunID})

	if err := scheduler.Process(ctx); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if started := starter.requests(); len(started) != 0 {
		t.Fatalf("started canceled/interrupted runs = %+v; canceled run was %s", started, canceledRun.RunID)
	}
}

func TestSchedulerRestartKeepsInterruptedRunInterrupted(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	interrupted := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	if err := store.InterruptRun(ctx, interrupted.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	starter := &recordingStarter{}
	restarted := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 5})

	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := restarted.Process(ctx); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if started := starter.requests(); len(started) != 0 {
		t.Fatalf("restarted scheduler started interrupted run: %+v", started)
	}
	runs, err := store.ListRuns(ctx, interrupted.TaskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if runs[0].InterruptedAt == nil || runs[0].InterruptionReason == nil || *runs[0].InterruptionReason != "manual" {
		t.Fatalf("interrupted run changed after restart: %+v", runs[0])
	}
}

func TestSchedulerRestartKeepsPendingApprovalUnscheduled(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	workflowID := createSchedulerApprovalWorkflow(t, ctx, store)
	linkSchedulerWorkflow(t, ctx, store, binding.ProjectID, workflowID)
	started := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	if _, err := store.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun pending approval: %v", err)
	}
	starter := &recordingStarter{}
	restarted := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 5})

	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := restarted.Process(ctx); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if startedRuns := starter.requests(); len(startedRuns) != 0 {
		t.Fatalf("restarted scheduler started pending approval target: %+v", startedRuns)
	}
	transitions, err := store.ListTransitions(ctx, started.TaskID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].State != "pending_approval" {
		t.Fatalf("transitions after restart = %+v, want pending approval preserved", transitions)
	}
}

func TestSchedulerActiveOwnershipIsMemoryOnly(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	startedRun := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	scheduler := newSchedulerTestService(t, store, &recordingStarter{}, SchedulerConfig{Concurrency: 1})
	registerAutomaticStarts(t, scheduler, []workflow.RunID{startedRun.RunID})
	if err := scheduler.Process(ctx); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if scheduler.ActiveCount() != 1 {
		t.Fatalf("active count = %d, want in-memory ownership", scheduler.ActiveCount())
	}
	finalizer := &recordingInterruptedRunFinalizer{}
	restarted := newSchedulerTestService(t, store, nil, SchedulerConfig{Concurrency: 1}, WithSchedulerAttentionFinalizer(finalizer))
	if restarted.ActiveCount() != 0 {
		t.Fatalf("restarted active count = %d, want no durable ownership", restarted.ActiveCount())
	}
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile restarted: %v", err)
	}
	runs, err := store.ListRuns(ctx, startedRun.TaskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if runs[0].InterruptedAt == nil || runs[0].InterruptionReason == nil || *runs[0].InterruptionReason != ReasonSchedulerStartupOrphanedRun {
		t.Fatalf("restarted scheduler did not treat prior active owner as orphaned: %+v", runs[0])
	}
	if len(finalizer.interruptedRuns) != 1 || finalizer.interruptedRuns[0] != startedRun.RunID {
		t.Fatalf("interrupted run finalizations = %+v, want %s", finalizer.interruptedRuns, startedRun.RunID)
	}
}

func TestSchedulerCloseStopsNewClaims(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	_ = createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	starter := &recordingStarter{}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 1})
	if err := scheduler.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := scheduler.Process(ctx); err == nil {
		t.Fatalf("expected stopped scheduler error")
	}
	if started := starter.requests(); len(started) != 0 {
		t.Fatalf("stopped scheduler started runs: %+v", started)
	}
}

func TestSchedulerRecoveryInterruptsOrphanedStartedAndUnstartedRuns(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	orphan := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	if _, err := store.ClaimRun(ctx, orphan.RunID, 0); err != nil {
		t.Fatalf("ClaimRun orphan: %v", err)
	}
	runnable := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	scheduler := newSchedulerTestService(t, store, nil, SchedulerConfig{Concurrency: 5})

	if err := scheduler.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	orphanRuns, err := store.ListRuns(ctx, orphan.TaskID)
	if err != nil {
		t.Fatalf("ListRuns orphan: %v", err)
	}
	if orphanRuns[0].InterruptedAt == nil || orphanRuns[0].InterruptionReason == nil || *orphanRuns[0].InterruptionReason != ReasonSchedulerStartupOrphanedRun {
		t.Fatalf("orphan run = %+v, want interrupted", orphanRuns[0])
	}
	runnableRuns, err := store.ListRuns(ctx, runnable.TaskID)
	if err != nil {
		t.Fatalf("ListRuns runnable: %v", err)
	}
	if runnableRuns[0].InterruptedAt == nil || runnableRuns[0].InterruptionReason == nil || *runnableRuns[0].InterruptionReason != ReasonSchedulerStartupUnstartedRun {
		t.Fatalf("unstarted run = %+v, want interrupted", runnableRuns[0])
	}
}

func TestSchedulerRecoveryInterruptsUnstartedRuns(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	queued := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	finalizer := &recordingInterruptedRunFinalizer{}
	scheduler := newSchedulerTestService(t, store, nil, SchedulerConfig{Concurrency: 1}, WithSchedulerAttentionFinalizer(finalizer))

	if err := scheduler.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	runs, err := store.ListRuns(ctx, queued.TaskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].InterruptedAt == nil || runs[0].InterruptionReason == nil || *runs[0].InterruptionReason != ReasonSchedulerStartupUnstartedRun {
		t.Fatalf("recovered unstarted run = %+v", runs)
	}
	if len(finalizer.interruptedRuns) != 1 || finalizer.interruptedRuns[0] != queued.RunID {
		t.Fatalf("interrupted run finalizations = %+v, want %s", finalizer.interruptedRuns, queued.RunID)
	}
}

func TestSchedulerExplicitRunsBypassAutomaticConcurrency(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	automatic := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	explicit := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	starter := &recordingStarter{}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 1})

	registerAutomaticStarts(t, scheduler, []workflow.RunID{automatic.RunID})
	if err := scheduler.Process(ctx); err != nil {
		t.Fatalf("Process automatic: %v", err)
	}
	if err := scheduler.StartExplicitRuns(ctx, []workflow.RunID{explicit.RunID}); err != nil {
		t.Fatalf("StartExplicitRuns: %v", err)
	}
	started := starter.requests()
	if len(started) != 2 || started[0].RunID != automatic.RunID || started[1].RunID != explicit.RunID {
		t.Fatalf("started = %+v, want automatic %s then explicit %s", started, automatic.RunID, explicit.RunID)
	}
}

func TestSchedulerRecoveryUsesPendingAskResolver(t *testing.T) {
	ctx, store, binding, metadataStore := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	keep := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	drop := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	if _, err := metadataStore.DB().ExecContext(ctx, `UPDATE task_runs SET waiting_ask_id = 'ask-keep' WHERE id = ?`, keep.RunID); err != nil {
		t.Fatalf("mark keep waiting: %v", err)
	}
	if _, err := metadataStore.DB().ExecContext(ctx, `UPDATE task_runs SET waiting_ask_id = 'ask-drop' WHERE id = ?`, drop.RunID); err != nil {
		t.Fatalf("mark drop waiting: %v", err)
	}
	resolver := pendingAskResolverFunc(func(_ context.Context, _ string, _ workflow.RunID, askID string) (bool, error) {
		return askID == "ask-keep", nil
	})
	scheduler := newSchedulerTestService(t, store, nil, SchedulerConfig{Concurrency: 5}, WithSchedulerPendingAskResolver(resolver))

	if err := scheduler.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	keepRuns, err := store.ListRuns(ctx, keep.TaskID)
	if err != nil {
		t.Fatalf("ListRuns keep: %v", err)
	}
	if keepRuns[0].InterruptedAt != nil || keepRuns[0].WaitingAskID == nil || *keepRuns[0].WaitingAskID != "ask-keep" {
		t.Fatalf("keep waiting run = %+v", keepRuns[0])
	}
	dropRuns, err := store.ListRuns(ctx, drop.TaskID)
	if err != nil {
		t.Fatalf("ListRuns drop: %v", err)
	}
	if dropRuns[0].InterruptedAt == nil || dropRuns[0].InterruptionReason == nil || *dropRuns[0].InterruptionReason != ReasonSchedulerPendingAskUnavailable {
		t.Fatalf("drop waiting run = %+v, want interrupted", dropRuns[0])
	}
}

func TestSchedulerRuntimeStartFailureInterruptsRun(t *testing.T) {
	ctx, store, binding, metadataStore := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	started := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	starter := &recordingStarter{err: errors.New("role missing")}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 1})
	registerAutomaticStarts(t, scheduler, []workflow.RunID{started.RunID})

	if err := scheduler.Process(ctx); !errors.Is(err, ErrSchedulerRuntimeStartFailed) {
		t.Fatalf("Process error = %v, want ErrSchedulerRuntimeStartFailed", err)
	}
	runs, err := store.ListRuns(ctx, started.TaskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if runs[0].InterruptedAt == nil || runs[0].InterruptionReason == nil || *runs[0].InterruptionReason != ReasonSchedulerRuntimeStartFailed {
		t.Fatalf("run after starter failure = %+v", runs[0])
	}
	var detail string
	if err := metadataStore.DB().QueryRowContext(ctx, `SELECT interruption_detail_json FROM task_runs WHERE id = ?`, string(runs[0].ID)).Scan(&detail); err != nil {
		t.Fatalf("query interruption detail: %v", err)
	}
	if !strings.Contains(detail, "role missing") {
		t.Fatalf("interruption detail = %s, want starter error", detail)
	}
}

func TestSchedulerStartContinuesAfterRuntimeStartFailure(t *testing.T) {
	ctx, store, binding, _ := newSchedulerTestContextStore(t)
	createLinkedSchedulerValidWorkflow(t, ctx, store, binding.ProjectID)
	starter := &recordingStarter{err: errors.New("role missing")}
	scheduler := newSchedulerTestService(t, store, starter, SchedulerConfig{Concurrency: 1})
	t.Cleanup(func() { _ = scheduler.Close() })

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !scheduler.Started() {
		t.Fatal("scheduler not marked started after isolated runtime start failure")
	}
	started := createAndStartSchedulerTask(t, ctx, store, binding.ProjectID)
	registerAutomaticStarts(t, scheduler, []workflow.RunID{started.RunID})
	if err := scheduler.Process(ctx); !errors.Is(err, ErrSchedulerRuntimeStartFailed) {
		t.Fatalf("Process: %v, want ErrSchedulerRuntimeStartFailed", err)
	}
	runs, err := store.ListRuns(ctx, started.TaskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if runs[0].InterruptedAt == nil || runs[0].InterruptionReason == nil || *runs[0].InterruptionReason != ReasonSchedulerRuntimeStartFailed {
		t.Fatalf("run after startup starter failure = %+v", runs[0])
	}
}

type recordingStarter struct {
	mu      sync.Mutex
	started []SchedulerStartRunRequest
	err     error
}

func (s *recordingStarter) PrepareWorkflowRun(_ context.Context, req SchedulerPrepareRunRequest) (PreparedWorkflowRun, error) {
	start := SchedulerStartRunRequest{
		RunID:       req.RunID,
		TaskID:      req.TaskID,
		PlacementID: req.PlacementID,
		NodeID:      req.NodeID,
		Generation:  req.Generation,
	}
	if s.err != nil {
		return nil, s.err
	}
	return schedulerPreparedRun{activate: func() {
		s.mu.Lock()
		s.started = append(s.started, start)
		s.mu.Unlock()
	}}, nil
}

func (s *recordingStarter) requests() []SchedulerStartRunRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SchedulerStartRunRequest{}, s.started...)
}

type failingClaimStore struct {
	SchedulerStore
	mu       sync.Mutex
	failures int
}

func (s *failingClaimStore) AdmitRun(ctx context.Context, admission workflowstore.RunAdmission) (workflowstore.RunnableRunRecord, error) {
	s.mu.Lock()
	if s.failures > 0 {
		s.failures--
		s.mu.Unlock()
		return workflowstore.RunnableRunRecord{}, errors.New("temporary claim failure")
	}
	s.mu.Unlock()
	return s.SchedulerStore.AdmitRun(ctx, admission)
}

type schedulerPreparedRun struct {
	admission RunAdmission
	activate  func()
	abort     func(context.Context) error
	commitErr error
}

func (p schedulerPreparedRun) Admission() RunAdmission {
	return p.admission
}

func (p schedulerPreparedRun) Commit() error {
	return p.commitErr
}

func (p schedulerPreparedRun) Activate() {
	if p.activate != nil {
		p.activate()
	}
}

func (p schedulerPreparedRun) Abort(ctx context.Context) error {
	if p.abort == nil {
		return nil
	}
	return p.abort(ctx)
}

type pendingAskResolverFunc func(context.Context, string, workflow.RunID, string) (bool, error)

func (f pendingAskResolverFunc) CanRehydrate(ctx context.Context, sessionID string, runID workflow.RunID, askID string) (bool, error) {
	return f(ctx, sessionID, runID, askID)
}

func newSchedulerTestService(t *testing.T, store SchedulerStore, starter SchedulerRuntimeStarter, cfg SchedulerConfig, opts ...SchedulerOption) *SchedulerService {
	t.Helper()
	scheduler, err := NewSchedulerService(store, starter, workflowexecution.NewMutationPermit(), cfg, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return scheduler
}

func newSchedulerTestStore(t *testing.T) (*workflowstore.Store, metadata.Binding, *metadata.Store) {
	t.Helper()
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, "kent-root"))
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore := testsetup.OpenStore(t, cfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "SCH"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")), workflowstore.WithNow(func() time.Time {
		now = now.Add(time.Millisecond)
		return now
	}))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	return store, binding, metadataStore
}

func newSchedulerTestContextStore(t *testing.T) (context.Context, *workflowstore.Store, metadata.Binding, *metadata.Store) {
	t.Helper()
	store, binding, metadataStore := newSchedulerTestStore(t)
	return context.Background(), store, binding, metadataStore
}

func createLinkedSchedulerValidWorkflow(t *testing.T, ctx context.Context, store *workflowstore.Store, projectID string) workflow.WorkflowID {
	t.Helper()
	workflowID := createSchedulerValidWorkflow(t, ctx, store)
	linkSchedulerWorkflow(t, ctx, store, projectID, workflowID)
	return workflowID
}

func linkSchedulerWorkflow(t *testing.T, ctx context.Context, store *workflowstore.Store, projectID string, workflowID workflow.WorkflowID) {
	t.Helper()
	if _, err := store.LinkWorkflow(ctx, projectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
}

func createSchedulerValidWorkflow(t *testing.T, ctx context.Context, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := schedulerNodeByKind(t, def, workflow.NodeKindStart)
	done := schedulerNodeByKind(t, def, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-agent-" + string(created.ID))
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-start-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-start-" + string(created.ID)), Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(created.ID)), Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	return created.ID
}

func createSchedulerApprovalWorkflow(t *testing.T, ctx context.Context, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	workflowID := createSchedulerValidWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	doneEdge := workflow.EdgeID("")
	for _, edge := range def.Edges {
		if edge.Key == "done" {
			doneEdge = edge.ID
			break
		}
	}
	if doneEdge == "" {
		t.Fatalf("done edge missing in %+v", def.Edges)
	}
	if err := store.DeleteEdge(ctx, doneEdge); err != nil {
		t.Fatalf("DeleteEdge done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: doneEdge, WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(workflowID)), Key: "done", TargetNodeID: workflow.NodeIDOf(schedulerNodeByKind(t, def, workflow.NodeKindTerminal)), ContextMode: workflow.ContextModeNewSession, RequiresApproval: true}); err != nil {
		t.Fatalf("Update approval edge: %v", err)
	}
	return workflowID
}

type schedulerStartedTask struct {
	TaskID workflow.TaskID
	workflowstore.StartTaskResult
}

func createAndStartSchedulerTask(t *testing.T, ctx context.Context, store *workflowstore.Store, projectID string) schedulerStartedTask {
	t.Helper()
	task, err := store.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: projectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := store.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	return schedulerStartedTask{TaskID: task.ID, StartTaskResult: started}
}

func registerAutomaticStarts(t *testing.T, registrar workflowexecution.AutomaticStartRegistrar, runIDs []workflow.RunID) {
	t.Helper()
	if err := registrar.RegisterAutomaticStarts(runIDs); err != nil {
		t.Fatalf("RegisterAutomaticStarts: %v", err)
	}
}

func schedulerNodeByKind(t *testing.T, def workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind() == kind {
			return node
		}
	}
	t.Fatalf("node kind %s missing in %+v", kind, def.Nodes)
	return nil
}
