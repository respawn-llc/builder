package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
)

func TestSchedulerLostClaimResolvesAutomaticIntentForReregistration(t *testing.T) {
	const runID = workflow.RunID("run-lost-claim")
	intents := NewAutomaticIntents()
	scheduler, err := NewSchedulerService(
		lostClaimSchedulerStore{run: workflowstore.RunRecord{ID: runID, Generation: 1}},
		discardingSchedulerStarter{},
		NewMutationPermit(),
		SchedulerConfig{Concurrency: 1},
		WithAutomaticIntents(intents),
	)
	if err != nil {
		t.Fatalf("NewSchedulerService: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })

	if err := scheduler.RegisterAutomaticStarts([]workflow.RunID{runID}); err != nil {
		t.Fatalf("RegisterAutomaticStarts initial: %v", err)
	}
	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process after lost claim: %v", err)
	}
	if err := scheduler.RegisterAutomaticStarts([]workflow.RunID{runID}); err != nil {
		t.Fatalf("RegisterAutomaticStarts after lost claim: %v", err)
	}
	if got := intents.Take(1); len(got) != 1 || got[0] != runID {
		t.Fatalf("reregistered automatic intents = %v, want [%s]", got, runID)
	}
}

func TestSchedulerPreparationFailureDoesNotInterruptNewerGeneration(t *testing.T) {
	prepareErr := errors.New("runtime preparation failed")
	store := newPreparationFailureRaceStore()
	scheduler, err := NewSchedulerService(
		store,
		schedulerStarterFunc(func(context.Context, SchedulerPrepareRunRequest) (PreparedWorkflowRun, error) {
			return nil, prepareErr
		}),
		NewMutationPermit(),
		SchedulerConfig{Concurrency: 1},
	)
	if err != nil {
		t.Fatalf("NewSchedulerService: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })

	result := make(chan error, 1)
	go func() {
		result <- scheduler.StartExplicitRuns(context.Background(), []workflow.RunID{store.runID})
	}()
	<-store.cleanupEntered
	store.advanceGeneration()
	close(store.continueCleanup)

	if err := <-result; !errors.Is(err, prepareErr) {
		t.Fatalf("StartExplicitRuns error = %v, want preparation failure", err)
	}
	run := store.snapshot()
	if run.Generation != 2 || run.InterruptedAt != nil {
		t.Fatalf("newer run generation changed by stale preparation cleanup: %+v", run)
	}
}

func TestSchedulerResumeCancellationAbortsPreparedScopesAndPreservesInterruptedRuns(t *testing.T) {
	taskID := workflow.TaskID("task-canceled-resume")
	store := newResumeSchedulerStore(taskID, 2)
	firstPrepared := &resumePreparedRun{}
	secondPreparationStarted := make(chan struct{})
	starter := &resumeSchedulerStarter{
		prepare: func(ctx context.Context, request SchedulerPrepareRunRequest, index int) (PreparedWorkflowRun, error) {
			switch index {
			case 0:
				return firstPrepared, nil
			case 1:
				close(secondPreparationStarted)
				<-ctx.Done()
				return nil, context.Cause(ctx)
			default:
				return nil, errors.New("unexpected preparation")
			}
		},
	}
	scheduler := newResumeScheduler(t, store, starter)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := scheduler.ResumeTaskRuns(ctx, taskID)
		result <- err
	}()
	<-secondPreparationStarted
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("ResumeTaskRuns error = %v, want context.Canceled", err)
	}
	if firstPrepared.abortCount() != 1 {
		t.Fatalf("first preparation aborts = %d, want 1", firstPrepared.abortCount())
	}
	if store.admitCount() != 0 {
		t.Fatalf("durable admissions = %d, want 0", store.admitCount())
	}
	assertResumeStoreInterrupted(t, store)
	if scheduler.ActiveCount() != 0 {
		t.Fatalf("active count = %d, want no retained preparation", scheduler.ActiveCount())
	}
}

type schedulerStarterFunc func(context.Context, SchedulerPrepareRunRequest) (PreparedWorkflowRun, error)

func (f schedulerStarterFunc) PrepareWorkflowRun(ctx context.Context, request SchedulerPrepareRunRequest) (PreparedWorkflowRun, error) {
	return f(ctx, request)
}

type preparationFailureRaceStore struct {
	lostClaimSchedulerStore

	mu              sync.Mutex
	runID           workflow.RunID
	run             workflowstore.RunRecord
	cleanupEntered  chan struct{}
	continueCleanup chan struct{}
}

func newPreparationFailureRaceStore() *preparationFailureRaceStore {
	runID := workflow.RunID("run-preparation-failure-race")
	return &preparationFailureRaceStore{
		runID: runID,
		run: workflowstore.RunRecord{
			ID:          runID,
			TaskID:      "task-preparation-failure-race",
			PlacementID: "placement-preparation-failure-race",
			NodeID:      "node-preparation-failure-race",
			Generation:  1,
		},
		cleanupEntered:  make(chan struct{}),
		continueCleanup: make(chan struct{}),
	}
}

func (s *preparationFailureRaceStore) GetRun(context.Context, workflow.RunID) (workflowstore.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run, nil
}

func (s *preparationFailureRaceStore) InterruptRun(
	_ context.Context,
	_ workflow.RunID,
	reason string,
	_ string,
) error {
	close(s.cleanupEntered)
	<-s.continueCleanup
	s.mu.Lock()
	defer s.mu.Unlock()
	interruptedAt := int64(30)
	s.run.InterruptedAt = &interruptedAt
	s.run.InterruptionReason = &reason
	return nil
}

func (s *preparationFailureRaceStore) InterruptRunGeneration(
	_ context.Context,
	_ workflow.RunID,
	generation int64,
	reason string,
	_ string,
) error {
	close(s.cleanupEntered)
	<-s.continueCleanup
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.Generation != generation || s.run.InterruptedAt != nil {
		return sql.ErrNoRows
	}
	interruptedAt := int64(30)
	s.run.InterruptedAt = &interruptedAt
	s.run.InterruptionReason = &reason
	return nil
}

func (s *preparationFailureRaceStore) advanceGeneration() {
	s.mu.Lock()
	defer s.mu.Unlock()
	startedAt := int64(20)
	s.run.Generation++
	s.run.StartedAt = &startedAt
}

func (s *preparationFailureRaceStore) snapshot() workflowstore.RunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run
}

