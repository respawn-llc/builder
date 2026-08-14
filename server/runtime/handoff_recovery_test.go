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

	if futureMessages := countHandoffFutureMessageRecords(t, store); futureMessages != 0 {
		t.Fatalf("restored handoff future-message records = %d, want 0 for atomic replacement", futureMessages)
	}
}

func TestHandoffReplacementStoresFutureMessageAtomically(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	persistSuccessfulTriggerHandoff(t, engine, "atomic-handoff-call")

	summaryClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
		Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}}
	restored := mustNewHandoffTestEngine(t, mustOpenTestSession(t, store.Dir()), summaryClient, Config{})
	var applied bool
	err := withActiveTestRun(t, restored, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		var applyErr error
		applied, applyErr = restored.applyPendingHandoffIfNeeded(ctx, stepID)
		return applyErr
	})
	if err != nil {
		t.Fatalf("apply restored handoff: %v", err)
	}
	if !applied {
		t.Fatal("restored handoff did not compact")
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read handoff replacement records: %v", err)
	}
	var replacement session.HistoryReplacementRecord
	replacementIndex := -1
	for index, record := range window.Records {
		if candidate, ok := mustSessionEventPayload(record).(session.HistoryReplacementRecord); ok {
			replacementIndex = index
			replacement = candidate
		}
	}
	if replacement.Items == nil {
		t.Fatalf("handoff replacement is absent: %+v", window.Records)
	}
	if replacement.PendingHandoffFutureMessage != nil {
		t.Fatalf("atomic handoff replacement persisted pending future message: %q", *replacement.PendingHandoffFutureMessage)
	}
	futureItems := 0
	for _, item := range replacement.Items {
		if item.MessageType != nil && *item.MessageType == session.MessageTypeHandoffFutureMessage {
			futureItems++
		}
	}
	if futureItems != 1 {
		t.Fatalf("atomic handoff replacement future-agent items = %d, want 1", futureItems)
	}
	for _, record := range window.Records[replacementIndex+1:] {
		if message, ok := mustSessionEventPayload(record).(session.MessageRecord); ok &&
			message.MessageType != nil &&
			*message.MessageType == session.MessageTypeHandoffFutureMessage {
			t.Fatalf("atomic handoff replacement appended a second typed future-agent event: %+v", record)
		}
	}

	if err := restored.Close(); err != nil {
		t.Fatalf("close restored handoff engine: %v", err)
	}
	reopened := mustNewHandoffTestEngine(t, mustOpenTestSession(t, store.Dir()), &fakeClient{}, Config{})
	if pending := reopened.handoffRuntimeState().RequestSnapshot(); pending != nil {
		t.Fatalf("reopened atomic handoff queued a duplicate request: %+v", pending)
	}
	if future := reopened.handoffRuntimeState().FutureMessageSnapshot(); future != "" {
		t.Fatalf("reopened atomic handoff queued a duplicate future message: %q", future)
	}
	if got := countHandoffFutureMessages(reopened.transcriptRuntimeState().SnapshotItems()); got != 1 {
		t.Fatalf("reopened atomic handoff future-agent items = %d, want 1", got)
	}
}

