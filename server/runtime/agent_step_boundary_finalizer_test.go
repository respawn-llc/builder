package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"core/internal/testharness/filemode"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
)

func TestAgentStepBoundaryFinalizerCommitsExactlyOneBoundaryAndProjectsEligibility(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
	finalizer := newAgentStepBoundaryFinalizer(engine)
	finalizer.Open()

	receipt, err := finalizer.Commit("step-1", nil)
	if err != nil || !receipt.Committed {
		t.Fatalf("Commit: receipt=%+v err=%v", receipt, err)
	}
	finalizer.Complete(receipt)

	records := mustReadRuntimeEvents(t, engine)
	boundaries := 0
	for _, record := range records {
		if _, ok := mustSessionEventPayload(record).(session.AgentStepBoundaryRecord); ok {
			boundaries++
		}
	}
	if boundaries != 1 {
		t.Fatalf("boundary records = %d, want 1", boundaries)
	}
	if !engine.compactionRuntimeState().ManualCompactionEligible() {
		t.Fatal("committed boundary did not project manual eligibility")
	}
}

func TestAgentStepBoundaryFinalizerKeepsCommittedFactsWhenObserverFails(t *testing.T) {
	observer := &agentBoundaryTestObserver{}
	store := mustCreateTestSessionAt(t, t.TempDir(), withRuntimeTestPersistenceObserver(observer))
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(), Config{})
	finalizer := newAgentStepBoundaryFinalizer(engine)
	finalizer.Open()
	observer.err = os.ErrPermission

	receipt, err := finalizer.Commit("step-1", nil)
	if err == nil || !receipt.Committed {
		t.Fatalf("Commit: receipt=%+v err=%v, want committed observer failure", receipt, err)
	}
	finalizer.Complete(receipt)
	if !engine.compactionRuntimeState().ManualCompactionEligible() {
		t.Fatal("committed boundary observer failure did not project eligibility")
	}
}

func TestAgentStepBoundaryFinalizerLeavesEligibilityUnchangedWhenAppendIsUncommitted(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
	finalizer := newAgentStepBoundaryFinalizer(engine)
	finalizer.Open()
	filemode.MustBlockEventLogAppends(t, filepath.Join(engine.store.Dir(), "events.jsonl"))

	receipt, err := finalizer.Commit("step-1", nil)
	if err == nil || receipt.Committed {
		t.Fatalf("Commit: receipt=%+v err=%v, want uncommitted failure", receipt, err)
	}
	finalizer.Complete(receipt)
	if engine.compactionRuntimeState().ManualCompactionEligible() {
		t.Fatal("uncommitted boundary projected eligibility")
	}
}

type agentBoundaryTestObserver struct {
	err error
}

func (o *agentBoundaryTestObserver) ObservePersistedStore(context.Context, session.PersistedStoreSnapshot) error {
	return o.err
}

func TestAgentStepBoundaryFinalizerCommitIsIdempotentForAnOpenGeneration(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
	finalizer := newAgentStepBoundaryFinalizer(engine)
	finalizer.Open()

	first, firstErr := finalizer.Commit("step-1", nil)
	second, secondErr := finalizer.Commit("step-1", nil)
	if firstErr != nil || secondErr != nil || first != second {
		t.Fatalf("repeated Commit results = (%+v,%v), (%+v,%v)", first, firstErr, second, secondErr)
	}
	records := mustReadRuntimeEvents(t, engine)
	boundaries := 0
	for _, record := range records {
		if _, ok := mustSessionEventPayload(record).(session.AgentStepBoundaryRecord); ok {
			boundaries++
		}
	}
	if boundaries != 1 {
		t.Fatalf("idempotent boundary records = %d, want 1", boundaries)
	}
}

func TestAgentStepBoundaryFinalizerAtomicallyCommitsPreparedFinalPayloads(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
	finalizer := engine.openAgentStepBoundary("step-atomic")
	finalizer.MarkDispatched()

	message := llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("final answer"),
	}
	if err := engine.steer("step-atomic", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{message},
	)); err != nil {
		t.Fatalf("stage final message: %v", err)
	}
	before := mustReadRuntimeEvents(t, engine)
	for _, record := range before {
		switch mustSessionEventPayload(record).(type) {
		case session.MessageRecord:
			t.Fatal("staged final message was durable before boundary commit")
		}
	}

	receipt, err := finalizer.Commit("step-atomic", nil)
	if err != nil || !receipt.Committed {
		t.Fatalf("commit staged finalization: receipt=%+v err=%v", receipt, err)
	}
	records := mustReadRuntimeEvents(t, engine)
	messageRecords := 0
	boundaryRecords := 0
	for _, record := range records {
		payload := mustSessionEventPayload(record)
		switch payload.(type) {
		case session.MessageRecord:
			messageRecords++
		case session.AgentStepBoundaryRecord:
			boundaryRecords++
		}
	}
	if messageRecords != 1 || boundaryRecords != 1 {
		t.Fatalf("atomic finalization facts = messages:%d boundaries:%d, want 1/1", messageRecords, boundaryRecords)
	}
}

func TestAgentStepBoundaryFinalizerRollsBackPreparedFinalPayloadsWhenAppendIsUncommitted(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
	finalizer := engine.openAgentStepBoundary("step-atomic-failure")
	finalizer.MarkDispatched()
	message := llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   textutil.Value(llm.MessagePhaseFinal),
		Content: textutil.Value("final answer"),
	}
	if err := engine.steer("step-atomic-failure", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{message},
	)); err != nil {
		t.Fatalf("stage final message: %v", err)
	}
	if len(engine.transcriptRuntimeState().SnapshotMessages()) != 1 {
		t.Fatal("staged final message was not projected transiently")
	}
	blocker := filemode.MustBlockEventLogAppends(t, filepath.Join(engine.store.Dir(), "events.jsonl"))

	receipt, err := finalizer.Commit("step-atomic-failure", nil)
	if err == nil || receipt.Committed {
		t.Fatalf("uncommitted finalization: receipt=%+v err=%v", receipt, err)
	}
	if messages := engine.transcriptRuntimeState().SnapshotMessages(); len(messages) != 0 {
		t.Fatalf("rolled-back transient messages = %d, want 0", len(messages))
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event log before durable assertion: %v", err)
	}
	for _, record := range mustReadRuntimeEvents(t, engine) {
		switch mustSessionEventPayload(record).(type) {
		case session.MessageRecord, session.AgentStepBoundaryRecord:
			t.Fatal("uncommitted finalization left durable final facts")
		}
	}
}

func mustReadRuntimeEvents(t *testing.T, engine *Engine) []session.EventRecord {
	t.Helper()
	records, err := engine.eventLog.ReadRecentRecords(1000)
	if err != nil {
		t.Fatalf("read runtime events: %v", err)
	}
	return records.Records
}

func newTestToolRegistry() *tools.Registry {
	return tools.NewRegistry()
}
