package session

import (
	"testing"
	"time"
)

func collectStoreEvents(t *testing.T, store *Store) []Event {
	t.Helper()
	var events []Event
	if err := store.WalkEvents(func(evt Event) error {
		events = append(events, evt)
		return nil
	}); err != nil {
		t.Fatalf("collect events: %v", err)
	}
	return events
}

func TestProjectRunsReconstructsDurableHistory(t *testing.T) {
	store := newSessionTestStore(t)

	run1Start := time.Now().UTC().Add(-2 * time.Minute)
	run1Finish := run1Start.Add(30 * time.Second)
	if _, err := store.AppendRunStarted(RunRecord{RunID: "run-1", StepID: "step-1", StartedAt: run1Start}); err != nil {
		t.Fatalf("append run-1 start: %v", err)
	}
	if _, err := store.AppendRunFinished(RunRecord{RunID: "run-1", StepID: "step-1", Status: RunStatusCompleted, StartedAt: run1Start, FinishedAt: run1Finish}); err != nil {
		t.Fatalf("append run-1 finish: %v", err)
	}

	run2Start := time.Now().UTC().Add(-time.Minute)
	run2Finish := run2Start.Add(10 * time.Second)
	if _, err := store.AppendRunStarted(RunRecord{RunID: "run-2", StepID: "step-2", StartedAt: run2Start}); err != nil {
		t.Fatalf("append run-2 start: %v", err)
	}
	if _, err := store.AppendRunFinished(RunRecord{RunID: "run-2", StepID: "step-2", Status: RunStatusInterrupted, StartedAt: run2Start, FinishedAt: run2Finish}); err != nil {
		t.Fatalf("append run-2 finish: %v", err)
	}

	reopened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	runs := ProjectRuns(collectStoreEvents(t, reopened))
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %+v", runs)
	}
	if runs[0].RunID != "run-1" || runs[0].Status != RunStatusCompleted || !runs[0].StartedAt.Equal(run1Start) || !runs[0].FinishedAt.Equal(run1Finish) {
		t.Fatalf("unexpected first run: %+v", runs[0])
	}
	if runs[1].RunID != "run-2" || runs[1].Status != RunStatusInterrupted || !runs[1].StartedAt.Equal(run2Start) || !runs[1].FinishedAt.Equal(run2Finish) {
		t.Fatalf("unexpected second run: %+v", runs[1])
	}

	latest, err := reopened.LatestRun()
	if err != nil {
		t.Fatalf("latest run after reopen: %v", err)
	}
	if latest == nil || latest.RunID != "run-2" || latest.Status != RunStatusInterrupted {
		t.Fatalf("unexpected latest run after reopen: %+v", latest)
	}
}

func TestStoreLatestRunReturnsNewestDurableRun(t *testing.T) {
	store := newSessionTestStore(t)

	latest, err := store.LatestRun()
	if err != nil {
		t.Fatalf("latest run without history: %v", err)
	}
	if latest != nil {
		t.Fatalf("expected no latest run, got %+v", latest)
	}

	startedAt := time.Now().UTC().Add(-time.Minute)
	finishedAt := startedAt.Add(15 * time.Second)
	if _, err := store.AppendRunStarted(RunRecord{RunID: "run-1", StepID: "step-1", StartedAt: startedAt}); err != nil {
		t.Fatalf("append run start: %v", err)
	}
	if _, err := store.AppendRunFinished(RunRecord{RunID: "run-1", StepID: "step-1", Status: RunStatusFailed, StartedAt: startedAt, FinishedAt: finishedAt}); err != nil {
		t.Fatalf("append run finish: %v", err)
	}

	latest, err = store.LatestRun()
	if err != nil {
		t.Fatalf("latest run: %v", err)
	}
	if latest == nil || latest.RunID != "run-1" || latest.Status != RunStatusFailed {
		t.Fatalf("unexpected latest run: %+v", latest)
	}
}

func TestLatestRunTreatsStartedWithoutFinishAsRunningAfterReopen(t *testing.T) {
	store := newSessionTestStore(t)

	startedAt := time.Now().UTC().Add(-time.Minute)
	if _, err := store.AppendRunStarted(RunRecord{RunID: "run-1", StepID: "step-1", StartedAt: startedAt}); err != nil {
		t.Fatalf("append run start: %v", err)
	}

	reopened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	latest, err := reopened.LatestRun()
	if err != nil {
		t.Fatalf("latest run: %v", err)
	}
	if latest == nil || latest.RunID != "run-1" || latest.Status != RunStatusRunning || !latest.StartedAt.Equal(startedAt) || !latest.FinishedAt.IsZero() {
		t.Fatalf("unexpected running run reconstruction: %+v", latest)
	}
}

func TestStoreAppendRunFinishedRequiresTerminalStatus(t *testing.T) {
	store := newSessionTestStore(t)

	if _, err := store.AppendRunFinished(RunRecord{RunID: "run-1", StepID: "step-1", Status: RunStatusRunning}); err == nil {
		t.Fatal("expected non-terminal run_finished status to be rejected")
	}
	if _, err := store.AppendRunFinished(RunRecord{RunID: "run-2", StepID: "step-2"}); err == nil {
		t.Fatal("expected empty run_finished status to be rejected")
	}
}
