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
	"core/shared/toolspec"
	"core/shared/transcript"
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
		t.Fatal("staged final message was projected before durable commit")
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

func TestAgentStepBoundaryFinalizerAtomicallyRejectsToolAndReviewerPayloads(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
	finalizer := engine.openAgentStepBoundary("step-atomic-tool-reviewer")
	finalizer.MarkDispatched()

	call := llm.ToolCall{
		ID:    "call-atomic",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	}
	if err := engine.steer("step-atomic-tool-reviewer", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}},
	)); err != nil {
		t.Fatalf("stage assistant tool call: %v", err)
	}
	liveCall := call
	liveCall.Presentation = transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
		ToolName: string(toolspec.ToolExecCommand),
	})
	if err := engine.steer("step-atomic-tool-reviewer", steerEventIntent(Event{
		Kind:     EventToolCallStarted,
		ToolCall: &liveCall,
	})); err != nil {
		t.Fatalf("stage live tool call: %v", err)
	}
	result := tools.Result{
		CallID: call.ID,
		Name:   toolspec.ToolExecCommand,
		Output: json.RawMessage(`{"output":"ok","exit_code":0}`),
	}
	if err := engine.steer("step-atomic-tool-reviewer", steerToolCompletionIntent(result)); err != nil {
		t.Fatalf("stage tool completion: %v", err)
	}
	if err := engine.steer("step-atomic-tool-reviewer", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value(call.ID),
			Name:       textutil.Value(string(call.Name)),
			Content:    textutil.Value(string(result.Output)),
		}},
	)); err != nil {
		t.Fatalf("stage tool result message: %v", err)
	}
	if err := engine.steer("step-atomic-tool-reviewer", steerLocalEntryIntent(storedLocalEntry{
		Role: "reviewer_suggestions",
		Text: "verify atomic output",
	})); err != nil {
		t.Fatalf("stage reviewer entry: %v", err)
	}

	blocker := filemode.MustBlockEventLogAppends(t, filepath.Join(engine.store.Dir(), "events.jsonl"))
	receipt, err := finalizer.Commit("step-atomic-tool-reviewer", nil)
	if err == nil || receipt.Committed {
		t.Fatalf("uncommitted tool/reviewer finalization: receipt=%+v err=%v", receipt, err)
	}
	if got := engine.transcriptRuntimeState().ToolCompletionCount(); got != 0 {
		t.Fatalf("uncommitted tool completions = %d, want 0", got)
	}
	if got := len(engine.transcriptRuntimeState().SnapshotMessages()); got != 0 {
		t.Fatalf("uncommitted messages = %d, want 0", got)
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event log before durable assertion: %v", err)
	}
	for _, record := range mustReadRuntimeEvents(t, engine) {
		switch mustSessionEventPayload(record).(type) {
		case session.MessageRecord, session.ToolCompletionRecord, session.LocalEntryRecord, session.AgentStepBoundaryRecord:
			t.Fatal("uncommitted finalization left a durable tool/reviewer fact")
		}
	}
}

func TestAgentStepBoundaryFinalizerDoesNotDispatchWhenRequestPreparationFails(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(), Config{})
	finalizer := engine.openAgentStepBoundary("step-pre-dispatch-failure")
	finalizer.ArmGeneration()
	preparationErr := errors.New("request preparation failed")
	_, err := engine.generateWithMissingToolOutputRepairAtDispatch(
		context.Background(),
		"step-pre-dispatch-failure",
		func() (llm.Request, error) {
			return llm.Request{}, preparationErr
		},
		nil,
		nil,
		nil,
		finalizer.MarkDispatched,
	)
	if !errors.Is(err, preparationErr) {
		t.Fatalf("generation error = %v, want preparation error", err)
	}
	if finalizer.Dispatched() {
		t.Fatal("request preparation failure opened a dispatched generation")
	}
	if err := finalizer.Abort(err); err == nil {
		t.Fatal("aborting un-dispatched generation returned nil")
	}
	if engine.compactionRuntimeState().ManualCompactionEligible() {
		t.Fatal("pre-dispatch failure made manual compaction eligible")
	}
	for _, record := range mustReadRuntimeEvents(t, engine) {
		if _, ok := mustSessionEventPayload(record).(session.AgentStepBoundaryRecord); ok {
			t.Fatal("pre-dispatch failure persisted an agent step boundary")
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