func TestSchedulerResumeAdmissionFailureAbortsAllScopesAndPreservesInterruptedRuns(t *testing.T) {
	taskID := workflow.TaskID("task-failed-resume-admission")
	admitErr := errors.New("resume admission unavailable")
	store := newResumeSchedulerStore(taskID, 2)
	store.admitErr = admitErr
	prepared := []*resumePreparedRun{{}, {}}
	starter := &resumeSchedulerStarter{
		prepare: func(_ context.Context, _ SchedulerPrepareRunRequest, index int) (PreparedWorkflowRun, error) {
			return prepared[index], nil
		},
	}
	scheduler := newResumeScheduler(t, store, starter)

	if _, err := scheduler.ResumeTaskRuns(context.Background(), taskID); !errors.Is(err, admitErr) {
		t.Fatalf("ResumeTaskRuns error = %v, want admission failure", err)
	}
	for index, item := range prepared {
		if item.abortCount() != 1 {
			t.Fatalf("preparation %d aborts = %d, want 1", index, item.abortCount())
		}
	}
	if store.admitCount() != 1 {
		t.Fatalf("durable admissions = %d, want 1 attempted batch", store.admitCount())
	}
	assertResumeStoreInterrupted(t, store)
	if scheduler.ActiveCount() != 0 {
		t.Fatalf("active count = %d, want no retained preparation", scheduler.ActiveCount())
	}
}

