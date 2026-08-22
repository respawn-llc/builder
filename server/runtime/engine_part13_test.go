package runtime

import (
	"context"
	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	triggerhandofftool "core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"errors"
	"testing"
)

func TestRunStepLoopDoesNotDuplicateCompactionSoonReminderAfterAutoCompactionIsDisabled(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("checking"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
				ToolCalls: []llm.ToolCall{{ID: "call_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
				Usage:     llm.Usage{InputTokens: 100, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
				Usage:     llm.Usage{InputTokens: 920, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("next"), Phase: textutil.Value(llm.MessagePhaseFinal)},
				Usage:     llm.Usage{InputTokens: 930, WindowTokens: 2_000},
			},
		},
		inputTokenCountFn: func(req llm.Request) int {
			hasToolResult := false
			for _, msg := range requestMessages(req) {
				if msg.Role == llm.RoleTool {
					hasToolResult = true
					break
				}
			}
			if hasToolResult {
				return 890
			}
			return 930
		},
	}

	eng := mustNewExecTestEngine(t, store, client, Config{
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	restoreStep := setTestActiveStep(eng, "step-1")
	if _, err := eng.runStepLoop(context.Background(), runtimeTestStepID("step-1")); err != nil {
		t.Fatalf("first runStepLoop: %v", err)
	}
	restoreStep()

	changed, enabled := eng.SetAutoCompactionEnabled(false)
	if !changed || enabled {
		t.Fatalf("expected auto compaction toggle off, changed=%v enabled=%v", changed, enabled)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("continue")}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	restoreStep = setTestActiveStep(eng, "step-2")
	msg, err := eng.runStepLoop(context.Background(), runtimeTestStepID("step-2"))
	restoreStep()
	if err != nil {
		t.Fatalf("second runStepLoop: %v", err)
	}
	if messageContent(msg) != "next" {
		t.Fatalf("unexpected second assistant message: %+v", msg)
	}
	if len(client.calls) != 3 {
		t.Fatalf("expected three model requests across both runs, got %d", len(client.calls))
	}

	remindersInThirdRequest := 0
	for _, reqMsg := range requestMessages(client.calls[2]) {
		if reqMsg.Role == llm.RoleDeveloper && reqMsg.MessageType != nil && *reqMsg.MessageType == llm.MessageTypeCompactionSoonReminder {
			remindersInThirdRequest++
		}
	}
	if remindersInThirdRequest != 1 {
		t.Fatalf("expected exactly one historical reminder in request while disabled, got %d messages=%+v", remindersInThirdRequest, requestMessages(client.calls[2]))
	}
}

func countCompactionSoonReminderWarnings(_ *Engine, snapshot ChatSnapshot) int {
	count := 0
	for _, entry := range snapshot.Entries {
		if entry.Role == "warning" && entry.MessageType == llm.MessageTypeCompactionSoonReminder {
			count++
		}
	}
	return count
}

func TestCompactionSoonReminderRechecksPreciselyAfterTranscriptMutation(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 840, contextWindow: 2_000}
	eng := mustNewHandoffTestEngine(t, store, client, Config{
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 860, WindowTokens: 2_000})

	restoreStep := setTestActiveStep(eng, "step-1")
	if err := newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), "step-1"); err != nil {
		t.Fatalf("reminder below exact threshold: %v", err)
	}
	restoreStep()
	if client.countCalls != 1 {
		t.Fatalf("expected first reminder probe to count precisely once, got %d", client.countCalls)
	}
	if eng.compactionRuntimeState().SoonReminderIssued() {
		t.Fatal("did not expect handoff tool to become enabled below the exact reminder threshold")
	}

	client.inputTokenCount = 860
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Content: textutil.Value("mutation")}})); err != nil {
		t.Fatalf("append mutation: %v", err)
	}
	restoreStep = setTestActiveStep(eng, "step-2")
	if err := newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), "step-2"); err != nil {
		t.Fatalf("reminder above exact threshold after mutation: %v", err)
	}
	restoreStep()
	if client.countCalls != 2 {
		t.Fatalf("expected transcript mutation to force a fresh precise reminder check, got %d calls", client.countCalls)
	}
	if !eng.compactionRuntimeState().SoonReminderIssued() {
		t.Fatal("expected reminder to enable trigger_handoff after exact recount")
	}
}

