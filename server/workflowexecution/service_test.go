package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
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

func TestSchedulerQueuedExplicitRunsReturnBeforeStartAndBypassAutomaticCapacity(t *testing.T) {
	firstRun := workflowstore.RunRecord{
		ID:          "run-first-explicit",
		TaskID:      "task-explicit",
		PlacementID: "placement-first-explicit",
		NodeID:      "node-first-explicit",
		Generation:  1,
	}
	secondRun := workflowstore.RunRecord{
		ID:          "run-second-explicit",
		TaskID:      "task-second-explicit",
		PlacementID: "placement-second-explicit",
		NodeID:      "node-second-explicit",
		Generation:  1,
	}
	store := &queuedExplicitSchedulerStore{runs: map[workflow.RunID]workflowstore.RunRecord{
		firstRun.ID:  firstRun,
		secondRun.ID: secondRun,
	}}
	starter := &queuedExplicitSchedulerStarter{}
	scheduler, err := NewSchedulerService(
		store,
		starter,
		NewMutationPermit(),
		SchedulerConfig{Concurrency: 1},
	)
	if err != nil {
		t.Fatalf("NewSchedulerService: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })

	if err := scheduler.StartExplicitRuns(context.Background(), []workflow.RunID{firstRun.ID}); err != nil {
		t.Fatalf("StartExplicitRuns first: %v", err)
	}
	if err := scheduler.QueueExplicitRuns([]workflow.RunID{secondRun.ID}); err != nil {
		t.Fatalf("QueueExplicitRuns second: %v", err)
	}
	if len(starter.requests) != 1 {
		t.Fatalf("starts before scheduler processing = %+v, want only first run", starter.requests)
	}
	if err := scheduler.EnsureTaskQuiescent(context.Background(), secondRun.TaskID); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("EnsureTaskQuiescent queued explicit run error = %v, want ErrTaskExecutionNotQuiescent", err)
	}

	if err := scheduler.Process(context.Background()); err != nil {
		t.Fatalf("Process queued explicit run: %v", err)
	}
	if len(starter.requests) != 2 || starter.requests[1].RunID != secondRun.ID {
		t.Fatalf("starts after scheduler processing = %+v, want queued second run", starter.requests)
	}
	if scheduler.ActiveCount() != 2 {
		t.Fatalf("active runs = %d, want 2 explicit runs above automatic capacity 1", scheduler.ActiveCount())
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

func (lostClaimSchedulerStore) InterruptRun(context.Context, workflow.RunID, string, string) error {
	return nil
}

func (lostClaimSchedulerStore) InterruptRunGeneration(context.Context, workflow.RunID, int64, string, string) error {
	return nil
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

func (discardingSchedulerStarter) StartWorkflowRun(context.Context, SchedulerStartRunRequest) error {
	return nil
}

type queuedExplicitSchedulerStore struct {
	lostClaimSchedulerStore
	runs map[workflow.RunID]workflowstore.RunRecord
}

func (s *queuedExplicitSchedulerStore) GetRun(_ context.Context, runID workflow.RunID) (workflowstore.RunRecord, error) {
	run, ok := s.runs[runID]
	if !ok {
		return workflowstore.RunRecord{}, sql.ErrNoRows
	}
	return run, nil
}

func (s *queuedExplicitSchedulerStore) ClaimRun(_ context.Context, runID workflow.RunID, generation int64) (workflowstore.RunnableRunRecord, error) {
	run, ok := s.runs[runID]
	if !ok || run.Generation != generation {
		return workflowstore.RunnableRunRecord{}, sql.ErrNoRows
	}
	startedAt := int64(1)
	run.StartedAt = &startedAt
	s.runs[runID] = run
	return workflowstore.RunnableRunRecord{RunRecord: run}, nil
}

type queuedExplicitSchedulerStarter struct {
	requests []SchedulerStartRunRequest
}

func (s *queuedExplicitSchedulerStarter) StartWorkflowRun(_ context.Context, req SchedulerStartRunRequest) error {
	s.requests = append(s.requests, req)
	return nil
}
