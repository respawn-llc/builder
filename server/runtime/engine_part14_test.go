package runtime

import (
	"context"
	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	triggerhandofftool "core/server/tools"
	brand "core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReopenedSessionAfterSuccessfulTriggerHandoffRequeuesPendingHandoff(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	handoffCall := llm.ToolCall{
		ID:    "call_handoff_pending_restore",
		Name:  string(toolspec.ToolTriggerHandoff),
		Input: mustJSON(map[string]any{"summarizer_prompt": "keep API details", "future_agent_message": "resume after restart"}),
	}
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Content: "handing off", Phase: llm.MessagePhaseCommentary, ToolCalls: []llm.ToolCall{handoffCall}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	resultOutput := mustJSON(triggerhandofftool.TriggerHandoffResultPayload{
		Summary:                 "Handoff scheduled. Context will be compacted before the next model turn and future-agent guidance was saved.",
		FutureAgentMessageAdded: true,
	})
	if err := eng.steer("step-1", steerToolCompletionIntent(tools.Result{CallID: handoffCall.ID, Name: toolspec.ToolTriggerHandoff, Output: resultOutput})); err != nil {
		t.Fatalf("persist tool completion: %v", err)
	}
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleTool, ToolCallID: handoffCall.ID, Name: string(toolspec.ToolTriggerHandoff), Content: string(resultOutput)}})); err != nil {
		t.Fatalf("append tool result: %v", err)
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	resumedClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "resumed", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{InputTokens: 300, WindowTokens: 2_000},
		},
	}}
	restored := mustNewHandoffTestEngine(t, reopenedStore, resumedClient, Config{})
	if restored.handoffRuntimeState().RequestSnapshot() == nil {
		t.Fatal("expected restore to recover pending handoff request")
	}
	if got, want := restored.handoffRuntimeState().RequestSnapshot().summarizerPrompt, "keep API details"; got != want {
		t.Fatalf("pending summarizer_prompt = %q, want %q", got, want)
	}
	if got, want := restored.handoffRuntimeState().RequestSnapshot().futureAgentMessage, "resume after restart"; got != want {
		t.Fatalf("pending future_agent_message = %q, want %q", got, want)
	}

	msg, err := restored.SubmitUserMessage(context.Background(), "continue")
	if err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if msg.Content != "resumed" {
		t.Fatalf("assistant content = %q, want resumed", msg.Content)
	}
	if len(resumedClient.calls) != 2 {
		t.Fatalf("expected recovered handoff compaction plus follow-up request, got %d", len(resumedClient.calls))
	}
	first := resumedClient.calls[0]
	foundInstructions := false
	for _, item := range first.Items {
		if item.Type == llm.ResponseItemTypeMessage && item.Role == llm.RoleDeveloper && item.Content == compactionInstructions("keep API details") {
			foundInstructions = true
			break
		}
	}
	if !foundInstructions {
		t.Fatalf("expected restored handoff compaction request to include summarizer prompt, items=%+v", first.Items)
	}
	followUp := resumedClient.calls[1]
	if got, want := followUp.SessionID, restored.SessionID(); got != want {
		t.Fatalf("expected follow-up request session id to stay on the main conversation after restored handoff compaction, got %q want %q", got, want)
	}
	if got, want := followUp.PromptCacheKey, conversationPromptCacheKey(restored.SessionID(), restored.compactionRuntimeState().Count()); got != want {
		t.Fatalf("expected follow-up request prompt cache key to rotate after restored handoff compaction, got %q want %q", got, want)
	}
	foundCall := false
	foundOutput := false
	foundFuture := false
	for _, item := range followUp.Items {
		switch {
		case item.Type == llm.ResponseItemTypeFunctionCall && item.CallID == handoffCall.ID:
			foundCall = true
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == handoffCall.ID:
			foundOutput = true
		case item.Type == llm.ResponseItemTypeMessage && item.MessageType == llm.MessageTypeHandoffFutureMessage && item.Content == prompts.FormatHandoffFutureAgentMessage("resume after restart"):
			foundFuture = true
		}
	}
	if foundCall || foundOutput {
		t.Fatalf("expected recovered follow-up request to omit lingering trigger_handoff items, foundCall=%v foundOutput=%v items=%+v", foundCall, foundOutput, followUp.Items)
	}
	if !foundFuture {
		t.Fatalf("expected recovered follow-up request to include future-agent message, items=%+v", followUp.Items)
	}
}

