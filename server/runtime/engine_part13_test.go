package runtime

import (
	"context"
	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	triggerhandofftool "core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRunStepLoopDoesNotDuplicateCompactionSoonReminderAfterAutoCompactionIsDisabled(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "checking", Phase: llm.MessagePhaseCommentary},
				ToolCalls: []llm.ToolCall{{ID: "call_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
				Usage:     llm.Usage{InputTokens: 100, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
				Usage:     llm.Usage{InputTokens: 920, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "next", Phase: llm.MessagePhaseFinal},
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
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	if _, err := eng.runStepLoop(context.Background(), "step-1"); err != nil {
		t.Fatalf("first runStepLoop: %v", err)
	}
	if reminders := countCompactionSoonReminderWarnings(eng, eng.ChatSnapshot()); reminders != 1 {
		t.Fatalf("expected one reminder after first run, got %d entries=%+v", reminders, eng.ChatSnapshot().Entries)
	}

	changed, enabled := eng.SetAutoCompactionEnabled(false)
	if !changed || enabled {
		t.Fatalf("expected auto compaction toggle off, changed=%v enabled=%v", changed, enabled)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "continue"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	msg, err := eng.runStepLoop(context.Background(), "step-2")
	if err != nil {
		t.Fatalf("second runStepLoop: %v", err)
	}
	if msg.Content != "next" {
		t.Fatalf("unexpected second assistant message: %+v", msg)
	}
	if len(client.calls) != 3 {
		t.Fatalf("expected three model requests across both runs, got %d", len(client.calls))
	}

	remindersInThirdRequest := 0
	for _, reqMsg := range requestMessages(client.calls[2]) {
		if reqMsg.Role == llm.RoleDeveloper && reqMsg.MessageType == llm.MessageTypeCompactionSoonReminder {
			remindersInThirdRequest++
		}
	}
	if remindersInThirdRequest != 1 {
		t.Fatalf("expected exactly one historical reminder in request while disabled, got %d messages=%+v", remindersInThirdRequest, requestMessages(client.calls[2]))
	}
	if reminders := countCompactionSoonReminderWarnings(eng, eng.ChatSnapshot()); reminders != 1 {
		t.Fatalf("expected reminder not to duplicate while disabled, got %d entries=%+v", reminders, eng.ChatSnapshot().Entries)
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

func TestCompactionSoonReminderIncludesTriggerHandoffAdditionWhenConfigured(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 890, WindowTokens: 2_000})

	if err := newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), "step-1"); err != nil {
		t.Fatalf("append reminder: %v", err)
	}

	reminders := 0
	for _, entry := range eng.ChatSnapshot().Entries {
		if entry.Role == "warning" && entry.MessageType == llm.MessageTypeCompactionSoonReminder {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("expected enabled reminder text once, got %d entries=%+v", reminders, eng.ChatSnapshot().Entries)
	}
}

func TestCompactionSoonReminderRechecksPreciselyAfterTranscriptMutation(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 840, contextWindow: 2_000}
	eng := mustNewHandoffTestEngine(t, store, client, Config{
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 860, WindowTokens: 2_000})

	if err := newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), "step-1"); err != nil {
		t.Fatalf("reminder below exact threshold: %v", err)
	}
	if client.countCalls != 1 {
		t.Fatalf("expected first reminder probe to count precisely once, got %d", client.countCalls)
	}
	if eng.compactionRuntimeState().SoonReminderIssued() {
		t.Fatal("did not expect handoff tool to become enabled below the exact reminder threshold")
	}

	client.inputTokenCount = 860
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Content: "mutation"}})); err != nil {
		t.Fatalf("append mutation: %v", err)
	}
	if err := newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), "step-2"); err != nil {
		t.Fatalf("reminder above exact threshold after mutation: %v", err)
	}
	if client.countCalls != 2 {
		t.Fatalf("expected transcript mutation to force a fresh precise reminder check, got %d calls", client.countCalls)
	}
	if !eng.compactionRuntimeState().SoonReminderIssued() {
		t.Fatal("expected reminder to enable trigger_handoff after exact recount")
	}
	reminders := 0
	for _, entry := range eng.ChatSnapshot().Entries {
		if entry.Role == "warning" && entry.MessageType == llm.MessageTypeCompactionSoonReminder {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("expected one reminder after exact recount, got %d entries=%+v", reminders, eng.ChatSnapshot().Entries)
	}
}

