package runtime

import (
	"context"
	"testing"

	"core/server/llm"
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

	handoffCall := llm.ToolCall{
		ID:   "handoff-call",
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
