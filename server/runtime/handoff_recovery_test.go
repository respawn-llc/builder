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
	for _, item := range restored.transcriptRuntimeState().SnapshotItems() {
		if item.CallID == nil || *item.CallID != handoffCall.ID {
			continue
		}
		switch item.Type {
		case llm.ResponseItemTypeFunctionCall,
			llm.ResponseItemTypeFunctionCallOutput,
			llm.ResponseItemTypeCustomToolCall,
			llm.ResponseItemTypeCustomToolOutput:
			t.Fatalf("reopened active items retained trigger-handoff protocol item: %+v", item)
		}
	}
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