func TestTriggerHandoffFailsBeforeReminder(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})

	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call-handoff-1", Name: string(toolspec.ToolTriggerHandoff)}, "", "")
	if !errors.Is(err, errHandoffTooEarly) {
		t.Fatalf("expected errHandoffTooEarly, got %v", err)
	}
}

func TestTriggerHandoffFailsWhenAutoCompactionDisabled(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewHandoffTestEngine(t, store, &fakeClient{}, Config{})
	eng.compactionRuntimeState().SetSoonReminderIssued(true)
	changed, enabled := eng.SetAutoCompactionEnabled(false)
	if !changed || enabled {
		t.Fatalf("expected auto compaction toggle off, changed=%v enabled=%v", changed, enabled)
	}

	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call-handoff-1", Name: string(toolspec.ToolTriggerHandoff)}, "", "")
	if !errors.Is(err, errHandoffDisabledByUser) {
		t.Fatalf("expected errHandoffDisabledByUser, got %v", err)
	}
}

func TestTriggerHandoffSchedulesCompactionAndAppendsFutureMessageWithoutManualCarryover(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "summary"}}},
	}
	eng := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)
	activeCall := llm.ToolCall{ID: "call-handoff-1", Name: string(toolspec.ToolTriggerHandoff), Input: json.RawMessage(`{"summarizer_prompt":"keep API details","future_agent_message":"resume with tests"}`)}

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
	if len(client.calls) != 1 {
		t.Fatalf("expected one local-summary model call, got %d", len(client.calls))
	}

	foundPrompt := false
	for _, item := range client.calls[0].Items {
		if item.Type == llm.ResponseItemTypeMessage && item.Role == llm.RoleDeveloper && item.Content == compactionInstructions("keep API details") {
			foundPrompt = true
			break
		}
	}
	if !foundPrompt {
		t.Fatalf("expected handoff to reuse compaction instructions, got %+v", client.calls[0].Items)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	foundFutureMessage := false
	foundManualCarryover := false
	for _, message := range messages {
		if message.MessageType == llm.MessageTypeHandoffFutureMessage && message.Content == prompts.FormatHandoffFutureAgentMessage("resume with tests") {
			foundFutureMessage = true
		}
		if message.MessageType == llm.MessageTypeManualCompactionCarryover {
			foundManualCarryover = true
		}
	}
	if !foundFutureMessage {
		t.Fatalf("expected future-agent message in history, got %+v", messages)
	}
	if foundManualCarryover {
		t.Fatalf("did not expect manual compaction carryover for trigger_handoff, got %+v", messages)
	}

	entries := eng.ChatSnapshot().Entries
	foundDeveloperContext := false
	for _, entry := range entries {
		if entry.Role == string(transcript.EntryRoleDeveloperContext) && entry.Text == prompts.FormatHandoffFutureAgentMessage("resume with tests") {
			foundDeveloperContext = true
		}
		if entry.Role == string(transcript.EntryRoleManualCompactionCarryover) {
			t.Fatalf("did not expect manual carryover transcript entry for trigger_handoff, got %+v", entries)
		}
	}
	if !foundDeveloperContext {
		t.Fatalf("expected future-agent message to be detail-only developer context, got %+v", entries)
	}
}

func TestPrepareModelTurnSkipsAutoCompactionAfterPendingHandoffCompaction(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "handoff summary"},
			Usage:     llm.Usage{InputTokens: 1_900, WindowTokens: 2_000},
		}},
		inputTokenCount: 1_900,
	}
	eng := mustNewHandoffTestEngine(t, store, client, Config{
		CompactionMode:        "local",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 1_900, WindowTokens: 2_000})
	eng.handoffRuntimeState().QueueRequest("keep runtime details", "")

	executor := &defaultStepExecutor{engine: eng}
	if err := executor.prepareModelTurn(context.Background(), "step-1"); err != nil {
		t.Fatalf("prepare model turn: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected only pending handoff compaction call, got %d calls", len(client.calls))
	}
}