func TestTriggerHandoffSchedulesCompactionAndAppendsFutureMessageWithoutPreservedUserMessage(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")}}},
	}
	eng := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)
	activeCall := llm.ToolCall{ID: "call-handoff-1", Name: string(toolspec.ToolTriggerHandoff), Input: json.RawMessage(`{"summarizer_prompt":"keep API details","future_agent_message":"resume with tests"}`)}

	restoreStep := setTestActiveStep(eng, "step-1")
	summary, futureAdded, err := eng.TriggerHandoff(context.Background(), "step-1", activeCall, "keep API details", "resume with tests")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if summary == "" || !futureAdded {
		t.Fatalf("unexpected trigger handoff result: summary=%q futureAdded=%v", summary, futureAdded)
	}
	if len(client.calls) != 0 {
		t.Fatalf("expected handoff scheduling to avoid immediate compaction model call, got %d", len(client.calls))
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err != nil {
		t.Fatalf("apply pending handoff: %v", err)
	}
	restoreStep()
	if len(client.calls) != 1 {
		t.Fatalf("expected one local-summary model call, got %d", len(client.calls))
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	var futureMessage *llm.Message
	foundPreservedUserMessage := false
	for index := range messages {
		message := messages[index]
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeHandoffFutureMessage {
			futureMessage = &messages[index]
		}
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeCompactionPreservedUserMessage {
			foundPreservedUserMessage = true
		}
	}
	if futureMessage == nil {
		t.Fatalf("expected future-agent message in history, got %+v", messages)
	}
	if futureMessage.Role != llm.RoleDeveloper ||
		futureMessage.Content == nil ||
		*futureMessage.Content != prompts.FormatHandoffFutureAgentMessage("resume with tests") {
		t.Fatalf("future-agent message = %+v", *futureMessage)
	}
	if foundPreservedUserMessage {
		t.Fatalf("did not expect a compaction-preserved user message for trigger_handoff, got %+v", messages)
	}
}

func TestTriggerHandoffWithBlankFutureMessageAppendsNoFutureMessage(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
	}}}
	eng := mustNewHandoffTestEngine(t, store, client, Config{})
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	restoreStep := setTestActiveStep(eng, "step-1")
	_, futureAdded, err := eng.TriggerHandoff(
		context.Background(),
		"step-1",
		llm.ToolCall{ID: "call-handoff-blank", Name: string(toolspec.ToolTriggerHandoff)},
		"keep API details",
		" \n\t ",
	)
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if futureAdded {
		t.Fatal("blank future-agent message was reported as appended")
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err != nil {
		t.Fatalf("apply pending handoff: %v", err)
	}
	restoreStep()
	for _, message := range eng.transcriptRuntimeState().SnapshotMessages() {
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeHandoffFutureMessage {
			t.Fatalf("blank future-agent message reached history: %+v", message)
		}
	}
}

func TestPrepareModelTurnSkipsAutoCompactionAfterPendingHandoffCompaction(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("handoff summary")},
			Usage:     llm.Usage{InputTokens: 1_900, WindowTokens: 2_000},
		}},
		inputTokenCount: 1_900,
	}
	eng := mustNewHandoffTestEngine(t, store, client, Config{
		CompactionMode:        "local",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 1_900, WindowTokens: 2_000})
	eng.handoffRuntimeState().QueueRequest("keep runtime details", "")

	executor := &defaultStepExecutor{engine: eng}
	restoreStep := setTestActiveStep(eng, "step-1")
	if err := executor.prepareModelTurn(context.Background(), "step-1"); err != nil {
		t.Fatalf("prepare model turn: %v", err)
	}
	restoreStep()
	if len(client.calls) != 1 {
		t.Fatalf("expected only pending handoff compaction call, got %d calls", len(client.calls))
	}
}

