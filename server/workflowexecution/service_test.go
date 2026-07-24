package workflowexecution

import (
	"context"
	"database/sql"
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
