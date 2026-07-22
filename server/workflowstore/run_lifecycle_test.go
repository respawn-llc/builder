package workflowstore

import (
	"context"
	"errors"
	"testing"

	"core/server/metadata"
	"core/shared/config"
)

type runLifecycleFixture struct {
	ctx     context.Context
	store   *Store
	binding metadata.Binding
	cfg     config.App
	task    TaskRecord
	started StartTaskResult
}

func newRunLifecycleFixture(t *testing.T) runLifecycleFixture {
	t.Helper()
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	return runLifecycleFixture{
		ctx:     ctx,
		store:   store,
		binding: binding,
		cfg:     cfg,
		task:    task,
		started: startTask(t, ctx, store, task.ID),
	}
}

func (f runLifecycleFixture) claim(t *testing.T) RunnableRunRecord {
	t.Helper()
	return claimRunFixture(t, f.ctx, f.store, f.started.RunID, 0)
}

func (f runLifecycleFixture) attachSession(t *testing.T, claimed RunnableRunRecord) string {
	t.Helper()
	return createAndAttachRunSessionFixture(t, f.ctx, f.store, f.binding, f.cfg, claimed.ID, claimed.Generation)
}

func (f runLifecycleFixture) runs(t *testing.T) []RunRecord {
	t.Helper()
	runs, err := f.store.ListRuns(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	return runs
}

func (f runLifecycleFixture) resume(t *testing.T) []RunRecord {
	t.Helper()
	runs, err := f.store.ResumeTaskRuns(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	return runs.Runs
}

func (f runLifecycleFixture) recordViolation(t *testing.T, req RecordProtocolViolationRequest) RecordProtocolViolationResult {
	t.Helper()
	req.RunID = f.started.RunID
	result, err := f.store.RecordProtocolViolation(f.ctx, req)
	if err != nil {
		t.Fatalf("RecordProtocolViolation: %v", err)
	}
	return result
}

func TestRecordProtocolViolationInterruptsAtCap(t *testing.T) {
	f := newRunLifecycleFixture(t)
	first := f.recordViolation(t, RecordProtocolViolationRequest{Kind: ProtocolViolationInvalidCompletion, MaxCount: 2, Detail: `{"detail":"first"}`})
	if first.Count != 1 || first.Interrupted {
		t.Fatalf("first violation = %+v, want count 1 active", first)
	}
	second := f.recordViolation(t, RecordProtocolViolationRequest{Kind: ProtocolViolationInvalidCompletion, MaxCount: 2, Detail: `{"detail":"second"}`})
	if second.Count != 2 || !second.Interrupted {
		t.Fatalf("second violation = %+v, want count 2 interrupted", second)
	}
	runs := f.runs(t)
	if len(runs) != 1 || runs[0].InvalidCompletions != 2 || runs[0].InterruptedAt == nil || runs[0].InterruptionReason == nil || *runs[0].InterruptionReason != "workflow_protocol_violation_limit" {
		t.Fatalf("run after cap = %+v", runs)
	}
}

func TestCompleteRunRejectsStaleGeneration(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	if _, err := f.store.CompleteRun(f.ctx, CompleteRunRequest{RunID: f.started.RunID, TransitionID: "done", ExpectedGeneration: 0, RequireGeneration: true}); !errors.Is(err, ErrStaleRunGeneration) {
		t.Fatalf("expected stale generation error, got %v", err)
	}
	completeRun(t, f.ctx, f.store, CompleteRunRequest{RunID: f.started.RunID, TransitionID: "done", ExpectedGeneration: claimed.Generation, RequireGeneration: true})
}

// Intentional corruption fixture: delete the transition-edge snapshot to
// verify run context handles legacy/corrupt missing-edge rows safely.

// Intentional corruption fixture: force non-array input JSON that product
// APIs cannot create, then assert the store rejects it at read boundary.

// Intentional snapshot fixture: inject a join binding into the stored edge
// snapshot to verify join-sourced inputs resolve through run context.

func TestResumeTaskRunRequeuesInterruptedRunWithSameSession(t *testing.T) {
	f := newRunLifecycleFixture(t)
	claimed := f.claim(t)
	sessionID := f.attachSession(t, claimed)
	if err := f.store.InterruptRunGeneration(f.ctx, f.started.RunID, claimed.Generation, "manual", `{"reason":"test"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	interrupted := f.runs(t)[0]

	result, err := f.store.ResumeTaskRuns(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(result.ResolvedInterruptedRunProjections) != 1 {
		t.Fatalf("resolved interruption projections = %+v, want one", result.ResolvedInterruptedRunProjections)
	}
	projection := result.ResolvedInterruptedRunProjections[0]
	if projection.RunID != f.started.RunID || projection.SessionID != sessionID || projection.InterruptionReason != "manual" || projection.InterruptionDetailJSON == nil || *projection.InterruptionDetailJSON != `{"reason":"test"}` || interrupted.InterruptedAt == nil || projection.OccurredAtUnixMs != *interrupted.InterruptedAt {
		t.Fatalf("resolved interruption projection = %+v, interrupted run = %+v", projection, interrupted)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("resumed runs = %+v, want one", result.Runs)
	}
	resumed := result.Runs[0]
	if resumed.ID != f.started.RunID || resumed.SessionID != sessionID || resumed.StartedAt != nil || resumed.InterruptedAt != nil || resumed.Generation <= claimed.Generation {
		t.Fatalf("resumed run = %+v, want same run/session requeued with newer generation", resumed)
	}
	runnable, err := f.store.ListRunnableRuns(f.ctx, 10)
	if err != nil {
		t.Fatalf("ListRunnableRuns: %v", err)
	}
	if len(runnable) != 1 || runnable[0].ID != f.started.RunID || runnable[0].SessionID != sessionID {
		t.Fatalf("runnable after resume = %+v, want same run/session", runnable)
	}
	reclaimed := claimRunFixture(t, f.ctx, f.store, f.started.RunID, resumed.Generation)
	if err := f.store.AttachRunSession(f.ctx, f.started.RunID, reclaimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession same session after resume: %v", err)
	}
}