func TestPrepareModelTurnMaterializesWorktreeReminderAfterPendingHandoffCompaction(t *testing.T) {
	store := mustCreateTestSession(t)
	target := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/handoff",
		"/tmp/wt-handoff",
		"/tmp/workspace",
		"/tmp/wt-handoff",
	))

	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("handoff summary")},
			Usage:     llm.Usage{InputTokens: 1_900, WindowTokens: 2_000},
		}},
		inputTokenCount: 1_900,
	}
	eng := mustNewHandoffTestEngine(t, store, client, Config{
		CompactionMode:        "local",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 1_900, WindowTokens: 2_000})
	eng.handoffRuntimeState().QueueRequest("keep runtime details", "")

	executor := &defaultStepExecutor{engine: eng}
	restoreStep := setTestActiveStep(eng, "step-1")
	if err := executor.prepareModelTurn(context.Background(), "step-1"); err != nil {
		t.Fatalf("prepare model turn: %v", err)
	}
	restoreStep()

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	if got := worktreeReminderMessageCount(messages); got != 1 {
		t.Fatalf("worktree reminders after handoff compaction = %d, want 1 messages=%+v", got, messages)
	}
	assertLatestWorktreeContext(t, messages, target)
	if len(client.calls) != 1 {
		t.Fatalf("expected only handoff compaction call, got %d calls", len(client.calls))
	}
}

func TestPendingTriggerHandoffFailsToolCallsAndRetriesLocalSummary(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant},
			ToolCalls: []llm.ToolCall{
				{
					ID:    "call_summary_tool",
					Name:  string(toolspec.ToolExecCommand),
					Input: json.RawMessage(`{"cmd":"pwd"}`),
				},
				{
					ID:    "call_search_summary_tool",
					Name:  string(toolspec.ToolWebSearch),
					Input: json.RawMessage(`{"query":"handoff"}`),
				},
			},
			Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("condensed summary")},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
	}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		CompactionMode: "local",
		EnabledTools:   []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch, toolspec.ToolTriggerHandoff},
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	restoreStep := setTestActiveStep(eng, "step-1")
	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_tool_retry", Name: string(toolspec.ToolTriggerHandoff)}, "keep API details", "")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err != nil {
		t.Fatalf("apply pending handoff: %v", err)
	}
	restoreStep()
	if eng.handoffRuntimeState().RequestSnapshot() != nil {
		t.Fatalf("expected successful retry to clear pending handoff, got %+v", eng.handoffRuntimeState().RequestSnapshot())
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected local summary retry after failed tool call, got %d requests", len(client.calls))
	}
	for i, request := range client.calls {
		if request.ToolChoiceMode != llm.ToolChoiceModeAutomatic {
			t.Fatalf("handoff compaction request %d tool choice mode = %q, want automatic", i, request.ToolChoiceMode)
		}
	}
	assertRequestsPreserveCacheIdentity(t, client.calls[0], client.calls[1])

	foundFailedOutputs := map[string]bool{}
	for _, item := range client.calls[1].Items {
		if item.Type != llm.ResponseItemTypeFunctionCallOutput {
			continue
		}
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(item.Output, &payload); err != nil {
			t.Fatalf("unmarshal failed tool output: %v", err)
		}
		if payload.Error == handoffCompactionToolsDisabledMessage {
			if item.CallID == nil {
				t.Fatalf("failed tool output has no call id: %+v", item)
			}
			foundFailedOutputs[*item.CallID] = true
		}
	}
	for _, callID := range []string{"call_summary_tool", "call_search_summary_tool"} {
		if !foundFailedOutputs[callID] {
			t.Fatalf("expected failed handoff tool output for %s, got items=%+v", callID, client.calls[1].Items)
		}
	}
}

