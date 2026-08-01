package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestReopenedSessionAfterSuccessfulTriggerHandoffRequeuesPendingHandoff(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	appendAgentStepBoundaryForEligibilityTest(t, store, "handoff-compaction-step")
	engine := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}

	persistSuccessfulTriggerHandoff(t, engine, "handoff-call")

	resumedClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("resumed"),
			},
			Usage: llm.Usage{InputTokens: 300, WindowTokens: 2_000},
		},
	}}
	restored := mustNewHandoffTestEngine(t, mustOpenTestSession(t, store.Dir()), resumedClient, Config{})
	if pending := restored.handoffRuntimeState().RequestSnapshot(); pending == nil {
		t.Fatal("restored trigger-handoff request is absent")
	}

	if _, err := restored.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit after restored handoff: %v", err)
	}
	if generation := restored.compactionRuntimeState().Count(); generation != 1 {
		t.Fatalf("restored handoff compaction generation = %d, want 1", generation)
	}

	if futureMessages := countHandoffFutureMessageRecords(t, store); futureMessages != 1 {
		t.Fatalf("restored handoff future-message records = %d, want 1", futureMessages)
	}
}

func TestReopenedSessionAfterTriggerHandoffDoesNotRequeueWhenAnyCompactionAlreadyHappened(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	handoffCall := persistSuccessfulTriggerHandoff(t, engine, "satisfied-handoff-call")
	receipt, err := newCompactionPersistence(engine).replaceHistory(
		"compact",
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("summary"),
		}}),
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("persist compaction after trigger-handoff: receipt=%+v error=%v", receipt, err)
	}

	restored := mustNewHandoffTestEngine(t, mustOpenTestSession(t, store.Dir()), &fakeClient{}, Config{})
	if pending := restored.handoffRuntimeState().RequestSnapshot(); pending != nil {
		t.Fatalf("reopened session requeued satisfied handoff: %+v", pending)
	}
	if generation := restored.compactionRuntimeState().Count(); generation != 1 {
		t.Fatalf("reopened compaction generation = %d, want 1", generation)
	}
	assertNoTriggerHandoffProtocolItems(
		t,
		"reopened active items",
		restored.transcriptRuntimeState().SnapshotItems(),
		handoffCall,
	)
}

func TestReopenedSessionAfterFailedTriggerHandoffDoesNotRequeuePendingHandoff(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	handoffCall := llm.ToolCall{
		ID:   "failed-handoff-call",
		Name: string(toolspec.ToolTriggerHandoff),
		Input: mustJSON(map[string]any{
			"future_agent_message": "continue",
		}),
	}
	if err := engine.steer("handoff", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{
			Role:      llm.RoleAssistant,
			Phase:     textutil.Value(llm.MessagePhaseCommentary),
			Content:   textutil.Value("handoff"),
			ToolCalls: []llm.ToolCall{handoffCall},
		}},
	)); err != nil {
		t.Fatalf("persist trigger-handoff call: %v", err)
	}
	if err := engine.steer("handoff", steerToolCompletionIntent(tools.Result{
		CallID:  handoffCall.ID,
		Name:    toolspec.ToolTriggerHandoff,
		IsError: true,
		Output:  mustJSON(map[string]any{"failure": true}),
	})); err != nil {
		t.Fatalf("persist failed trigger-handoff completion: %v", err)
	}

	restored := mustNewHandoffTestEngine(t, mustOpenTestSession(t, store.Dir()), &fakeClient{}, Config{})
	if pending := restored.handoffRuntimeState().RequestSnapshot(); pending != nil {
		t.Fatalf("reopened session requeued failed handoff: %+v", pending)
	}
	window, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if readErr != nil {
		t.Fatalf("read bounded handoff records: %v", readErr)
	}
	for _, record := range window.Records {
		completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
		if ok && completion.CallID == handoffCall.ID && completion.IsError {
			return
		}
	}
	t.Fatalf("bounded handoff records contain no failed typed completion: %+v", window.Records)
}