func TestSchedulerResumeCommitFailureAbortsBeforeDurableAdmission(t *testing.T) {
	taskID := workflow.TaskID("task-failed-resume-commit")
	store := newResumeSchedulerStore(taskID, 1)
	commitErr := errors.New("script process start failed")
	prepared := &resumePreparedRun{commitErr: commitErr}
	scheduler := newResumeScheduler(t, store, &resumeSchedulerStarter{
		prepare: func(context.Context, SchedulerPrepareRunRequest, int) (PreparedWorkflowRun, error) {
			return prepared, nil
		},
	})

	if _, err := scheduler.ResumeTaskRuns(context.Background(), taskID); !errors.Is(err, ErrSchedulerRuntimeStartFailed) || !errors.Is(err, commitErr) {
		t.Fatalf("ResumeTaskRuns error = %v, want runtime start and commit failures", err)
	}
	if prepared.abortCount() != 1 {
		t.Fatalf("preparation aborts = %d, want 1", prepared.abortCount())
	}
	if store.admitCount() != 0 {
		t.Fatalf("durable admissions = %d, want 0", store.admitCount())
	}
	assertResumeStoreInterrupted(t, store)
	if scheduler.ActiveCount() != 0 {
		t.Fatalf("active count = %d, want released ownership", scheduler.ActiveCount())
	}
}

func TestSchedulerResumeCommitFailureAbortsEveryBranchBeforeDurableAdmission(t *testing.T) {
	taskID := workflow.TaskID("task-failed-multi-run-resume-commit")
	store := newResumeSchedulerStore(taskID, 3)
	commitErr := errors.New("second script process start failed")
	prepared := []*resumePreparedRun{{}, {commitErr: commitErr}, {}}
	scheduler := newResumeScheduler(t, store, &resumeSchedulerStarter{
		prepare: func(_ context.Context, _ SchedulerPrepareRunRequest, index int) (PreparedWorkflowRun, error) {
			return prepared[index], nil
		},
	})

	if _, err := scheduler.ResumeTaskRuns(context.Background(), taskID); !errors.Is(err, ErrSchedulerRuntimeStartFailed) || !errors.Is(err, commitErr) {
		t.Fatalf("ResumeTaskRuns error = %v, want runtime start and commit failures", err)
	}
	if store.admitCount() != 0 {
		t.Fatalf("durable admissions = %d, want 0", store.admitCount())
	}
	assertResumeStoreInterrupted(t, store)
	if scheduler.ActiveCount() != 0 {
		t.Fatalf("active count = %d, want every preparation released", scheduler.ActiveCount())
	}
}

func TestSchedulerResumeCommitFailureDoesNotAllowSiblingCompletion(t *testing.T) {
	taskID := workflow.TaskID("task-fast-sibling-resume-commit")
	store := newResumeSchedulerStore(taskID, 3)
	commitErr := errors.New("second script process start failed")
	prepared := []*resumePreparedRun{
		{onActivate: func() { store.complete("a-run", 2) }},
		{commitErr: commitErr},
		{},
	}
	scheduler := newResumeScheduler(t, store, &resumeSchedulerStarter{
		prepare: func(_ context.Context, _ SchedulerPrepareRunRequest, index int) (PreparedWorkflowRun, error) {
			return prepared[index], nil
		},
	})

	if _, err := scheduler.ResumeTaskRuns(context.Background(), taskID); !errors.Is(err, ErrSchedulerRuntimeStartFailed) || !errors.Is(err, commitErr) {
		t.Fatalf("ResumeTaskRuns error = %v, want runtime start and commit failures", err)
	}
	if store.admitCount() != 0 {
		t.Fatalf("durable admissions = %d, want 0", store.admitCount())
	}
	assertResumeStoreInterrupted(t, store)
	if scheduler.ActiveCount() != 0 {
		t.Fatalf("active count = %d, want every exact scope retired", scheduler.ActiveCount())
	}
}