func TestPendingTriggerHandoffFailsMalformedToolCallWithEmptyID(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant},
		ToolCalls: []llm.ToolCall{{
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"pwd"}`),
		}},
		Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
	}}}
	eng := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	restoreStep := setTestActiveStep(eng, "step-1")
	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_empty_id", Name: string(toolspec.ToolTriggerHandoff)}, "keep API details", "resume with tests")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); !errors.Is(err, errLocalCompactionToolCallEmptyID) {
		t.Fatalf("expected errLocalCompactionToolCallEmptyID, got %v", err)
	}
	restoreStep()
	if eng.handoffRuntimeState().RequestSnapshot() == nil {
		t.Fatal("expected malformed handoff failure to keep pending request queued")
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected malformed response to fail without retry, got %d requests", len(client.calls))
	}
}

func assertRequestsPreserveCacheIdentity(t *testing.T, first llm.Request, retry llm.Request) {
	t.Helper()
	if first.PromptCacheKey == "" {
		t.Fatal("expected first request to have prompt cache key")
	}
	if retry.PromptCacheKey != first.PromptCacheKey {
		t.Fatalf("retry PromptCacheKey = %q, want %q", retry.PromptCacheKey, first.PromptCacheKey)
	}
	if retry.PromptCacheScope != first.PromptCacheScope {
		t.Fatalf("retry PromptCacheScope = %q, want %q", retry.PromptCacheScope, first.PromptCacheScope)
	}
	firstTools, err := json.Marshal(first.Tools)
	if err != nil {
		t.Fatalf("marshal first tools: %v", err)
	}
	retryTools, err := json.Marshal(retry.Tools)
	if err != nil {
		t.Fatalf("marshal retry tools: %v", err)
	}
	if string(retryTools) != string(firstTools) {
		t.Fatalf("retry tools changed\nwant=%s\n got=%s", firstTools, retryTools)
	}
}

func TestPendingTriggerHandoffRetriesCustomToolCallOutput(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant},
			ToolCalls: []llm.ToolCall{{
				ID:          "call_custom_summary_tool",
				Name:        string(toolspec.ToolPatch),
				Custom:      true,
				CustomInput: textutil.Value("*** Begin Patch\n*** End Patch"),
			}},
			Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("condensed summary")},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
	}}
	eng := mustNewFakeToolEngine(t, store, client, Config{
		CompactionMode: "local",
		EnabledTools:   []toolspec.ID{toolspec.ToolPatch, toolspec.ToolTriggerHandoff},
	}, toolspec.ToolPatch)
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	restoreStep := setTestActiveStep(eng, "step-1")
	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_custom_tool_retry", Name: string(toolspec.ToolTriggerHandoff)}, "keep API details", "")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err != nil {
		t.Fatalf("apply pending handoff: %v", err)
	}
	restoreStep()
	if len(client.calls) != 2 {
		t.Fatalf("expected local summary retry after custom tool call, got %d requests", len(client.calls))
	}
	assertRequestsPreserveCacheIdentity(t, client.calls[0], client.calls[1])

	foundCustomFailedOutput := false
	for _, item := range client.calls[1].Items {
		if item.Type != llm.ResponseItemTypeCustomToolOutput || item.CallID == nil || *item.CallID != "call_custom_summary_tool" {
			continue
		}
		foundCustomFailedOutput = true
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(item.Output, &payload); err != nil {
			t.Fatalf("unmarshal failed custom tool output: %v", err)
		}
		if payload.Error != handoffCompactionToolsDisabledMessage {
			t.Fatalf("custom failed output error = %q, want %q", payload.Error, handoffCompactionToolsDisabledMessage)
		}
	}
	if !foundCustomFailedOutput {
		t.Fatalf("expected custom failed tool output in retry request, items=%+v", client.calls[1].Items)
	}
}

func TestPendingTriggerHandoffLeavesRequestPendingWhenSummaryRetryStillToolCalls(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_summary_tool_1",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"pwd"}`),
			}},
			Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_summary_tool_2",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"pwd"}`),
			}},
			Usage: llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_summary_tool_3",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"pwd"}`),
			}},
			Usage: llm.Usage{InputTokens: 300, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_summary_tool_4",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"pwd"}`),
			}},
			Usage: llm.Usage{InputTokens: 400, WindowTokens: 2_000},
		},
	}}
	eng := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	restoreStep := setTestActiveStep(eng, "step-1")
	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_second_failure", Name: string(toolspec.ToolTriggerHandoff)}, "keep API details", "resume with tests")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); !errors.Is(err, errLocalCompactionAttemptedToolCalls) {
		t.Fatalf("expected errLocalCompactionAttemptedToolCalls, got %v", err)
	}
	restoreStep()
	if eng.handoffRuntimeState().RequestSnapshot() == nil {
		t.Fatal("expected failed handoff retry to keep pending request queued")
	}
	if got, want := eng.handoffRuntimeState().RequestSnapshot().futureAgentMessage, "resume with tests"; got != want {
		t.Fatalf("pending future_agent_message after retry failure = %q, want %q", got, want)
	}
	if len(client.calls) != 4 {
		t.Fatalf("expected original summary request and three retries, got %d", len(client.calls))
	}
	for idx, call := range client.calls {
		if call.ToolChoiceMode != llm.ToolChoiceModeAutomatic {
			t.Fatalf("handoff compaction request %d tool choice mode = %q, want automatic", idx, call.ToolChoiceMode)
		}
		if idx == 0 {
			continue
		}
		if len(call.Tools) == 0 {
			t.Fatalf("expected retry request %d to keep tools exposed for cache stability", idx)
		}
		assertRequestsPreserveCacheIdentity(t, client.calls[0], call)
	}
}