func TestForkedSessionAfterTriggerHandoffRequeuesPendingHandoff(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	handoffCall := llm.ToolCall{
		ID:    "call_handoff_fork_restore",
		Name:  string(toolspec.ToolTriggerHandoff),
		Input: mustJSON(map[string]any{"future_agent_message": "resume after fork"}),
	}
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Content: "handing off", Phase: llm.MessagePhaseCommentary, ToolCalls: []llm.ToolCall{handoffCall}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	resultOutput := mustJSON(triggerhandofftool.TriggerHandoffResultPayload{
		Summary:                 "Handoff scheduled. Context will be compacted before the next model turn and future-agent guidance was saved.",
		FutureAgentMessageAdded: true,
	})
	if err := eng.steer("step-1", steerToolCompletionIntent(tools.Result{CallID: handoffCall.ID, Name: toolspec.ToolTriggerHandoff, Output: resultOutput})); err != nil {
		t.Fatalf("persist tool completion: %v", err)
	}
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleTool, ToolCallID: handoffCall.ID, Name: string(toolspec.ToolTriggerHandoff), Content: string(resultOutput)}})); err != nil {
		t.Fatalf("append tool result: %v", err)
	}
	if err := eng.steer("step-2", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "edit anchor"}})); err != nil {
		t.Fatalf("append second user message: %v", err)
	}

	forkedStore, _, err := session.ForkAtUserMessage(store, userMessageSeqAt(t, store, 2), "Parent -> edit")
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	forked := mustNewHandoffTestEngine(t, forkedStore, &fakeClient{}, Config{})
	if forked.handoffRuntimeState().RequestSnapshot() == nil {
		t.Fatal("expected forked session to recover pending handoff request")
	}
	if got, want := forked.handoffRuntimeState().RequestSnapshot().futureAgentMessage, "resume after fork"; got != want {
		t.Fatalf("forked pending future_agent_message = %q, want %q", got, want)
	}
}

func TestReopenedSessionAfterTriggerHandoffDoesNotRequeueWhenAnyCompactionAlreadyHappened(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	handoffCall := llm.ToolCall{
		ID:    "call_handoff_satisfied_restore",
		Name:  string(toolspec.ToolTriggerHandoff),
		Input: mustJSON(map[string]any{"future_agent_message": "resume after manual compact"}),
	}
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Content: "handing off", Phase: llm.MessagePhaseCommentary, ToolCalls: []llm.ToolCall{handoffCall}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	resultOutput := mustJSON(triggerhandofftool.TriggerHandoffResultPayload{
		Summary:                 "Handoff scheduled. Context will be compacted before the next model turn and future-agent guidance was saved.",
		FutureAgentMessageAdded: true,
	})
	if err := eng.steer("step-1", steerToolCompletionIntent(tools.Result{CallID: handoffCall.ID, Name: toolspec.ToolTriggerHandoff, Output: resultOutput})); err != nil {
		t.Fatalf("persist tool completion: %v", err)
	}
	if err := newCompactionPersistence(eng).replaceHistory("step-1", "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}})); err != nil {
		t.Fatalf("replace history: %v", err)
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	resumedClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "resumed", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{InputTokens: 300, WindowTokens: 2_000},
	}}}
	restored := mustNewHandoffTestEngine(t, reopenedStore, resumedClient, Config{})
	if restored.handoffRuntimeState().RequestSnapshot() != nil {
		t.Fatalf("did not expect restore to requeue handoff after later compaction, got %+v", restored.handoffRuntimeState().RequestSnapshot())
	}

	msg, err := restored.SubmitUserMessage(context.Background(), "continue")
	if err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if msg.Content != "resumed" {
		t.Fatalf("assistant content = %q, want resumed", msg.Content)
	}
	if len(resumedClient.calls) != 1 {
		t.Fatalf("expected compaction-satisfied session to resume with a single request, got %d", len(resumedClient.calls))
	}
	if got, want := resumedClient.calls[0].SessionID, restored.SessionID(); got != want {
		t.Fatalf("expected resumed request session id to stay on the main conversation after restored compaction, got %q want %q", got, want)
	}
	if got, want := resumedClient.calls[0].PromptCacheKey, conversationPromptCacheKey(restored.SessionID(), restored.compactionRuntimeState().Count()); got != want {
		t.Fatalf("expected resumed request prompt cache key to stay rotated after restored compaction, got %q want %q", got, want)
	}
	for _, item := range resumedClient.calls[0].Items {
		switch {
		case item.Type == llm.ResponseItemTypeFunctionCall && item.CallID == handoffCall.ID:
			t.Fatalf("did not expect reopened request to include lingering trigger_handoff call item, items=%+v", resumedClient.calls[0].Items)
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == handoffCall.ID:
			t.Fatalf("did not expect reopened request to include lingering trigger_handoff output item, items=%+v", resumedClient.calls[0].Items)
		}
	}
}