func TestPrepareModelTurnMaterializesWorktreeReminderAfterPendingHandoffCompaction(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	store := mustCreateTestSession(t)
	mustSetWorktreeReminderState(t, store, session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeEnter,
		Branch:        "feature/handoff",
		WorktreePath:  "/tmp/wt-handoff",
		WorkspaceRoot: "/tmp/workspace",
		EffectiveCwd:  "/tmp/wt-handoff",
	})

	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "handoff summary"},
			Usage:     llm.Usage{InputTokens: 1_900, WindowTokens: 2_000},
		}},
		inputTokenCount: 1_900,
	}
	eng := mustNewHandoffTestEngine(t, store, client, Config{
		CompactionMode:        "local",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 1_900, WindowTokens: 2_000})
	eng.handoffRuntimeState().QueueRequest("keep runtime details", "")

	executor := &defaultStepExecutor{engine: eng}
	if err := executor.prepareModelTurn(context.Background(), "step-1"); err != nil {
		t.Fatalf("prepare model turn: %v", err)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	reminderCount := 0
	for _, message := range messages {
		if message.Role == llm.RoleDeveloper && message.MessageType == llm.MessageTypeWorktreeMode {
			reminderCount++
			if !strings.Contains(message.Content, "feature/handoff") {
				t.Fatalf("unexpected worktree reminder content: %q", message.Content)
			}
		}
	}
	if reminderCount != 1 {
		t.Fatalf("expected one materialized worktree reminder after handoff compaction, got %d messages=%+v", reminderCount, messages)
	}
	state := store.Meta().WorktreeReminder
	if state == nil || !state.HasIssuedInGeneration {
		t.Fatalf("expected issued reminder state after handoff compaction, got %+v", state)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected only handoff compaction call, got %d calls", len(client.calls))
	}
}

func TestPrepareModelTurnHandoffReminderPersistenceFailureRetriesWithoutDuplicate(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	observer := &failOnIssuedWorktreeReminderObservation{}
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir, session.WithPersistenceObserver(observer))
	mustSetWorktreeReminderState(t, store, session.WorktreeReminderState{
		Mode:          session.WorktreeReminderModeEnter,
		Branch:        "feature/handoff-fail",
		WorktreePath:  "/tmp/wt-handoff-fail",
		WorkspaceRoot: "/tmp/workspace",
		EffectiveCwd:  "/tmp/wt-handoff-fail",
	})
	client := &fakeCompactionClient{
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "handoff summary"}, Usage: llm.Usage{InputTokens: 1_900, WindowTokens: 2_000}},
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "handoff summary retry"}, Usage: llm.Usage{InputTokens: 1_900, WindowTokens: 2_000}},
		},
		inputTokenCount: 1_900,
	}
	eng := mustNewHandoffTestEngine(t, store, client, Config{
		CompactionMode:        "local",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 1_900, WindowTokens: 2_000})
	eng.handoffRuntimeState().QueueRequest("keep runtime details", "")
	executor := &defaultStepExecutor{engine: eng}

	if err := executor.prepareModelTurn(context.Background(), "step-1"); err == nil || !strings.Contains(err.Error(), "persist observer failed") {
		t.Fatalf("prepare error = %v, want reminder state persistence failure", err)
	}
	assertWorktreeReminderEntryCount(t, eng.ChatSnapshot(), 1)

	eng.handoffRuntimeState().QueueRequest("keep runtime details", "")
	if err := executor.prepareModelTurn(context.Background(), "step-2"); err != nil {
		t.Fatalf("retry prepare model turn: %v", err)
	}
	assertWorktreeReminderEntryCount(t, eng.ChatSnapshot(), 1)
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
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
	}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		CompactionMode: "local",
		EnabledTools:   []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch, toolspec.ToolTriggerHandoff},
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_tool_retry", Name: string(toolspec.ToolTriggerHandoff)}, "keep API details", "")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err != nil {
		t.Fatalf("apply pending handoff: %v", err)
	}
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
			foundFailedOutputs[item.CallID] = true
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
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_empty_id", Name: string(toolspec.ToolTriggerHandoff)}, "keep API details", "resume with tests")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); !errors.Is(err, errLocalCompactionToolCallEmptyID) {
		t.Fatalf("expected errLocalCompactionToolCallEmptyID, got %v", err)
	}
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
				CustomInput: "*** Begin Patch\n*** End Patch",
			}},
			Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
	}}
	eng := mustNewFakeToolEngine(t, store, client, Config{
		CompactionMode: "local",
		EnabledTools:   []toolspec.ID{toolspec.ToolPatch, toolspec.ToolTriggerHandoff},
	}, toolspec.ToolPatch)
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_custom_tool_retry", Name: string(toolspec.ToolTriggerHandoff)}, "keep API details", "")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err != nil {
		t.Fatalf("apply pending handoff: %v", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected local summary retry after custom tool call, got %d requests", len(client.calls))
	}
	assertRequestsPreserveCacheIdentity(t, client.calls[0], client.calls[1])

	foundCustomFailedOutput := false
	for _, item := range client.calls[1].Items {
		if item.Type != llm.ResponseItemTypeCustomToolOutput || item.CallID != "call_custom_summary_tool" {
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
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_second_failure", Name: string(toolspec.ToolTriggerHandoff)}, "keep API details", "resume with tests")
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); !errors.Is(err, errLocalCompactionAttemptedToolCalls) {
		t.Fatalf("expected errLocalCompactionAttemptedToolCalls, got %v", err)
	}
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
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
	}}
	eng := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

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
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
		Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err != nil {
		t.Fatalf("retry pending handoff: %v", err)
	}
	if eng.handoffRuntimeState().RequestSnapshot() != nil {
		t.Fatalf("expected successful retry to clear pending handoff, got %+v", eng.handoffRuntimeState().RequestSnapshot())
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	foundFutureMessage := false
	for _, message := range messages {
		if message.MessageType == llm.MessageTypeHandoffFutureMessage && message.Content == prompts.FormatHandoffFutureAgentMessage("resume with tests") {
			foundFutureMessage = true
			break
		}
	}
	if !foundFutureMessage {
		t.Fatalf("expected successful retry to append future-agent message, got %+v", messages)
	}
}