func TestPendingTriggerHandoffRetriesAfterCompactionFailure(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("condensed summary")},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
	}}
	eng := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	restoreStep := setTestActiveStep(eng, "step-1")
	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_retry", Name: string(toolspec.ToolTriggerHandoff)}, "keep API details", "resume with tests")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if eng.handoffRuntimeState().RequestSnapshot() == nil {
		t.Fatal("expected queued handoff before compaction attempt")
	}

	client.responses = nil
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err == nil {
		t.Fatal("expected first pending handoff attempt to fail when compaction summary response is missing")
	}
	if eng.handoffRuntimeState().RequestSnapshot() == nil {
		t.Fatal("expected failed handoff compaction to leave pending request queued for retry")
	}
	if got, want := eng.handoffRuntimeState().RequestSnapshot().summarizerPrompt, "keep API details"; got != want {
		t.Fatalf("pending summarizer_prompt after failure = %q, want %q", got, want)
	}
	if got, want := eng.handoffRuntimeState().RequestSnapshot().futureAgentMessage, "resume with tests"; got != want {
		t.Fatalf("pending future_agent_message after failure = %q, want %q", got, want)
	}

	client.responses = []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("condensed summary")},
		Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err != nil {
		t.Fatalf("retry pending handoff: %v", err)
	}
	restoreStep()
	if eng.handoffRuntimeState().RequestSnapshot() != nil {
		t.Fatalf("expected successful retry to clear pending handoff, got %+v", eng.handoffRuntimeState().RequestSnapshot())
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	foundFutureMessage := false
	for _, message := range messages {
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeHandoffFutureMessage {
			foundFutureMessage = true
			break
		}
	}
	if !foundFutureMessage {
		t.Fatalf("expected successful retry to append future-agent message, got %+v", messages)
	}
}

func TestRunStepLoopTriggerHandoffOmitsCallAndOutputFromFollowUpRequestAndKeepsFutureMessage(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("handing off"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
				ToolCalls: []llm.ToolCall{{
					ID:    "call_handoff_1",
					Name:  string(toolspec.ToolTriggerHandoff),
					Input: json.RawMessage(`{"summarizer_prompt":"keep API details","future_agent_message":"resume with tests"}`),
				}},
				Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("condensed summary")},
				Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
				Usage:     llm.Usage{InputTokens: 300, WindowTokens: 2_000},
			},
		},
	}

	var eng *Engine
	registry := newTestToolRegistry(t,
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}},
		tools.HandlerRegistration{ID: toolspec.ToolTriggerHandoff, Handler: triggerhandofftool.NewTriggerHandoffTool(func() triggerhandofftool.TriggerHandoffController { return eng })},
	)
	eng = mustNewTestEngine(t, store, client, registry, Config{
		CompactionMode: "local",
		EnabledTools:   []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolTriggerHandoff},
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	restoreStep := setTestActiveStep(eng, "step-1")
	msg, err := eng.runStepLoop(context.Background(), runtimeTestStepID("step-1"))
	restoreStep()
	if err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("unexpected final assistant message: %+v", msg)
	}
	if len(client.calls) != 3 {
		t.Fatalf("expected tool call, local compaction summary, and follow-up requests, got %d", len(client.calls))
	}
	if got, want := client.calls[2].SessionID, eng.SessionID(); got == nil || *got != want {
		t.Fatalf("expected follow-up request session id to stay on the main conversation after handoff compaction, got %v want %q", got, want)
	}
	if got, want := client.calls[2].PromptCacheKey, conversationPromptCacheKey(eng.SessionID()); got != want {
		t.Fatalf("expected follow-up request prompt cache key to remain stable after handoff compaction, got %q want %q", got, want)
	}

	followUp := client.calls[2]
	foundCall := false
	foundOutput := false
	foundFuture := false
	for _, item := range followUp.Items {
		switch {
		case item.Type == llm.ResponseItemTypeFunctionCall && item.CallID != nil && *item.CallID == "call_handoff_1":
			foundCall = true
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID != nil && *item.CallID == "call_handoff_1":
			foundOutput = true
		case item.Type == llm.ResponseItemTypeMessage && item.MessageType != nil && *item.MessageType == llm.MessageTypeHandoffFutureMessage:
			foundFuture = true
		}
	}
	if foundCall || foundOutput {
		t.Fatalf("expected follow-up request to omit trigger_handoff call/output items entirely, foundCall=%v foundOutput=%v items=%+v", foundCall, foundOutput, followUp.Items)
	}
	if !foundFuture {
		t.Fatalf("expected future-agent message in follow-up request, items=%+v", followUp.Items)
	}
}