func TestSchedulerResumeCancellationDuringCommitAbortsBeforeDurableAdmission(t *testing.T) {
	taskID := workflow.TaskID("task-canceled-resume-commit")
	store := newResumeSchedulerStore(taskID, 3)
	commitStarted := make(chan struct{})
	releaseCommit := make(chan struct{})
	prepared := []*resumePreparedRun{
		{},
		{onCommit: func() {
			close(commitStarted)
			<-releaseCommit
		}},
		{},
	}
	scheduler := newResumeScheduler(t, store, &resumeSchedulerStarter{
		prepare: func(_ context.Context, _ SchedulerPrepareRunRequest, index int) (PreparedWorkflowRun, error) {
			return prepared[index], nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := scheduler.ResumeTaskRuns(ctx, taskID)
		result <- err
	}()
	<-commitStarted
	cancel()
	close(releaseCommit)

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("ResumeTaskRuns error = %v, want context.Canceled", err)
	}
	if store.admitCount() != 0 {
		t.Fatalf("durable admissions = %d, want 0", store.admitCount())
	}
	for index, item := range prepared {
		if item.activationCount() != 0 {
			t.Fatalf("preparation %d activations = %d, want 0", index, item.activationCount())
		}
		if item.abortCount() != 1 {
			t.Fatalf("preparation %d aborts = %d, want 1", index, item.abortCount())
		}
	}
	assertResumeStoreInterrupted(t, store)
	if scheduler.ActiveCount() != 0 {
		t.Fatalf("active count = %d, want no retained preparation", scheduler.ActiveCount())
	}
}

func TestSchedulerResumeActivatesBranchesOnlyAfterEveryCommitSucceeds(t *testing.T) {
	taskID := workflow.TaskID("task-activation-barrier")
	store := newResumeSchedulerStore(taskID, 3)
	committed := 0
	activationCommitCounts := make([]int, 0, 3)
	prepared := make([]*resumePreparedRun, 3)
	for index := range prepared {
		prepared[index] = &resumePreparedRun{
			onCommit: func() {
				committed++
			},
			onActivate: func() {
				activationCommitCounts = append(activationCommitCounts, committed)
			},
		}
	}
	scheduler := newResumeScheduler(t, store, &resumeSchedulerStarter{
		prepare: func(_ context.Context, _ SchedulerPrepareRunRequest, index int) (PreparedWorkflowRun, error) {
			return prepared[index], nil
		},
	})

	resumed, err := scheduler.ResumeTaskRuns(context.Background(), taskID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(resumed.Runs) != 3 {
		t.Fatalf("resumed runs = %d, want 3", len(resumed.Runs))
	}
	if len(activationCommitCounts) != 3 {
		t.Fatalf("activations = %d, want 3", len(activationCommitCounts))
	}
	for index, commitCount := range activationCommitCounts {
		if commitCount != 3 {
			t.Fatalf("activation %d observed %d commits, want all 3", index, commitCount)
		}
	}
}

func TestSchedulerResumeCancellationAfterAdmissionRetainsScopeUntilCompensationRetries(t *testing.T) {
	taskID := workflow.TaskID("task-retry-resume-compensation")
	store := newResumeSchedulerStore(taskID, 1)
	compensationErr := errors.New("interruption storage unavailable")
	store.interruptErr = compensationErr
	ctx, cancel := context.WithCancel(context.Background())
	store.afterAdmit = cancel
	prepared := &resumePreparedRun{}
	scheduler := newResumeScheduler(t, store, &resumeSchedulerStarter{
		prepare: func(context.Context, SchedulerPrepareRunRequest, int) (PreparedWorkflowRun, error) {
			return prepared, nil
		},
	})

	if _, err := scheduler.ResumeTaskRuns(ctx, taskID); !errors.Is(err, context.Canceled) || !errors.Is(err, compensationErr) {
		t.Fatalf("ResumeTaskRuns error = %v, want cancellation and compensation failure", err)
	}
	if prepared.abortCount() != 0 {
		t.Fatalf("preparation aborts = %d, want retained exact scope", prepared.abortCount())
	}
	if prepared.activationCount() != 0 {
		t.Fatalf("preparation activations = %d, want 0", prepared.activationCount())
	}
	if scheduler.ActiveCount() != 1 {
		t.Fatalf("active count = %d, want retained ownership", scheduler.ActiveCount())
	}
	runs := store.snapshot()
	if len(runs) != 1 || runs[0].Generation != 2 || runs[0].InterruptedAt != nil {
		t.Fatalf("run after failed compensation = %+v, want admitted active generation", runs)
	}

	store.setInterruptError(nil)
	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process compensation retry: %v", err)
	}
	if prepared.abortCount() != 1 {
		t.Fatalf("preparation aborts after retry = %d, want 1", prepared.abortCount())
	}
	if scheduler.ActiveCount() != 0 {
		t.Fatalf("active count after retry = %d, want released ownership", scheduler.ActiveCount())
	}
	runs = store.snapshot()
	if runs[0].InterruptedAt == nil ||
		runs[0].InterruptionReason == nil ||
		*runs[0].InterruptionReason != ReasonSchedulerRuntimeStartFailed {
		t.Fatalf("run after compensation retry = %+v", runs)
	}
}

func TestSchedulerMultiRunResumeCancellationAfterAdmissionRetriesCompensationAsOneBatch(t *testing.T) {
	taskID := workflow.TaskID("task-retry-multi-run-resume-compensation")
	store := newResumeSchedulerStore(taskID, 3)
	compensationErr := errors.New("batch interruption storage unavailable")
	store.interruptErr = compensationErr
	ctx, cancel := context.WithCancel(context.Background())
	store.afterAdmit = cancel
	prepared := []*resumePreparedRun{{}, {}, {}}
	scheduler := newResumeScheduler(t, store, &resumeSchedulerStarter{
		prepare: func(_ context.Context, _ SchedulerPrepareRunRequest, index int) (PreparedWorkflowRun, error) {
			return prepared[index], nil
		},
	})

	if _, err := scheduler.ResumeTaskRuns(ctx, taskID); !errors.Is(err, context.Canceled) || !errors.Is(err, compensationErr) {
		t.Fatalf("ResumeTaskRuns error = %v, want cancellation and batch compensation failure", err)
	}
	for _, run := range store.snapshot() {
		if run.Generation != 2 || run.InterruptedAt != nil {
			t.Fatalf("run after failed batch compensation = %+v, want admitted active generation", run)
		}
	}
	if scheduler.ActiveCount() != 3 {
		t.Fatalf("active count = %d, want every admitted scope retained", scheduler.ActiveCount())
	}
	for index, item := range prepared {
		if item.activationCount() != 0 {
			t.Fatalf("preparation %d activations = %d, want 0", index, item.activationCount())
		}
	}

	store.setInterruptError(nil)
	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process batch compensation retry: %v", err)
	}
	for _, run := range store.snapshot() {
		if run.InterruptedAt == nil ||
			run.InterruptionReason == nil ||
			*run.InterruptionReason != ReasonSchedulerRuntimeStartFailed {
			t.Fatalf("run after batch compensation retry = %+v", run)
		}
	}
	if scheduler.ActiveCount() != 0 {
		t.Fatalf("active count after batch retry = %d, want every scope released", scheduler.ActiveCount())
	}
}