func TestReopenedSessionAfterFailedTriggerHandoffDoesNotRequeuePendingHandoff(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	handoffCall := llm.ToolCall{
		ID:    "call_handoff_failed_restore",
		Name:  string(toolspec.ToolTriggerHandoff),
		Input: mustJSON(map[string]any{"future_agent_message": "should not resume"}),
	}
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Content: "attempting handoff", Phase: llm.MessagePhaseCommentary, ToolCalls: []llm.ToolCall{handoffCall}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	failedOutput := mustJSON(map[string]any{"error": handoffDisabledByUserMessage})
	if err := eng.steer("step-1", steerToolCompletionIntent(tools.Result{CallID: handoffCall.ID, Name: toolspec.ToolTriggerHandoff, IsError: true, Output: failedOutput})); err != nil {
		t.Fatalf("persist failed tool completion: %v", err)
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	restored := mustNewHandoffTestEngine(t, reopenedStore, &fakeClient{}, Config{})
	if restored.handoffRuntimeState().RequestSnapshot() != nil {
		t.Fatalf("did not expect failed trigger_handoff completion to requeue handoff, got %+v", restored.handoffRuntimeState().RequestSnapshot())
	}
}

func TestReopenedSessionAfterLegacyReviewerRollbackStillRequeuesPendingTriggerHandoff(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	handoffCall := llm.ToolCall{
		ID:    "call_handoff_rollback_restore",
		Name:  string(toolspec.ToolTriggerHandoff),
		Input: mustJSON(map[string]any{"future_agent_message": "resume after rollback"}),
	}
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Content: "handing off", Phase: llm.MessagePhaseCommentary, ToolCalls: []llm.ToolCall{handoffCall}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	resultOutput := mustJSON(triggerhandofftool.TriggerHandoffResultPayload{
		Summary:                 "Handoff scheduled. Context will be compacted before the next model turn and future-agent guidance was saved.",
		FutureAgentMessageAdded: true,
	})
	if err := eng.steer("step-1", steerToolCompletionIntent(tools.Result{CallID: handoffCall.ID, Name: toolspec.ToolTriggerHandoff, Output: resultOutput})); err != nil {
		t.Fatalf("persist tool completion: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "history_replaced", historyReplacementPayload{Engine: "reviewer_rollback", Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: "rolled back"}})}); err != nil {
		t.Fatalf("append legacy reviewer rollback history replacement: %v", err)
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	restored := mustNewHandoffTestEngine(t, reopenedStore, &fakeClient{}, Config{})
	if restored.handoffRuntimeState().RequestSnapshot() == nil {
		t.Fatal("expected ignored legacy reviewer rollback to preserve pending handoff recovery")
	}
	if got, want := restored.handoffRuntimeState().RequestSnapshot().futureAgentMessage, "resume after rollback"; got != want {
		t.Fatalf("pending future_agent_message = %q, want %q", got, want)
	}
}

func TestManualCompactionClearsQueuedTriggerHandoff(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{InputTokens: 300, WindowTokens: 2_000},
		},
	}}

	eng := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_manual_clear", Name: string(toolspec.ToolTriggerHandoff)}, "", "resume after manual compact")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if eng.handoffRuntimeState().RequestSnapshot() == nil {
		t.Fatal("expected queued handoff before manual compaction")
	}
	if err := eng.CompactContext(context.Background(), "manual compact now"); err != nil {
		t.Fatalf("manual compact: %v", err)
	}
	if eng.handoffRuntimeState().RequestSnapshot() != nil {
		t.Fatalf("expected manual compaction to clear queued handoff, got %+v", eng.handoffRuntimeState().RequestSnapshot())
	}

	msg, err := eng.SubmitUserMessage(context.Background(), "continue")
	if err != nil {
		t.Fatalf("submit after manual compaction: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected manual compaction plus a single follow-up request, got %d", len(client.calls))
	}
}