func TestPendingTriggerHandoffRetriesFutureMessageAfterAppendFailureWithoutRecompaction(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
		Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}}
	eng := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)
	futureAgentMessage := "resume \"with tests\"\nthen inspect logs"

	_, _, err := eng.TriggerHandoff(context.Background(), "step-1", llm.ToolCall{ID: "call_handoff_append_retry", Name: string(toolspec.ToolTriggerHandoff)}, "keep API details", futureAgentMessage)
	if err != nil {
		t.Fatalf("trigger handoff: %v", err)
	}

	appendFailures := 0
	eng.beforePersistMessage = func(msg llm.Message) error {
		if msg.MessageType != llm.MessageTypeHandoffFutureMessage || appendFailures > 0 {
			return nil
		}
		appendFailures++
		return errors.New("synthetic future-message append failure")
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err == nil {
		t.Fatal("expected first pending handoff attempt to fail while appending future-agent message")
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected exactly one compaction summary call after append failure, got %d", len(client.calls))
	}
	if eng.handoffRuntimeState().RequestSnapshot() != nil {
		t.Fatalf("expected compaction-success path to consume original handoff request, got %+v", eng.handoffRuntimeState().RequestSnapshot())
	}
	// The retry queue keeps the raw tool argument so retry emission cannot wrap
	// already-formatted future-agent context a second time.
	if got, want := eng.handoffRuntimeState().FutureMessageSnapshot(), futureAgentMessage; got != want {
		t.Fatalf("pending future-agent message after append failure = %q, want %q", got, want)
	}

	eng.beforePersistMessage = nil
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err != nil {
		t.Fatalf("retry pending future-agent message append: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected retry after future-message append failure not to re-run compaction, got %d compaction calls", len(client.calls))
	}
	if got := eng.handoffRuntimeState().FutureMessageSnapshot(); got != "" {
		t.Fatalf("expected successful retry to clear pending future-agent message, got %q", got)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	foundFutureMessage := false
	for _, message := range messages {
		if message.MessageType == llm.MessageTypeHandoffFutureMessage && message.Content == prompts.FormatHandoffFutureAgentMessage(futureAgentMessage) {
			foundFutureMessage = true
			break
		}
	}
	if !foundFutureMessage {
		t.Fatalf("expected successful retry to append future-agent message after append failure, got %+v", messages)
	}
}

func TestReopenedSessionAfterTriggerHandoffFutureMessageAppendFailureRetriesWithoutRecompaction(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
		Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}}
	eng := mustNewHandoffTestEngine(t, store, client, Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	handoffCall := llm.ToolCall{
		ID:    "call_handoff_reopen_future_retry",
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
	eng.handoffRuntimeState().QueueRequest("keep API details", "resume after restart")

	eng.beforePersistMessage = func(msg llm.Message) error {
		if msg.MessageType == llm.MessageTypeHandoffFutureMessage {
			return errors.New("synthetic future-message append failure")
		}
		return nil
	}
	if _, err := eng.applyPendingHandoffIfNeeded(context.Background(), "step-1"); err == nil {
		t.Fatal("expected handoff future-message append to fail")
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected exactly one compaction summary call before reopen, got %d", len(client.calls))
	}
	if eng.handoffRuntimeState().RequestSnapshot() != nil {
		t.Fatalf("expected successful compaction to consume queued handoff request before reopen, got %+v", eng.handoffRuntimeState().RequestSnapshot())
	}

	reopenedStore, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	resumedClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "resumed", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{InputTokens: 300, WindowTokens: 2_000},
	}}}
	restored := mustNewHandoffTestEngine(t, reopenedStore, resumedClient, Config{})
	if restored.handoffRuntimeState().RequestSnapshot() != nil {
		t.Fatalf("did not expect restore to requeue handoff after successful compaction, got %+v", restored.handoffRuntimeState().RequestSnapshot())
	}
	if got, want := restored.handoffRuntimeState().FutureMessageSnapshot(), "resume after restart"; got != want {
		t.Fatalf("pending future-agent message after reopen = %q, want %q", got, want)
	}

	msg, err := restored.SubmitUserMessage(context.Background(), "continue")
	if err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if msg.Content != "resumed" {
		t.Fatalf("assistant content = %q, want resumed", msg.Content)
	}
	if len(resumedClient.calls) != 1 {
		t.Fatalf("expected reopened retry to append future-agent message without re-running compaction, got %d requests", len(resumedClient.calls))
	}
	if got, want := resumedClient.calls[0].SessionID, restored.SessionID(); got != want {
		t.Fatalf("expected reopened request session id to stay on the main conversation after restored handoff compaction, got %q want %q", got, want)
	}
	if got, want := resumedClient.calls[0].PromptCacheKey, conversationPromptCacheKey(restored.SessionID(), restored.compactionRuntimeState().Count()); got != want {
		t.Fatalf("expected reopened request prompt cache key to stay rotated after restored handoff compaction, got %q want %q", got, want)
	}
	foundFuture := false
	for _, item := range resumedClient.calls[0].Items {
		if item.Type == llm.ResponseItemTypeMessage && item.MessageType == llm.MessageTypeHandoffFutureMessage && item.Content == prompts.FormatHandoffFutureAgentMessage("resume after restart") {
			foundFuture = true
			break
		}
	}
	if !foundFuture {
		t.Fatalf("expected reopened request to include retried future-agent message, items=%+v", resumedClient.calls[0].Items)
	}
}