type lostClaimSchedulerStore struct {
	run workflowstore.RunRecord
}

func (s lostClaimSchedulerStore) GetRun(context.Context, workflow.RunID) (workflowstore.RunRecord, error) {
	return s.run, nil
}

func (lostClaimSchedulerStore) ClaimRun(context.Context, workflow.RunID, int64) (workflowstore.RunnableRunRecord, error) {
	return workflowstore.RunnableRunRecord{}, sql.ErrNoRows
}

func (lostClaimSchedulerStore) AdmitRun(context.Context, workflowstore.RunAdmission) (workflowstore.RunnableRunRecord, error) {
	return workflowstore.RunnableRunRecord{}, sql.ErrNoRows
}

func (lostClaimSchedulerStore) ListTaskResumeCandidates(context.Context, workflow.TaskID) ([]workflowstore.RunRecord, error) {
	return nil, nil
}

func (lostClaimSchedulerStore) AdmitTaskResume(context.Context, workflow.TaskID, []workflowstore.RunAdmission) (workflowstore.ResumeTaskRunsResult, error) {
	return workflowstore.ResumeTaskRunsResult{}, nil
}

func (lostClaimSchedulerStore) InterruptRun(context.Context, workflow.RunID, string, string) error {
	return nil
}

func (lostClaimSchedulerStore) InterruptRunGeneration(context.Context, workflow.RunID, int64, string, string) error {
	return nil
}

func (lostClaimSchedulerStore) InterruptExactRuns(context.Context, []workflowstore.ExactRunRef, string, string) ([]workflowstore.RunRecord, error) {
	return nil, nil
}

func (lostClaimSchedulerStore) ReconcileStartedRuns(context.Context, string) ([]workflowstore.RunRecord, error) {
	return nil, nil
}

func (lostClaimSchedulerStore) ReconcileUnstartedRuns(context.Context, string) ([]workflowstore.RunRecord, error) {
	return nil, nil
}