func TestManualCompactionRemotePassesSlashCommandArgumentsAsInstructions(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		compactionResponses: []llm.CompactionResponse{
			{
				OutputItems: []llm.ResponseItem{
					{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "seed"},
					{Type: llm.ResponseItemTypeCompaction, ID: "cmp_1", EncryptedContent: "enc_1"},
				},
				Usage: llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
			},
		},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	args := "preserve migration caveats"
	if err := eng.CompactContext(context.Background(), args); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("expected one remote compact call, got %d", len(client.compactionCalls))
	}
	if got, want := client.compactionCalls[0].Instructions, compactionInstructions(args); got != want {
		t.Fatalf("unexpected compact instructions\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestManualCompactionLocalAppendsSlashCommandArgumentsToPrompt(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "summary"}},
		},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	args := "keep TODO decisions"
	if err := eng.CompactContext(context.Background(), args); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected one local-summary model call, got %d", len(client.calls))
	}
	if len(client.calls[0].Tools) == 0 {
		t.Fatalf("expected tools to remain declared for local compaction cache stability")
	}

	additional := additionalCompactionInstructionsHeader + "\n " + args
	found := false
	for _, item := range client.calls[0].Items {
		if item.Type == llm.ResponseItemTypeMessage && item.Role == llm.RoleDeveloper && item.Content == compactionInstructions(args) && strings.HasSuffix(item.Content, additional) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected local compact prompt to include appended slash command args, got %+v", client.calls[0].Items)
	}
}

func TestManualCompactionLocalSendsPromptAsDeveloperMessage(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "summary"},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected one local-summary model call, got %d", len(client.calls))
	}

	found := false
	for _, item := range client.calls[0].Items {
		if item.Type == llm.ResponseItemTypeMessage && item.Role == llm.RoleDeveloper && item.Content == prompts.CompactionPrompt {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected local compaction prompt as developer message, got %+v", client.calls[0].Items)
	}
}