func TestRunStepLoopTriggerHandoffOmitsCallAndOutputFromFollowUpRequestAndKeepsFutureMessage(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "handing off", Phase: llm.MessagePhaseCommentary},
				ToolCalls: []llm.ToolCall{{
					ID:    "call_handoff_1",
					Name:  string(toolspec.ToolTriggerHandoff),
					Input: json.RawMessage(`{"summarizer_prompt":"keep API details","future_agent_message":"resume with tests"}`),
				}},
				Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
				Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
				Usage:     llm.Usage{InputTokens: 300, WindowTokens: 2_000},
			},
		},
	}

	var eng *Engine
	registry := tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}},
		tools.HandlerRegistration{ID: toolspec.ToolTriggerHandoff, Handler: triggerhandofftool.NewTriggerHandoffTool(func() triggerhandofftool.TriggerHandoffController { return eng })},
	)
	eng = mustNewTestEngine(t, store, client, registry, Config{
		CompactionMode: "local",
		EnabledTools:   []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolTriggerHandoff},
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	msg, err := eng.runStepLoop(context.Background(), "step-1")
	if err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("unexpected final assistant message: %+v", msg)
	}
	if len(client.calls) != 3 {
		t.Fatalf("expected tool call, local compaction summary, and follow-up requests, got %d", len(client.calls))
	}
	if got, want := client.calls[2].SessionID, eng.SessionID(); got != want {
		t.Fatalf("expected follow-up request session id to stay on the main conversation after handoff compaction, got %q want %q", got, want)
	}
	if got, want := client.calls[2].PromptCacheKey, conversationPromptCacheKey(eng.SessionID(), eng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("expected follow-up request prompt cache key to rotate after handoff compaction, got %q want %q", got, want)
	}

	followUp := client.calls[2]
	foundCall := false
	foundOutput := false
	futureIdx := -1
	for idx, item := range followUp.Items {
		switch {
		case item.Type == llm.ResponseItemTypeFunctionCall && item.CallID == "call_handoff_1":
			foundCall = true
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == "call_handoff_1":
			foundOutput = true
		case item.Type == llm.ResponseItemTypeMessage && item.MessageType == llm.MessageTypeHandoffFutureMessage && item.Content == prompts.FormatHandoffFutureAgentMessage("resume with tests"):
			futureIdx = idx
		}
	}
	if foundCall || foundOutput {
		t.Fatalf("expected follow-up request to omit trigger_handoff call/output items entirely, foundCall=%v foundOutput=%v items=%+v", foundCall, foundOutput, followUp.Items)
	}
	if futureIdx < 0 {
		t.Fatalf("expected future-agent message in follow-up request, items=%+v", followUp.Items)
	}
}