func (lostClaimSchedulerStore) ListWaitingAskRuns(context.Context) ([]workflowstore.RunRecord, error) {
	return nil, nil
}

type discardingSchedulerStarter struct{}

func (discardingSchedulerStarter) PrepareWorkflowRun(_ context.Context, req SchedulerPrepareRunRequest) (PreparedWorkflowRun, error) {
	return discardingPreparedRun{request: req}, nil
}

type discardingPreparedRun struct {
	request SchedulerPrepareRunRequest
}

func (p discardingPreparedRun) Admission() RunAdmission {
	return RunAdmission{}
}

func (p discardingPreparedRun) Commit() error {
	return nil
}

func (discardingPreparedRun) Activate() {}

func (p discardingPreparedRun) Abort(context.Context) error {
	return nil
}

func (p discardingPreparedRun) Compensate(context.Context) error {
	return nil
}

type resumeSchedulerStore struct {
	lostClaimSchedulerStore

	mu           sync.Mutex
	taskID       workflow.TaskID
	runs         []workflowstore.RunRecord
	admitErr     error
	interruptErr error
	afterAdmit   func()
	admitCalls   int
}

func newResumeSchedulerStore(taskID workflow.TaskID, runCount int) *resumeSchedulerStore {
	runs := make([]workflowstore.RunRecord, 0, runCount)
	for index := 0; index < runCount; index++ {
		startedAt := int64(10 + index)
		interruptedAt := int64(20 + index)
		reason := "manual"
		runs = append(runs, workflowstore.RunRecord{
			ID:                 workflow.RunID(string(rune('a'+index)) + "-run"),
			TaskID:             taskID,
			PlacementID:        workflow.PlacementID(string(rune('a'+index)) + "-placement"),
			NodeID:             workflow.NodeID(string(rune('a'+index)) + "-node"),
			Generation:         1,
			StartedAt:          &startedAt,
			InterruptedAt:      &interruptedAt,
			InterruptionReason: &reason,
		})
	}
	return &resumeSchedulerStore{taskID: taskID, runs: runs}
}

func (s *resumeSchedulerStore) GetRun(_ context.Context, runID workflow.RunID) (workflowstore.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.runs {
		if run.ID == runID {
			return run, nil
		}
	}
	return workflowstore.RunRecord{}, sql.ErrNoRows
}

func (s *resumeSchedulerStore) ListTaskResumeCandidates(context.Context, workflow.TaskID) ([]workflowstore.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]workflowstore.RunRecord(nil), s.runs...), nil
}

func (s *resumeSchedulerStore) AdmitTaskResume(
	_ context.Context,
	taskID workflow.TaskID,
	admissions []workflowstore.RunAdmission,
) (workflowstore.ResumeTaskRunsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admitCalls++
	if s.admitErr != nil {
		return workflowstore.ResumeTaskRunsResult{}, s.admitErr
	}
	if taskID != s.taskID || len(admissions) != len(s.runs) {
		return workflowstore.ResumeTaskRunsResult{}, sql.ErrNoRows
	}
	resumed := make([]workflowstore.RunRecord, 0, len(admissions))
	for _, admission := range admissions {
		found := false
		for index := range s.runs {
			run := &s.runs[index]
			if run.ID != admission.RunID || run.Generation != admission.ExpectedGeneration || run.InterruptedAt == nil {
				continue
			}
			run.Generation++
			run.InterruptedAt = nil
			run.InterruptionReason = nil
			if admission.SessionID != nil {
				run.SessionID = *admission.SessionID
			}
			resumed = append(resumed, *run)
			found = true
			break
		}
		if !found {
			return workflowstore.ResumeTaskRunsResult{}, sql.ErrNoRows
		}
	}
	if s.afterAdmit != nil {
		s.afterAdmit()
	}
	return workflowstore.ResumeTaskRunsResult{Runs: resumed}, nil
}

