package runtime

import (
	"context"
	"encoding/json"
	"errors"
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

func TestRequestPreparationFailureDoesNotOpenAgentStepGeneration(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
	finalizer := engine.openAgentStepBoundary("step-preparation-failure")
	finalizer.ArmGeneration()
	expected := errors.New("request preparation failed")

	_, err := engine.generateWithMissingToolOutputRepairAtDispatch(
		context.Background(),
		"step-preparation-failure",
		func() (llm.Request, error) {
			return llm.Request{}, expected
		},
		nil,
		nil,
		nil,
		finalizer.MarkDispatched,
	)
	if !errors.Is(err, expected) {
		t.Fatalf("generation error = %v, want request preparation error", err)
	}
	if finalizer.Dispatched() {
		t.Fatal("request preparation failure opened a provider generation")
	}
	if engine.compactionRuntimeState().ManualCompactionEligible() {
		t.Fatal("request preparation failure projected manual eligibility")
	}
	if err := finalizer.Abort(err); err == nil {
		t.Fatal("aborting armed pre-dispatch generation returned nil")
	}
}

func TestRepeatedProviderDispatchKeepsOneManualBoundaryGeneration(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
	finalizer := engine.openAgentStepBoundary("step-retry")
	finalizer.MarkDispatched()
	coordinator := engine.compactionRuntimeState().manualBoundaryCoordinator()

	coordinator.mu.Lock()
	if coordinator.current == nil {
		coordinator.mu.Unlock()
		t.Fatal("first provider dispatch did not open a manual boundary generation")
	}
	firstGenerationID := coordinator.current.id
	coordinator.mu.Unlock()

	entry, err := coordinator.enqueueForGenerationOrdered(context.Background(), compactionInstructionsInput{}, nil, nil)
	if err != nil {
		t.Fatalf("enqueue pending compaction: %v", err)
	}
	finalizer.MarkDispatched()

	coordinator.mu.Lock()
	current := coordinator.current
	coordinator.mu.Unlock()
	if current == nil || current.id != firstGenerationID {
		t.Fatalf("retry dispatch replaced generation %d with %+v", firstGenerationID, current)
	}
	detached := coordinator.sealAndTake()
	if len(detached) != 1 || detached[0] != entry {
		t.Fatalf("detached retry-generation entries = %+v, want original pending entry", detached)
	}
	entry.complete(manualCompactionResult{err: errors.New("test cleanup")})
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

func TestAgentStepBoundaryFinalizerDoesNotProjectPreparedFinalPayloadsWhenAppendIsUncommitted(t *testing.T) {
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
	if len(engine.transcriptRuntimeState().SnapshotMessages()) != 0 {
		t.Fatal("staged final message was projected before commit")
	}
	blocker := filemode.MustBlockEventLogAppends(t, filepath.Join(engine.store.Dir(), "events.jsonl"))

	receipt, err := finalizer.Commit("step-atomic-failure", nil)
	if err == nil || receipt.Committed {
		t.Fatalf("uncommitted finalization: receipt=%+v err=%v", receipt, err)
	}
	if messages := engine.transcriptRuntimeState().SnapshotMessages(); len(messages) != 0 {
		t.Fatalf("uncommitted final messages = %d, want 0", len(messages))
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

func TestUncommittedToolCompletionKeepsLiveToolAbortable(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
	if err := engine.steer("step-tool", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{
			Role:      llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{ID: "call-tool", Name: "exec_command"}},
		}},
	)); err != nil {
		t.Fatalf("persist authoritative tool call: %v", err)
	}
	if err := engine.transcriptRuntimeState().RecordLiveToolStart("step-tool", llm.ToolCall{
		ID:   "call-tool",
		Name: "exec_command",
	}); err != nil {
		t.Fatalf("record live tool start: %v", err)
	}
	blocker := filemode.MustBlockEventLogAppends(t, filepath.Join(engine.store.Dir(), "events.jsonl"))

	err := engine.steer("step-tool", steerToolCompletionIntent(tools.Result{
		CallID: "call-tool",
		Name:   "exec_command",
		Output: json.RawMessage(`{"ok":true}`),
	}))
	if err == nil {
		t.Fatal("uncommitted tool completion unexpectedly succeeded")
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event log: %v", err)
	}
	if _, ok := engine.transcriptRuntimeState().ToolCallSnapshot("call-tool"); !ok {
		t.Fatal("uncommitted tool completion removed the live tool before durable commit")
	}
}

func TestAgentStepBoundaryFinalizerRejectsUncommittedFinalToolAndLocalPayloads(t *testing.T) {
	tests := []struct {
		name  string
		steer func(*Engine, string) error
	}{
		{
			name: "tool result message",
			steer: func(engine *Engine, stepID string) error {
				return engine.steer(stepID, steerFinalMessageIntent(llm.Message{
					Role:       llm.RoleTool,
					Content:    textutil.Value("tool output"),
					ToolCallID: textutil.Value("call-tool"),
				}))
			},
		},
		{
			name: "reviewer local outcome",
			steer: func(engine *Engine, stepID string) error {
				return engine.steer(stepID, steerFinalLocalEntryIntent(storedLocalEntry{
					Role: "reviewer_status",
					Text: "no suggestions",
				}))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
			finalizer := engine.openAgentStepBoundary("step-final-payload")
			finalizer.MarkDispatched()
			if err := test.steer(engine, "step-final-payload"); err != nil {
				t.Fatalf("stage final payload: %v", err)
			}
			blocker := filemode.MustBlockEventLogAppends(t, filepath.Join(engine.store.Dir(), "events.jsonl"))
			receipt, err := finalizer.Commit("step-final-payload", nil)
			if err == nil || receipt.Committed {
				t.Fatalf("uncommitted final payload: receipt=%+v err=%v", receipt, err)
			}
			if err := blocker.Restore(); err != nil {
				t.Fatalf("restore event log: %v", err)
			}
			for _, record := range mustReadRuntimeEvents(t, engine) {
				switch mustSessionEventPayload(record).(type) {
				case session.MessageRecord, session.LocalEntryRecord, session.AgentStepBoundaryRecord:
					t.Fatal("uncommitted final payload left durable facts")
				}
			}
		})
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
