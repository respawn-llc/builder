package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestReopenedSessionAfterSuccessfulTriggerHandoffRequeuesPendingHandoff(t *testing.T) {
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
	pending := restored.handoffRuntimeState().RequestSnapshot()
	if pending == nil || pending.summarizerPrompt == "" || pending.futureAgentMessage == "" {
		t.Fatalf("restored pending handoff = %+v", pending)
	}

	if _, err := restored.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit after restored handoff: %v", err)
	}
	if generation := restored.compactionRuntimeState().Count(); generation != 1 {
		t.Fatalf("restored handoff compaction generation = %d, want 1", generation)
	}

	futureMessages := 0
	for _, item := range restored.transcriptRuntimeState().SnapshotItems() {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.Role != nil &&
			*item.Role == llm.RoleDeveloper &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeHandoffFutureMessage {
			futureMessages++
		}
	}
	if futureMessages != 1 {
		t.Fatalf("restored handoff future-message items = %d, want 1", futureMessages)
	}
}

func TestReopenedSessionAfterTriggerHandoffDoesNotRequeueWhenAnyCompactionAlreadyHappened(t *testing.T) {
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
	handoffCall := persistSuccessfulTriggerHandoff(t, engine, "future-message-retry-handoff-call")
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
	if pendingFuture := engine.handoffRuntimeState().FutureMessageSnapshot(); pendingFuture == "" {
		t.Fatal("failed future-message append lost retry ownership")
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
	if pendingFuture := restored.handoffRuntimeState().FutureMessageSnapshot(); pendingFuture == "" {
		t.Fatal("reopened session lost future-message retry ownership")
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
	request := resumedClient.calls[len(resumedClient.calls)-1]
	expectedCacheKey := conversationPromptCacheKeyForLineage(
		restored.SessionID(),
		store.Meta().PromptCacheLineageGeneration,
		restored.CompactionCount(),
	)
	if request.SessionID != restored.SessionID() || request.PromptCacheKey != expectedCacheKey {
		t.Fatalf(
			"reopened request identity = session:%q cache-key:%q",
			request.SessionID,
			request.PromptCacheKey,
		)
	}

	futureMessages := 0
	for _, item := range request.Items {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.Role != nil &&
			*item.Role == llm.RoleDeveloper &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeHandoffFutureMessage {
			futureMessages++
		}
	}
	if futureMessages != 1 {
		t.Fatalf("reopened request future-message items = %d, want 1", futureMessages)
	}
	assertNoTriggerHandoffProtocolItems(t, "reopened request", request.Items, handoffCall)
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
