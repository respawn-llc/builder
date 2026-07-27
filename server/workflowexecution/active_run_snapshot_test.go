package workflowexecution

import (
	"context"
	"sync"
	"testing"
	"time"

	"core/server/workflow"
	"core/server/workflowstore"
)

func TestSchedulerActiveRunSnapshotTracksStartingRunningRetirementAndABA(t *testing.T) {
	run := workflowstore.RunRecord{
		ID:          "run-observed",
		TaskID:      "task-observed",
		PlacementID: "placement-observed",
		NodeID:      "node-observed",
		Generation:  4,
	}
	starter := newBlockingActivationStarter()
	scheduler, err := NewSchedulerService(
		observationSchedulerStore{run: run},
		starter,
		NewMutationPermit(),
		SchedulerConfig{Concurrency: 1},
	)
	if err != nil {
		t.Fatalf("NewSchedulerService: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })

	startDone := make(chan error, 1)
	go func() {
		startDone <- scheduler.StartExplicitRuns(context.Background(), []workflow.RunID{run.ID})
	}()
	awaitBlockingActivation(t, starter)

	starting := scheduler.ActiveRunSnapshot()
	requireSchedulerSnapshot(t, starting, SchedulerActiveRunPhaseStarting, run, run.Generation+1)

	var readers sync.WaitGroup
	readerErrs := make(chan string, 32)
	for range 32 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 64 {
				got := scheduler.ActiveRunSnapshot()
				if len(got.ActiveRuns) != 1 ||
					got.ActiveRuns[0].RunID != run.ID ||
					got.ActiveRuns[0].TaskID != run.TaskID ||
					got.ActiveRuns[0].Generation != run.Generation+1 ||
					got.ActiveRuns[0].Phase != SchedulerActiveRunPhaseStarting {
					readerErrs <- "concurrent active-run observation did not retain the blocked starting run"
					return
				}
			}
		}()
	}
	readers.Wait()
	close(readerErrs)
	for err := range readerErrs {
		t.Error(err)
	}

	starting.ActiveRuns[0].TaskID = "mutated"
	unchanged := scheduler.ActiveRunSnapshot()
	requireSchedulerSnapshot(t, unchanged, SchedulerActiveRunPhaseStarting, run, run.Generation+1)

	close(starter.release)
	if err := <-startDone; err != nil {
		t.Fatalf("StartExplicitRuns: %v", err)
	}
	running := scheduler.ActiveRunSnapshot()
	requireSchedulerSnapshot(t, running, SchedulerActiveRunPhaseRunning, run, run.Generation+1)
	if running.Revision <= starting.Revision {
		t.Fatalf("running revision = %d, want > starting revision %d", running.Revision, starting.Revision)
	}

	scheduler.RuntimeFinished(run.ID, run.Generation+1)
	retired := scheduler.ActiveRunSnapshot()
	if len(retired.ActiveRuns) != 0 {
		t.Fatalf("retired active runs = %+v, want empty", retired.ActiveRuns)
	}
	if retired.Revision <= running.Revision {
		t.Fatalf("retired revision = %d, want > running revision %d", retired.Revision, running.Revision)
	}

	if err := scheduler.StartExplicitRuns(context.Background(), []workflow.RunID{run.ID}); err != nil {
		t.Fatalf("StartExplicitRuns ABA: %v", err)
	}
	aba := scheduler.ActiveRunSnapshot()
	requireSchedulerSnapshot(t, aba, SchedulerActiveRunPhaseRunning, run, run.Generation+1)
	if aba.Revision <= retired.Revision {
		t.Fatalf("ABA revision = %d, want > retired revision %d", aba.Revision, retired.Revision)
	}
	if aba.ActiveRuns[0] != running.ActiveRuns[0] {
		t.Fatalf("ABA observation = %+v, want same active observation as prior running %+v", aba.ActiveRuns[0], running.ActiveRuns[0])
	}
}

func requireSchedulerSnapshot(
	t *testing.T,
	snapshot SchedulerActiveRunSnapshot,
	phase SchedulerActiveRunPhase,
	run workflowstore.RunRecord,
	generation int64,
) {
	t.Helper()
	if snapshot.Revision == 0 {
		t.Fatal("snapshot revision is zero after an observed scheduler mutation")
	}
	if len(snapshot.ActiveRuns) != 1 {
		t.Fatalf("active runs = %+v, want exactly one", snapshot.ActiveRuns)
	}
	active := snapshot.ActiveRuns[0]
	if active.RunID != run.ID ||
		active.TaskID != run.TaskID ||
		active.PlacementID != run.PlacementID ||
		active.NodeID != run.NodeID ||
		active.Generation != generation ||
		active.Phase != phase {
		t.Fatalf("active run = %+v, want run=%s task=%s placement=%s node=%s generation=%d phase=%v",
			active, run.ID, run.TaskID, run.PlacementID, run.NodeID, generation, phase)
	}
}

type observationSchedulerStore struct {
	run workflowstore.RunRecord
}

func (s observationSchedulerStore) GetRun(context.Context, workflow.RunID) (workflowstore.RunRecord, error) {
	return s.run, nil
}

func (s observationSchedulerStore) AdmitRun(_ context.Context, admission workflowstore.RunAdmission) (workflowstore.RunnableRunRecord, error) {
	run := s.run
	run.Generation = admission.ExpectedGeneration + 1
	return workflowstore.RunnableRunRecord{RunRecord: run}, nil
}

func (observationSchedulerStore) InterruptRun(context.Context, workflow.RunID, string, string) error {
	return nil
}

func (observationSchedulerStore) InterruptRunGeneration(context.Context, workflow.RunID, int64, string, string) error {
	return nil
}

func (observationSchedulerStore) ReconcileStartedRuns(context.Context, string) ([]workflowstore.RunRecord, error) {
	return nil, nil
}

func (observationSchedulerStore) ReconcileUnstartedRuns(context.Context, string) ([]workflowstore.RunRecord, error) {
	return nil, nil
}

func (observationSchedulerStore) ListWaitingAskRuns(context.Context) ([]workflowstore.RunRecord, error) {
	return nil, nil
}

type blockingActivationStarter struct {
	entered chan struct{}
	release chan struct{}
}

func newBlockingActivationStarter() *blockingActivationStarter {
	return &blockingActivationStarter{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (s *blockingActivationStarter) PrepareWorkflowRun(context.Context, SchedulerPrepareRunRequest) (PreparedWorkflowRun, error) {
	return blockingActivationPreparedRun{entered: s.entered, release: s.release}, nil
}

type blockingActivationPreparedRun struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (blockingActivationPreparedRun) Admission() RunAdmission {
	return RunAdmission{}
}

func (blockingActivationPreparedRun) Commit() error {
	return nil
}

func (p blockingActivationPreparedRun) Activate() {
	p.entered <- struct{}{}
	<-p.release
}

func (blockingActivationPreparedRun) Abort(context.Context) error {
	return nil
}

func awaitBlockingActivation(t *testing.T, starter *blockingActivationStarter) {
	t.Helper()
	select {
	case <-starter.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scheduler to enter the blocking runtime-start call")
	}
}