func TestRunStepLoopInjectsReminderBeforeTriggerHandoffAndOmitsCallOutputFromFollowUp(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "handing off", Phase: llm.MessagePhaseCommentary},
				ToolCalls: []llm.ToolCall{{
					ID:    "call_handoff_2",
					Name:  string(toolspec.ToolTriggerHandoff),
					Input: json.RawMessage(`{"future_agent_message":"resume with tests"}`),
				}},
				Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
				Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
				Usage:     llm.Usage{InputTokens: 300, WindowTokens: 2_000},
			},
		},
	}

	var eng *Engine
	registry := tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}},
		tools.HandlerRegistration{ID: toolspec.ToolTriggerHandoff, Handler: triggerhandofftool.NewTriggerHandoffTool(func() triggerhandofftool.TriggerHandoffController { return eng })},
	)
	eng = mustNewTestEngine(t, store, client, registry, Config{
		CompactionMode:        "local",
		ContextWindowTokens:   20_000,
		AutoCompactTokenLimit: 10_000,
		EnabledTools:          []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolTriggerHandoff},
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 8_900, WindowTokens: 20_000})

	msg, err := eng.runStepLoop(context.Background(), "step-1")
	if err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("unexpected final assistant message: %+v", msg)
	}
	if len(client.calls) != 3 {
		t.Fatalf("expected trigger request, local compaction summary, and follow-up requests, got %d", len(client.calls))
	}
	if got, want := client.calls[2].SessionID, eng.SessionID(); got != want {
		t.Fatalf("expected follow-up request session id to stay on the main conversation after handoff compaction, got %q want %q", got, want)
	}
	if got, want := client.calls[2].PromptCacheKey, conversationPromptCacheKey(eng.SessionID(), eng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("expected follow-up request prompt cache key to rotate after handoff compaction, got %q want %q", got, want)
	}

	remindersInFirstRequest := 0
	for _, reqMsg := range requestMessages(client.calls[0]) {
		if reqMsg.Role == llm.RoleDeveloper && reqMsg.MessageType == llm.MessageTypeCompactionSoonReminder {
			remindersInFirstRequest++
		}
	}
	if remindersInFirstRequest != 1 {
		t.Fatalf("expected exactly one pre-request reminder before trigger_handoff, got %d messages=%+v", remindersInFirstRequest, requestMessages(client.calls[0]))
	}

	followUp := client.calls[2]
	foundCall := false
	foundOutput := false
	futureIdx := -1
	for idx, item := range followUp.Items {
		switch {
		case item.Type == llm.ResponseItemTypeFunctionCall && item.CallID == "call_handoff_2":
			foundCall = true
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == "call_handoff_2":
			foundOutput = true
		case item.Type == llm.ResponseItemTypeMessage && item.MessageType == llm.MessageTypeHandoffFutureMessage && item.Content == prompts.FormatHandoffFutureAgentMessage("resume with tests"):
			futureIdx = idx
		}
	}
	if foundCall || foundOutput {
		t.Fatalf("expected follow-up request to omit trigger_handoff call/output items entirely, foundCall=%v foundOutput=%v items=%+v", foundCall, foundOutput, followUp.Items)
	}
	if futureIdx < 0 {
		t.Fatalf("expected future-agent message in follow-up request, items=%+v", followUp.Items)
	}
}