func TestManualCompactionClearsQueuedTriggerHandoff(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	appendAgentStepBoundaryForEligibilityTest(t, store, "manual-handoff-step")
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("complete"),
			},
			Usage: llm.Usage{InputTokens: 300, WindowTokens: 2_000},
		},
	}}
	engine := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	engine.compactionRuntimeState().SetSoonReminderIssued(true)

	handoffCall := llm.ToolCall{
		ID:   "manual-compaction-handoff-call",
		Name: string(toolspec.ToolTriggerHandoff),
	}
	if _, _, err := engine.TriggerHandoff(
		context.Background(),
		"step",
		handoffCall,
		"summarize",
		"continue",
	); err != nil {
		t.Fatalf("queue trigger-handoff: %v", err)
	}
	if pending := engine.handoffRuntimeState().RequestSnapshot(); pending == nil {
		t.Fatal("queued trigger-handoff request is absent before manual compaction")
	}

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("manual compact: %v", err)
	}
	if generation := engine.CompactionCount(); generation != 1 {
		t.Fatalf("manual compaction generation = %d, want 1", generation)
	}
	if pending := engine.handoffRuntimeState().RequestSnapshot(); pending != nil {
		t.Fatalf("manual compaction retained queued trigger-handoff request: %+v", pending)
	}

	if _, err := engine.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit after manual compaction: %v", err)
	}
	if generation := engine.CompactionCount(); generation != 1 {
		t.Fatalf("next turn ran queued trigger-handoff compaction: generation = %d, want 1", generation)
	}
	if len(client.calls) == 0 {
		t.Fatal("next turn did not produce a model request")
	}
	assertNoTriggerHandoffProtocolItems(
		t,
		"next request",
		client.calls[len(client.calls)-1].Items,
		handoffCall,
	)
}

func TestReopenedSessionAfterTriggerHandoffFutureMessageAppendFailureRetriesWithoutRecompaction(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var (
		blockFutureAppend bool
		blocker           *testEventLogAppendBlocker
		blockErr          error
		blockerRestored   bool
	)
	t.Cleanup(func() {
		if blocker != nil && !blockerRestored {
			if err := blocker.Restore(); err != nil {
				t.Errorf("restore blocked event log: %v", err)
			}
		}
	})
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
		Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}}
	engine := mustNewHandoffTestEngine(t, store, client, Config{
		OnEvent: func(event Event) {
			if !blockFutureAppend ||
				event.Kind != EventConversationUpdated ||
				event.CommittedTranscriptChanged {
				return
			}
			blockFutureAppend = false
			blocker, blockErr = blockTestEventLogAppends(store)
		},
	})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	persistSuccessfulTriggerHandoff(t, engine, "future-message-retry-handoff-call")
	engine.handoffRuntimeState().QueueRequest("summarize", "continue")

	blockFutureAppend = true
	if _, err := engine.applyPendingHandoffIfNeeded(context.Background(), "step"); err == nil {
		t.Fatal("handoff future-message append unexpectedly succeeded")
	}
	if blockErr != nil || blocker == nil {
		t.Fatalf("block future-message append: blocker=%v error=%v", blocker, blockErr)
	}
	if generation := engine.CompactionCount(); generation != 1 {
		t.Fatalf("committed handoff compaction generation = %d, want 1", generation)
	}
	if pending := engine.handoffRuntimeState().RequestSnapshot(); pending != nil {
		t.Fatalf("committed handoff compaction retained pending request: %+v", pending)
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event log: %v", err)
	}
	blockerRestored = true

	resumedClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("complete"),
		},
		Usage: llm.Usage{InputTokens: 300, WindowTokens: 2_000},
	}}}
	restored := mustNewHandoffTestEngine(t, mustOpenTestSession(t, store.Dir()), resumedClient, Config{})
	if pending := restored.handoffRuntimeState().RequestSnapshot(); pending != nil {
		t.Fatalf("reopened session requeued completed handoff: %+v", pending)
	}
	if _, err := restored.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if generation := restored.CompactionCount(); generation != 1 {
		t.Fatalf("future-message retry re-ran handoff compaction: generation = %d, want 1", generation)
	}
	if len(resumedClient.calls) == 0 {
		t.Fatal("future-message retry did not produce a model request")
	}
	if futureMessages := countHandoffFutureMessageRecords(t, store); futureMessages != 1 {
		t.Fatalf("reopened retry future-message records = %d, want 1", futureMessages)
	}
}