func TestLegacyPendingOnlyHandoffRecoveryRemainsStable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		replacementItems []llm.ResponseItem
		wantQueuedFuture bool
		appendTypedEvent bool
	}{
		{
			name: "pending only",
			replacementItems: llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleUser,
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			}}),
			wantQueuedFuture: true,
		},
		{
			name: "pending followed by typed future message",
			replacementItems: llm.ItemsFromMessages([]llm.Message{
				{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
			}),
			wantQueuedFuture: false,
			appendTypedEvent: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			mustAppendTestEvent(t, store, "legacy-handoff", historyReplacementPayload{
				Engine:                      "local",
				Mode:                        string(compactionModeManual),
				CompactionNumber:            textutil.Value(1),
				PendingHandoffFutureMessage: textutil.Value("continue"),
				Items:                       testCase.replacementItems,
			})
			if testCase.appendTypedEvent {
				mustAppendTestEvent(t, store, "legacy-handoff", llm.Message{
					Role:        llm.RoleDeveloper,
					MessageType: textutil.Value(llm.MessageTypeHandoffFutureMessage),
					Content:     textutil.Value("continue"),
				})
			}
			restored := mustNewHandoffTestEngine(t, mustOpenTestSession(t, store.Dir()), &fakeClient{}, Config{})
			if got := restored.handoffRuntimeState().FutureMessageSnapshot() != ""; got != testCase.wantQueuedFuture {
				t.Fatalf("queued legacy future message = %t, want %t", got, testCase.wantQueuedFuture)
			}
			if !testCase.wantQueuedFuture {
				if got := countHandoffFutureMessages(restored.transcriptRuntimeState().SnapshotItems()); got != 1 {
					t.Fatalf("reopened typed legacy future messages = %d, want 1", got)
				}
				return
			}

			err := withActiveTestRun(t, restored, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
				_, applyErr := restored.applyPendingHandoffIfNeeded(ctx, stepID)
				return applyErr
			})
			if err != nil {
				t.Fatalf("append recovered legacy future message: %v", err)
			}
			if got := countHandoffFutureMessageRecords(t, store); got != 1 {
				t.Fatalf("legacy recovered future-message records = %d, want 1", got)
			}
			if err := restored.Close(); err != nil {
				t.Fatalf("close recovered legacy engine: %v", err)
			}
			reopened := mustNewHandoffTestEngine(t, mustOpenTestSession(t, store.Dir()), &fakeClient{}, Config{})
			if future := reopened.handoffRuntimeState().FutureMessageSnapshot(); future != "" {
				t.Fatalf("reopened recovered legacy future message remained queued: %q", future)
			}
			if got := countHandoffFutureMessages(reopened.transcriptRuntimeState().SnapshotItems()); got != 1 {
				t.Fatalf("reopened recovered legacy future messages = %d, want 1", got)
			}
		})
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

	completeManualEligibilityAgentStep(t, engine)
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

func TestReopenedSessionAfterTriggerHandoffUsesAtomicFutureMessageReplacement(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
		Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}}
	engine := mustNewHandoffTestEngine(t, store, client, Config{})
	persistSuccessfulTriggerHandoff(t, engine, "future-message-atomic-handoff-call")
	engine.handoffRuntimeState().QueueRequest("summarize", "continue")

	var applied bool
	err := withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		var applyErr error
		applied, applyErr = engine.applyPendingHandoffIfNeeded(ctx, stepID)
		return applyErr
	})
	if err != nil || !applied {
		t.Fatalf("atomic handoff outcome = applied:%v error:%v", applied, err)
	}
	if pending := engine.handoffRuntimeState().RequestSnapshot(); pending != nil {
		t.Fatalf("atomic handoff retained pending request: %+v", pending)
	}
	if future := engine.handoffRuntimeState().FutureMessageSnapshot(); future != "" {
		t.Fatalf("atomic handoff retained future-message queue: %q", future)
	}
	if got := countHandoffFutureMessageRecords(t, store); got != 0 {
		t.Fatalf("atomic handoff appended future-message records = %d, want 0", got)
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

	var applied bool
	err := withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		var applyErr error
		applied, applyErr = engine.applyPendingHandoffIfNeeded(ctx, stepID)
		return applyErr
	})
	if applied || !errors.Is(err, observerErr) {
		t.Fatalf("future-message append outcome = applied:%v error:%v", applied, err)
	}
	if futureMessages := countHandoffFutureMessageRecords(t, store); futureMessages != 1 {
		t.Fatalf("committed handoff future-message records = %d, want 1", futureMessages)
	}

	err = withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		var applyErr error
		applied, applyErr = engine.applyPendingHandoffIfNeeded(ctx, stepID)
		return applyErr
	})
	if applied || err != nil {
		t.Fatalf("second future-message append outcome = applied:%v error:%v", applied, err)
	}
	if futureMessages := countHandoffFutureMessageRecords(t, store); futureMessages != 1 {
		t.Fatalf("second future-message append changed durable record count to %d", futureMessages)
	}
}

func TestReopenedSessionAfterTriggerHandoffUsesStableRequestSessionAndOmitsLingeringCallOutput(t *testing.T) {
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

	var compacted bool
	err := withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		var applyErr error
		compacted, applyErr = engine.applyPendingHandoffIfNeeded(ctx, stepID)
		return applyErr
	})
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
	if futureMessages := countHandoffFutureMessageRecords(t, store); futureMessages != 0 {
		t.Fatalf("reopened handoff future-message records = %d, want 0 for atomic replacement", futureMessages)
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

func countHandoffFutureMessages(items []llm.ResponseItem) int {
	count := 0
	for _, item := range items {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeHandoffFutureMessage {
			count++
		}
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