func TestReopenedSessionAfterTriggerHandoffUsesRotatedRequestSessionAndOmitsLingeringCallOutput(t *testing.T) {
	store := mustCreateTestSession(t)

	firstClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "handing off", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_handoff_restart",
				Name:  string(toolspec.ToolTriggerHandoff),
				Input: json.RawMessage(`{"future_agent_message":"resume after restart"}`),
			}},
			Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "condensed summary"},
			Usage:     llm.Usage{InputTokens: 200, WindowTokens: 2_000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{InputTokens: 300, WindowTokens: 2_000},
		},
	}}

	var eng *Engine
	registry := tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}},
		tools.HandlerRegistration{ID: toolspec.ToolTriggerHandoff, Handler: triggerhandofftool.NewTriggerHandoffTool(func() triggerhandofftool.TriggerHandoffController { return eng })},
	)
	eng = mustNewTestEngine(t, store, firstClient, registry, Config{
		CompactionMode: "local",
		EnabledTools:   []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolTriggerHandoff},
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	// Match real startup semantics: the initial runtime session has already injected
	// AGENTS/environment context before any reopen-and-resume path is exercised.
	// Without this seed, the first post-reopen SubmitUserMessage legitimately performs
	// that one-time injection and can trigger an extra compaction turn under this
	// tiny test window, which makes the test fail for the wrong reason.
	if err := eng.steerBaseMetaContextIfNeeded("seed-meta"); err != nil {
		t.Fatalf("inject agents: %v", err)
	}
	eng.compactionRuntimeState().SetSoonReminderIssued(true)

	if _, err := eng.runStepLoop(context.Background(), "step-1"); err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}

	reopenedStore, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	resumedClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "resumed", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 2_000},
	}}}
	restored := mustNewHandoffTestEngine(t, reopenedStore, resumedClient, Config{})

	msg, err := restored.SubmitUserMessage(context.Background(), "continue")
	if err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if msg.Content != "resumed" {
		t.Fatalf("assistant content = %q, want resumed", msg.Content)
	}
	if len(resumedClient.calls) != 1 {
		t.Fatalf("expected one resumed model call, got %d", len(resumedClient.calls))
	}
	if got, want := resumedClient.calls[0].SessionID, restored.SessionID(); got != want {
		t.Fatalf("expected resumed request session id to stay on the main conversation after restore, got %q want %q", got, want)
	}
	if got, want := resumedClient.calls[0].PromptCacheKey, conversationPromptCacheKey(restored.SessionID(), restored.compactionRuntimeState().Count()); got != want {
		t.Fatalf("expected resumed request prompt cache key to stay rotated after restore, got %q want %q", got, want)
	}
	for _, item := range resumedClient.calls[0].Items {
		switch {
		case item.Type == llm.ResponseItemTypeFunctionCall && item.CallID == "call_handoff_restart":
			t.Fatalf("did not expect reopened request to include lingering trigger_handoff call item, items=%+v", resumedClient.calls[0].Items)
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == "call_handoff_restart":
			t.Fatalf("did not expect reopened request to include lingering trigger_handoff output item, items=%+v", resumedClient.calls[0].Items)
		}
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
	store := mustCreateTestSessionAt(t, dir, session.WithPersistenceObserver(observer))
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	observer.armed = true
	err := eng.steer("step-1", steerCacheWarningIntent(transcript.CacheWarning{Reason: transcript.CacheWarningReasonCompaction}, transcript.EntryVisibilityAuto, false))
	if !errors.Is(err, errProbeCommittedObserverFailure) {
		t.Fatalf("cache-warning steer err = %v, want committed append observer error propagated", err)
	}
}

func TestRunStepLoopBailsOnCanceledContextWithoutModelCall(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "should-not-run", Phase: llm.MessagePhaseFinal}},
		},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := eng.runStepLoop(ctx, "step-1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runStepLoop err = %v, want context.Canceled", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("model calls = %d, want 0 (canceled step must not call the model)", len(client.calls))
	}
}