func TestPendingHandoffFutureMessageConsumesCommittedObserverFailure(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("handoff future-message observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	engine.handoffRuntimeState().QueueFutureMessage("continue")
	gate.FailNext(observerErr)

	applied, err := engine.applyPendingHandoffIfNeeded(context.Background(), "step")
	if applied || !errors.Is(err, observerErr) {
		t.Fatalf("future-message append outcome = applied:%v error:%v", applied, err)
	}
	if futureMessages := countHandoffFutureMessageRecords(t, store); futureMessages != 1 {
		t.Fatalf("committed handoff future-message records = %d, want 1", futureMessages)
	}

	applied, err = engine.applyPendingHandoffIfNeeded(context.Background(), "step")
	if applied || err != nil {
		t.Fatalf("second future-message append outcome = applied:%v error:%v", applied, err)
	}
	if futureMessages := countHandoffFutureMessageRecords(t, store); futureMessages != 1 {
		t.Fatalf("second future-message append changed durable record count to %d", futureMessages)
	}
}

func TestReopenedSessionAfterTriggerHandoffUsesRotatedRequestSessionAndOmitsLingeringCallOutput(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	sessionID := store.Meta().SessionID
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
		Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}}
	engine := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	persistSuccessfulTriggerHandoff(t, engine, "reopened-handoff-call")
	engine.handoffRuntimeState().QueueRequest("summarize", "continue")

	compacted, err := engine.applyPendingHandoffIfNeeded(context.Background(), "handoff")
	if err != nil || !compacted {
		t.Fatalf("apply pending handoff = compacted:%v error:%v", compacted, err)
	}
	if generation := engine.CompactionCount(); generation != 1 {
		t.Fatalf("handoff compaction generation = %d, want 1", generation)
	}

	resumedClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("complete"),
		},
		Usage: llm.Usage{InputTokens: 300, WindowTokens: 2_000},
	}}}
	restored := mustNewHandoffTestEngine(t, mustOpenTestSession(t, store.Dir()), resumedClient, Config{})
	if restored.SessionID() != sessionID {
		t.Fatalf("reopened handoff session identity = %q, want %q", restored.SessionID(), sessionID)
	}
	if pending := restored.handoffRuntimeState().RequestSnapshot(); pending != nil {
		t.Fatalf("reopened session requeued completed handoff: %+v", pending)
	}
	if _, err := restored.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if generation := restored.CompactionCount(); generation != 1 {
		t.Fatalf("reopened request re-ran handoff compaction: generation = %d, want 1", generation)
	}
	if len(resumedClient.calls) == 0 {
		t.Fatal("reopened handoff session did not produce a model request")
	}
	if futureMessages := countHandoffFutureMessageRecords(t, store); futureMessages != 1 {
		t.Fatalf("reopened handoff future-message records = %d, want 1", futureMessages)
	}
}

func assertNoTriggerHandoffProtocolItems(
	t *testing.T,
	scope string,
	items []llm.ResponseItem,
	handoffCall llm.ToolCall,
) {
	t.Helper()
	for _, item := range items {
		switch item.Type {
		case llm.ResponseItemTypeFunctionCall,
			llm.ResponseItemTypeFunctionCallOutput,
			llm.ResponseItemTypeCustomToolCall,
			llm.ResponseItemTypeCustomToolOutput:
		default:
			continue
		}
		if (item.CallID != nil && *item.CallID == handoffCall.ID) ||
			(item.Name != nil && *item.Name == handoffCall.Name) {
			t.Fatalf("%s retained trigger-handoff protocol item: %+v", scope, item)
		}
	}
}

func countHandoffFutureMessageRecords(t *testing.T, store *session.Store) int {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded handoff records: %v", err)
	}
	count := 0
	for _, record := range window.Records {
		message, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if !ok ||
			message.Role != session.MessageRoleDeveloper ||
			message.MessageType == nil ||
			*message.MessageType != session.MessageTypeHandoffFutureMessage {
			continue
		}
		count++
	}
	return count
}

func persistSuccessfulTriggerHandoff(t *testing.T, engine *Engine, callID string) llm.ToolCall {
	t.Helper()
	handoffCall := llm.ToolCall{
		ID:   callID,
		Name: string(toolspec.ToolTriggerHandoff),
		Input: mustJSON(map[string]any{
			"summarizer_prompt":    "summarize",
			"future_agent_message": "continue",
		}),
	}
	if err := engine.steer("handoff", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{
			Role:      llm.RoleAssistant,
			Phase:     textutil.Value(llm.MessagePhaseCommentary),
			Content:   textutil.Value("handoff"),
			ToolCalls: []llm.ToolCall{handoffCall},
		}},
	)); err != nil {
		t.Fatalf("persist trigger-handoff call: %v", err)
	}
	if err := engine.steer("handoff", steerToolCompletionIntent(tools.Result{
		CallID: handoffCall.ID,
		Name:   toolspec.ToolTriggerHandoff,
		Output: mustJSON(tools.TriggerHandoffResultPayload{
			FutureAgentMessageAdded: true,
		}),
	})); err != nil {
		t.Fatalf("persist trigger-handoff completion: %v", err)
	}
	return handoffCall
}