func TestManualCompactionAppendsLastVisibleUserMessageCarryover(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		compactionResponses: []llm.CompactionResponse{
			{
				OutputItems: []llm.ResponseItem{
					{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "condensed summary"},
					{Type: llm.ResponseItemTypeCompaction, ID: "cmp_1", EncryptedContent: "enc_1"},
				},
				Usage: llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
			},
		},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeCompactionSummary, Content: "older summary"}})); err != nil {
		t.Fatalf("append compaction summary: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "please keep tests green"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	if len(messages) == 0 {
		t.Fatal("expected messages after manual compaction")
	}
	carryoverIndex := -1
	var carryover llm.Message
	for i, message := range messages {
		switch message.MessageType {
		case llm.MessageTypeManualCompactionCarryover:
			carryoverIndex = i
			carryover = message
		}
	}
	if carryoverIndex < 0 {
		t.Fatalf("expected manual compaction carryover in message history, got %+v", messages)
	}
	if carryover.Role != llm.RoleDeveloper {
		t.Fatalf("expected developer carryover message, got role=%q", carryover.Role)
	}
	if carryover.MessageType != llm.MessageTypeManualCompactionCarryover {
		t.Fatalf("expected manual compaction carryover message type, got %q", carryover.MessageType)
	}
	if !strings.Contains(carryover.Content, "please keep tests green") {
		t.Fatalf("expected carryover to include last visible user message, got %q", carryover.Content)
	}
	if strings.Contains(carryover.Content, "older summary") {
		t.Fatalf("did not expect prior compaction summary in carryover, got %q", carryover.Content)
	}
}

func TestManualLocalCompactionRebuildsCanonicalContextOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, agentsGlobalDirName)
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global agents dir: %v", err)
	}
	globalPath := filepath.Join(globalDir, agentsFileName)
	if err := os.WriteFile(globalPath, []byte("global instructions"), 0o644); err != nil {
		t.Fatalf("write global AGENTS.md: %v", err)
	}

	workspace := t.TempDir()
	workspacePath := filepath.Join(workspace, agentsFileName)
	if err := os.WriteFile(workspacePath, []byte("workspace instructions"), 0o644); err != nil {
		t.Fatalf("write workspace AGENTS.md: %v", err)
	}
	writeTestSkill(t, filepath.Join(workspace, brand.ConfigDirName, "skills", "workspace-skill"), "workspace-skill", "from workspace")

	store := mustCreateNamedTestSession(t, "ws", workspace)
	client := &fakeCompactionClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
		Usage:     llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "please keep tests green"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	if len(messages) < 6 {
		t.Fatalf("expected canonical post-compaction messages, got %+v", messages)
	}
	if messages[0].MessageType != llm.MessageTypeCompactionSummary {
		t.Fatalf("expected compaction summary first, got %+v", messages[0])
	}
	if messages[1].MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected environment second, got %+v", messages[1])
	}
	if messages[2].MessageType != llm.MessageTypeSkills {
		t.Fatalf("expected skills third, got %+v", messages[2])
	}
	if messages[3].MessageType != llm.MessageTypeAgentsMD || !strings.Contains(messages[3].Content, "source: "+globalPath) {
		t.Fatalf("expected global AGENTS after skills, got %+v", messages[3])
	}
	if messages[4].MessageType != llm.MessageTypeAgentsMD || !strings.Contains(messages[4].Content, "source: "+workspacePath) {
		t.Fatalf("expected workspace AGENTS after global AGENTS, got %+v", messages[4])
	}
	if messages[5].MessageType != llm.MessageTypeManualCompactionCarryover || !strings.Contains(messages[5].Content, "please keep tests green") {
		t.Fatalf("expected manual carryover after reinjected base context, got %+v", messages[5])
	}
}