func (s *resumeSchedulerStore) InterruptRunGeneration(
	_ context.Context,
	runID workflow.RunID,
	generation int64,
	reason string,
	_ string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruptErr != nil {
		return s.interruptErr
	}
	for index := range s.runs {
		run := &s.runs[index]
		if run.ID != runID || run.Generation != generation || run.InterruptedAt != nil {
			continue
		}
		interruptedAt := int64(30)
		run.InterruptedAt = &interruptedAt
		run.InterruptionReason = &reason
		return nil
	}
	return sql.ErrNoRows
}

func (s *resumeSchedulerStore) InterruptExactRuns(
	_ context.Context,
	refs []workflowstore.ExactRunRef,
	reason string,
	_ string,
) ([]workflowstore.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruptErr != nil {
		return nil, s.interruptErr
	}
	indexes := make([]int, 0, len(refs))
	for _, ref := range refs {
		found := false
		for index := range s.runs {
			run := &s.runs[index]
			if run.TaskID == ref.TaskID &&
				run.ID == ref.RunID &&
				run.Generation == ref.Generation &&
				run.CompletedAt == nil &&
				run.InterruptedAt == nil {
				indexes = append(indexes, index)
				found = true
				break
			}
		}
		if !found {
			return nil, sql.ErrNoRows
		}
	}
	interrupted := make([]workflowstore.RunRecord, 0, len(indexes))
	for _, index := range indexes {
		run := &s.runs[index]
		interruptedAt := int64(30)
		run.InterruptedAt = &interruptedAt
		run.InterruptionReason = &reason
		interrupted = append(interrupted, *run)
	}
	return interrupted, nil
}

func (s *resumeSchedulerStore) complete(runID workflow.RunID, generation int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.runs {
		run := &s.runs[index]
		if run.ID != runID || run.Generation != generation {
			continue
		}
		completedAt := int64(40)
		run.CompletedAt = &completedAt
		return
	}
}

func (s *resumeSchedulerStore) admitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.admitCalls
}

func (s *resumeSchedulerStore) snapshot() []workflowstore.RunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]workflowstore.RunRecord(nil), s.runs...)
}

func (s *resumeSchedulerStore) setInterruptError(err error) {
	s.mu.Lock()
	s.interruptErr = err
	s.mu.Unlock()
}

type resumeSchedulerStarter struct {
	mu      sync.Mutex
	count   int
	prepare func(context.Context, SchedulerPrepareRunRequest, int) (PreparedWorkflowRun, error)
}

func (s *resumeSchedulerStarter) PrepareWorkflowRun(
	ctx context.Context,
	request SchedulerPrepareRunRequest,
) (PreparedWorkflowRun, error) {
	s.mu.Lock()
	index := s.count
	s.count++
	s.mu.Unlock()
	return s.prepare(ctx, request, index)
}

type resumePreparedRun struct {
	mu          sync.Mutex
	commitErr   error
	onCommit    func()
	onActivate  func()
	activations int
	aborts      int
}

func (*resumePreparedRun) Admission() RunAdmission {
	return RunAdmission{}
}

func (p *resumePreparedRun) Commit() error {
	if p.onCommit != nil {
		p.onCommit()
	}
	return p.commitErr
}

func (p *resumePreparedRun) Activate() {
	p.mu.Lock()
	p.activations++
	p.mu.Unlock()
	if p.onActivate != nil {
		p.onActivate()
	}
}

func (p *resumePreparedRun) Abort(context.Context) error {
	p.mu.Lock()
	p.aborts++
	p.mu.Unlock()
	return nil
}

func (p *resumePreparedRun) Compensate(ctx context.Context) error {
	return p.Abort(ctx)
}

func (p *resumePreparedRun) abortCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.aborts
}

func (p *resumePreparedRun) activationCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activations
}

func newResumeScheduler(t *testing.T, store SchedulerStore, starter SchedulerRuntimeStarter) *SchedulerService {
	t.Helper()
	scheduler, err := NewSchedulerService(store, starter, NewMutationPermit(), SchedulerConfig{Concurrency: 4})
	if err != nil {
		t.Fatalf("NewSchedulerService: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })
	return scheduler
}

func assertResumeStoreInterrupted(t *testing.T, store *resumeSchedulerStore) {
	t.Helper()
	for _, run := range store.snapshot() {
		if run.Generation != 1 || run.InterruptedAt == nil {
			t.Fatalf("run = %+v, want original interrupted generation", run)
		}
	}
}