func TestRunStepLoopInjectsReminderBeforeTriggerHandoff(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("handing off"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
				ToolCalls: []llm.ToolCall{{
					ID:    "call_handoff_2",
					Name:  string(toolspec.ToolTriggerHandoff),
					Input: json.RawMessage(`{"future_agent_message":"resume with tests"}`),
				}},
				Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("condensed summary")},
				Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
				Usage:     llm.Usage{InputTokens: 300, WindowTokens: 2_000},
			},
		},
	}

	var eng *Engine
	registry := newTestToolRegistry(t,
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}},
		tools.HandlerRegistration{ID: toolspec.ToolTriggerHandoff, Handler: triggerhandofftool.NewTriggerHandoffTool(func() triggerhandofftool.TriggerHandoffController { return eng })},
	)
	eng = mustNewTestEngine(t, store, client, registry, Config{
		CompactionMode:        "local",
		ContextWindowTokens:   20_000,
		AutoCompactTokenLimit: 10_000,
		EnabledTools:          []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolTriggerHandoff},
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 8_900, WindowTokens: 20_000})

	restoreStep := setTestActiveStep(eng, "step-1")
	msg, err := eng.runStepLoop(context.Background(), runtimeTestStepID("step-1"))
	restoreStep()
	if err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("unexpected final assistant message: %+v", msg)
	}
	if len(client.calls) != 3 {
		t.Fatalf("expected trigger request, local compaction summary, and follow-up requests, got %d", len(client.calls))
	}

	remindersInFirstRequest := 0
	for _, reqMsg := range requestMessages(client.calls[0]) {
		if reqMsg.Role == llm.RoleDeveloper && reqMsg.MessageType != nil && *reqMsg.MessageType == llm.MessageTypeCompactionSoonReminder {
			remindersInFirstRequest++
		}
	}
	if remindersInFirstRequest != 1 {
		t.Fatalf("expected exactly one pre-request reminder before trigger_handoff, got %d messages=%+v", remindersInFirstRequest, requestMessages(client.calls[0]))
	}
}

var errProbeCommittedObserverFailure = errors.New("probe committed observer failure")

type armedCommittedAppendFailObserver struct{ armed bool }

func (o *armedCommittedAppendFailObserver) ObservePersistedStore(_ context.Context, _ session.PersistedStoreSnapshot) error {
	if o.armed {
		return errProbeCommittedObserverFailure
	}
	return nil
}

func TestCacheWarningSteeringPropagatesCommittedAppendError(t *testing.T) {
	observer := &armedCommittedAppendFailObserver{}
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir, withRuntimeTestPersistenceObserver(observer))
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	observer.armed = true
	err := eng.steer(runtimeTestStepID("step-1"), steerCacheWarningIntent(transcript.CacheWarning{
		Scope:  transcript.CacheWarningScopeConversation,
		Reason: transcript.CacheWarningReasonCompaction,
	}, transcript.EntryVisibilityAuto, false))
	if !errors.Is(err, errProbeCommittedObserverFailure) {
		t.Fatalf("cache-warning steer err = %v, want committed append observer error propagated", err)
	}
}

func TestRunStepLoopBailsOnCanceledContextWithoutModelCall(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("should-not-run"), Phase: textutil.Value(llm.MessagePhaseFinal)}},
		},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := eng.runStepLoop(ctx, runtimeTestStepID("step-1"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runStepLoop err = %v, want context.Canceled", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("model calls = %d, want 0 (canceled step must not call the model)", len(client.calls))
	}
}