func TestHandoffCompactionPlacesAtomicHeadlessContextBeforeFutureMessage(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
		Usage:     llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := store.SetHeadlessActive(true); err != nil {
		t.Fatalf("mark headless active: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "continue"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	eng.handoffRuntimeState().QueueRequest("", "resume with tests")

	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err != nil {
		t.Fatalf("apply pending handoff: %v", err)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	futureIdx := -1
	headlessIdx := -1
	for idx, message := range messages {
		switch message.MessageType {
		case llm.MessageTypeHandoffFutureMessage:
			futureIdx = idx
		case llm.MessageTypeHeadlessMode:
			if idx > 0 {
				headlessIdx = idx
			}
		}
	}
	if futureIdx < 0 {
		t.Fatalf("expected future-agent message after handoff compaction, got %+v", messages)
	}
	if headlessIdx < 0 {
		t.Fatalf("expected headless enter reinjection after handoff compaction, got %+v", messages)
	}
	if headlessIdx >= futureIdx {
		t.Fatalf("expected atomic headless context before future-agent carryover, headless=%d future=%d messages=%+v", headlessIdx, futureIdx, messages)
	}
}

func TestManualLocalCompactionPlacesSummaryBeforeCarryoverInTranscript(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
			Usage:     llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
		}},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "please keep tests green"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}

	entries := eng.ChatSnapshot().Entries
	if len(entries) < 3 {
		t.Fatalf("expected transcript entries after compaction, got %+v", entries)
	}

	summaryIndex := -1
	summaryCount := 0
	carryoverIndex := -1
	for i, entry := range entries {
		switch entry.Role {
		case "compaction_summary":
			summaryIndex = i
			summaryCount++
		case "manual_compaction_carryover":
			carryoverIndex = i
		}
	}
	if summaryIndex < 0 || carryoverIndex < 0 {
		t.Fatalf("expected summary and carryover entries, got %+v", entries)
	}
	if summaryCount != 1 {
		t.Fatalf("expected exactly one compaction summary entry, got %d entries=%+v", summaryCount, entries)
	}
	if summaryIndex >= carryoverIndex {
		t.Fatalf("expected compaction summary before manual carryover, got %+v", entries)
	}
}

func TestManualLocalCompactionOmitsCarryoverWithoutNewUserMessageSincePreviousCompaction(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
			Usage:     llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
		}},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "older user message"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeCompactionSummary, Content: "previous compaction summary"}})); err != nil {
		t.Fatalf("append previous compaction summary: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}

	for _, message := range eng.transcriptRuntimeState().SnapshotMessages() {
		if message.MessageType == llm.MessageTypeManualCompactionCarryover {
			t.Fatalf("did not expect manual carryover message when no user message followed prior compaction, got %+v", eng.transcriptRuntimeState().SnapshotMessages())
		}
	}
	for _, entry := range eng.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleManualCompactionCarryover) {
			t.Fatalf("did not expect manual carryover transcript entry when no user message followed prior compaction, got %+v", eng.ChatSnapshot().Entries)
		}
	}
}

func TestReopenedManualCompactionKeepsCarryoverAsSingleDetailTranscriptEntry(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
			Usage:     llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
		}},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "please keep tests green"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	restored := mustNewExecTestEngine(t, reopenedStore, &fakeClient{}, Config{CompactionMode: "local"})

	messages := restored.transcriptRuntimeState().SnapshotMessages()
	carryoverMessages := 0
	for _, message := range messages {
		if message.MessageType != llm.MessageTypeManualCompactionCarryover {
			continue
		}
		carryoverMessages++
		if !strings.Contains(message.Content, "please keep tests green") {
			t.Fatalf("expected reopened model carryover to preserve last user text, got %q", message.Content)
		}
	}
	if carryoverMessages != 1 {
		t.Fatalf("manual compaction carryover message count = %d, want 1; messages=%+v", carryoverMessages, messages)
	}

	entries := restored.ChatSnapshot().Entries
	carryoverEntries := 0
	for _, entry := range entries {
		if entry.Role != string(transcript.EntryRoleManualCompactionCarryover) {
			continue
		}
		carryoverEntries++
		if !strings.Contains(entry.Text, "please keep tests green") {
			t.Fatalf("expected reopened transcript carryover to preserve last user text, got %q", entry.Text)
		}
		if entry.Visibility != transcript.EntryVisibilityDetail {
			t.Fatalf("expected reopened transcript carryover to stay detail-only, got %+v", entry)
		}
	}
	if carryoverEntries != 1 {
		t.Fatalf("manual compaction carryover transcript entry count = %d, want 1; entries=%+v", carryoverEntries, entries)
	}
}
