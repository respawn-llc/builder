package runtime

import (
	"context"
	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
	"encoding/json"
	"strings"
	"testing"
)

func TestShouldCompactBeforeUserMessageSkipsExactCountWhenProviderOverrideDisablesIt(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 960, contextWindow: 1000}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		AutoCompactTokenLimit: 950,
		ContextWindowTokens:   1000,
		ProviderCapabilitiesOverride: &llm.ProviderCapabilities{
			ProviderID:                     "openai",
			SupportsResponsesAPI:           true,
			SupportsResponsesCompact:       true,
			SupportsRequestInputTokenCount: false,
			SupportsPromptCacheKey:         true,
			SupportsNativeWebSearch:        true,
			SupportsReasoningEncrypted:     true,
			SupportsServerSideContextEdit:  true,
			IsOpenAIFirstParty:             true,
		},
		PreSubmitCompactionLeadTokens: 50,
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("a", 3400))}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	shouldCompact, err := eng.ShouldCompactBeforeUserMessage(context.Background(), strings.Repeat("b", 400))
	if err != nil {
		t.Fatalf("ShouldCompactBeforeUserMessage: %v", err)
	}
	if !shouldCompact {
		t.Fatal("expected fallback estimator to trigger pre-submit compaction when provider override disables exact counting")
	}
	if client.countCalls != 0 {
		t.Fatalf("count calls=%d, want 0 when provider override disables exact counting", client.countCalls)
	}
}

func TestShouldCompactBeforeUserMessageSkipsExactCountWhenLockedContractDisablesIt(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model: "gpt-5",
		ProviderContract: session.LockedProviderCapabilities{
			ProviderID:                        "openai",
			SupportsRequestInputTokenCount:    false,
			HasSupportsRequestInputTokenCount: true,
		},
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}

	client := &preciseCompactionClient{inputTokenCount: 960, contextWindow: 1000}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:                         "gpt-5",
		AutoCompactTokenLimit:         950,
		ContextWindowTokens:           1000,
		PreSubmitCompactionLeadTokens: 50,
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("a", 3400))}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	shouldCompact, err := eng.ShouldCompactBeforeUserMessage(context.Background(), strings.Repeat("b", 400))
	if err != nil {
		t.Fatalf("ShouldCompactBeforeUserMessage: %v", err)
	}
	if !shouldCompact {
		t.Fatal("expected fallback estimator to trigger pre-submit compaction when locked contract disables exact counting")
	}
	if client.countCalls != 0 {
		t.Fatalf("count calls=%d, want 0 when locked contract disables exact counting", client.countCalls)
	}
}

func TestCompactionSoonReminderStaysSingleShotAfterReEnablingAutoCompactionAboveReminderBand(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 890, WindowTokens: 2_000})

	changed, enabled := eng.SetAutoCompactionEnabled(false)
	if !changed || enabled {
		t.Fatalf("expected auto compaction toggle off, changed=%v enabled=%v", changed, enabled)
	}
	stepOffID := runtimeTestStepID("step-off")
	if err := runTestActiveStep(eng, stepOffID, func() error {
		return newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), stepOffID)
	}); err != nil {
		t.Fatalf("reminder while disabled: %v", err)
	}

	snap := eng.ChatSnapshot()
	if len(snap.Entries) != 1 {
		t.Fatalf("expected only seed entry while disabled, got %+v", snap.Entries)
	}

	changed, enabled = eng.SetAutoCompactionEnabled(true)
	if !changed || !enabled {
		t.Fatalf("expected auto compaction toggle on, changed=%v enabled=%v", changed, enabled)
	}
	stepOnID := runtimeTestStepID("step-on")
	if err := runTestActiveStep(eng, stepOnID, func() error {
		return newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), stepOnID)
	}); err != nil {
		t.Fatalf("reminder after re-enable: %v", err)
	}
	duplicateStepID := runtimeTestStepID("step-on-duplicate")
	if err := runTestActiveStep(eng, duplicateStepID, func() error {
		return newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), duplicateStepID)
	}); err != nil {
		t.Fatalf("duplicate reminder check: %v", err)
	}

	snap = eng.ChatSnapshot()
	reminders := 0
	for _, entry := range snap.Entries {
		if entry.Role == "warning" && entry.MessageType == llm.MessageTypeCompactionSoonReminder {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("expected one reminder after re-enable, got %d entries=%+v", reminders, snap.Entries)
	}

	eng.setLastUsage(llm.Usage{InputTokens: 800, WindowTokens: 2_000})
	resetStepID := runtimeTestStepID("step-reset")
	if err := runTestActiveStep(eng, resetStepID, func() error {
		return newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), resetStepID)
	}); err != nil {
		t.Fatalf("reset reminder state: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 860, WindowTokens: 2_000})
	reissueStepID := runtimeTestStepID("step-reissue")
	if err := runTestActiveStep(eng, reissueStepID, func() error {
		return newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), reissueStepID)
	}); err != nil {
		t.Fatalf("reissue reminder: %v", err)
	}

	snap = eng.ChatSnapshot()
	reminders = 0
	for _, entry := range snap.Entries {
		if entry.Role == "warning" && entry.MessageType == llm.MessageTypeCompactionSoonReminder {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("expected reminder to remain single-shot after falling below threshold, got %d entries=%+v", reminders, snap.Entries)
	}
}

func TestReopenedSessionRestoresCompactionSoonReminderIssuedState(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	if err := steerTestActiveStep(eng, "step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeCompactionSoonReminder), Content: textutil.Value(prompts.RenderCompactionSoonReminderPrompt(false, eng.estimatedToolCallsUntilForcedHandoff()))}})); err != nil {
		t.Fatalf("append reminder: %v", err)
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	restored := mustNewTestEngine(t, reopenedStore, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	restored.setLastUsage(llm.Usage{InputTokens: 890, WindowTokens: 2_000})
	if !restored.compactionRuntimeState().SoonReminderIssued() {
		t.Fatal("expected reopened session to restore reminder-issued state")
	}
	if !reopenedStore.Meta().CompactionSoonReminderIssued {
		t.Fatal("expected reopened session meta to persist reminder-issued state")
	}
	if err := newCompactionReminderCoordinator(restored).maybeAppend(context.Background(), "step-restore"); err != nil {
		t.Fatalf("reminder after reopen: %v", err)
	}
	if reminders := countCompactionSoonReminderWarnings(restored, restored.ChatSnapshot()); reminders != 1 {
		t.Fatalf("expected reopened session to avoid duplicate reminder, got %d entries=%+v", reminders, restored.ChatSnapshot().Entries)
	}
}

func TestForkedSessionBeforeReminderDoesNotCopyReminderIssuedState(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	if err := eng.persistCompactionSoonReminderIssued(true); err != nil {
		t.Fatalf("persist reminder-issued state: %v", err)
	}

	forkedStore, _, err := session.ForkAtUserMessage(mustMaterializeTestEventLog(t, store), userMessageSeqAt(t, store, 1), "Parent -> edit", sessioncontract.SessionCategoryMain)
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	if forkedStore.Meta().CompactionSoonReminderIssued {
		t.Fatal("expected fork before reminder to clear reminder-issued state")
	}
	forked := mustNewTestEngine(t, forkedStore, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if err != nil {
		t.Fatalf("restore forked engine: %v", err)
	}
	forked.setLastUsage(llm.Usage{InputTokens: 890, WindowTokens: 2_000})
	if forked.compactionRuntimeState().SoonReminderIssued() {
		t.Fatal("expected forked session before reminder to start with cleared reminder-issued state")
	}
	forkStepID := runtimeTestStepID("step-fork")
	if err := runTestActiveStep(forked, forkStepID, func() error {
		return newCompactionReminderCoordinator(forked).maybeAppend(context.Background(), forkStepID)
	}); err != nil {
		t.Fatalf("reminder after fork: %v", err)
	}
	if reminders := countCompactionSoonReminderWarnings(forked, forked.ChatSnapshot()); reminders != 1 {
		t.Fatalf("expected fork before reminder to allow a fresh reminder, got %d entries=%+v", reminders, forked.ChatSnapshot().Entries)
	}
}

func TestForkedSessionDoesNotCopyPersistedUsageState(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 410_000})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	if _, err := eng.recordLastUsage(llm.Usage{InputTokens: 900, WindowTokens: 410_000}); err != nil {
		t.Fatalf("record last usage: %v", err)
	}
	if store.Meta().UsageState == nil {
		t.Fatal("expected parent session to persist usage state")
	}

	forkedStore, _, err := session.ForkAtUserMessage(mustMaterializeTestEventLog(t, store), userMessageSeqAt(t, store, 1), "Parent -> edit", sessioncontract.SessionCategoryMain)
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	if forkedStore.Meta().UsageState != nil {
		t.Fatalf("expected forked session usage state cleared, got %+v", forkedStore.Meta().UsageState)
	}
}

func TestForkedSessionAfterReminderPreservesCompactionSoonReminderIssuedState(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	if err := eng.persistCompactionSoonReminderIssued(true); err != nil {
		t.Fatalf("persist reminder-issued state: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeCompactionSoonReminder), Content: textutil.Value("compact soon")}})); err != nil {
		t.Fatalf("append reminder message: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("after reminder")}})); err != nil {
		t.Fatalf("append second user message: %v", err)
	}

	forkedStore, _, err := session.ForkAtUserMessage(mustMaterializeTestEventLog(t, store), userMessageSeqAt(t, store, 2), "Parent -> edit", sessioncontract.SessionCategoryMain)
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	if !forkedStore.Meta().CompactionSoonReminderIssued {
		t.Fatal("expected fork after reminder to preserve reminder-issued state")
	}
}

func TestCompactionSoonReminderSkipsPreciseCountingWhenSuppressed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		compactionMode string
		disableAuto    bool
	}{
		{name: "auto compaction disabled", compactionMode: "local", disableAuto: true},
		{name: "compaction mode none", compactionMode: "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)

			client := &preciseCompactionClient{inputTokenCount: 890, contextWindow: 2_000}
			eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
				Model:                 "gpt-5",
				ContextWindowTokens:   2_000,
				AutoCompactTokenLimit: 1_000,
				CompactionMode:        tt.compactionMode,
			})
			if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
				t.Fatalf("append seed message: %v", err)
			}
			eng.setLastUsage(llm.Usage{InputTokens: 890, WindowTokens: 2_000})
			eng.compactionRuntimeState().SetSoonReminderIssued(true)

			if tt.disableAuto {
				changed, enabled := eng.SetAutoCompactionEnabled(false)
				if !changed || enabled {
					t.Fatalf("expected auto compaction toggle off, changed=%v enabled=%v", changed, enabled)
				}
			}

			if err := newCompactionReminderCoordinator(eng).maybeAppend(context.Background(), "suppressed"); err != nil {
				t.Fatalf("suppressed reminder check: %v", err)
			}
			if client.countCalls != 0 {
				t.Fatalf("expected suppressed reminder path to skip precise token counting, got %d calls", client.countCalls)
			}
			if got := len(eng.ChatSnapshot().Entries); got != 1 {
				t.Fatalf("expected no reminder entry while suppressed, got %d entries", got)
			}
			issued := eng.compactionRuntimeState().SoonReminderIssued()
			if !issued {
				t.Fatal("expected suppressed reminder path to preserve issued state")
			}
		})
	}
}

func TestRunStepLoopSkipsCompactionSoonReminderWhenImmediateAutoCompactionRuns(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)}}},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("seed")},
				{Type: llm.ResponseItemTypeCompaction, ID: textutil.Value("cmp_1"), EncryptedContent: textutil.Value("enc_1")},
			},
			Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
		}},
	}

	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   20_000,
		AutoCompactTokenLimit: 10_000,
		MaxTokens:             20,
		CompactionMode:        "native",
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 9_990, WindowTokens: 20_000})

	restoreStep := setTestActiveStep(eng, "step-1")
	msg, err := eng.runStepLoop(context.Background(), runtimeTestStepID("step-1"))
	restoreStep()
	if err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("unexpected assistant message: %+v", msg)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected one model request after compaction, got %d", len(client.calls))
	}
	for _, reqMsg := range requestMessages(client.calls[0]) {
		if reqMsg.Role == llm.RoleDeveloper && reqMsg.MessageType != nil && *reqMsg.MessageType == llm.MessageTypeCompactionSoonReminder {
			t.Fatalf("did not expect compaction-soon reminder in request after immediate auto-compaction, messages=%+v", requestMessages(client.calls[0]))
		}
	}

	snap := eng.ChatSnapshot()
	for _, entry := range snap.Entries {
		if entry.Role == "warning" && entry.MessageType == llm.MessageTypeCompactionSoonReminder {
			t.Fatalf("did not expect reminder in transcript after immediate auto-compaction, entries=%+v", snap.Entries)
		}
	}
}

func TestRunStepLoopInjectsCompactionSoonReminderBeforeFinalAnswerRequest(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{InputTokens: 890, WindowTokens: 2_000},
		}},
	}

	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 890, WindowTokens: 2_000})

	restoreStep := setTestActiveStep(eng, "step-1")
	msg, err := eng.runStepLoop(context.Background(), runtimeTestStepID("step-1"))
	restoreStep()
	if err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("unexpected assistant message: %+v", msg)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected exactly one model request, got %d", len(client.calls))
	}
	remindersInRequest := 0
	for _, reqMsg := range requestMessages(client.calls[0]) {
		if reqMsg.Role == llm.RoleDeveloper && reqMsg.MessageType != nil && *reqMsg.MessageType == llm.MessageTypeCompactionSoonReminder {
			remindersInRequest++
		}
	}
	if remindersInRequest != 1 {
		t.Fatalf("expected exactly one reminder in the request that produced the final answer, got %d messages=%+v", remindersInRequest, requestMessages(client.calls[0]))
	}

	snap := eng.ChatSnapshot()
	assistantIdx := -1
	reminderIdx := -1
	reminders := 0
	for idx, entry := range snap.Entries {
		if entry.Role == "assistant" && entry.Text == "done" {
			assistantIdx = idx
		}
		if entry.Role == "warning" && entry.MessageType == llm.MessageTypeCompactionSoonReminder {
			reminders++
			reminderIdx = idx
		}
	}
	if reminders != 1 {
		t.Fatalf("expected exactly one reminder entry, got %d entries=%+v", reminders, snap.Entries)
	}
	if assistantIdx < 0 || reminderIdx != assistantIdx-1 {
		t.Fatalf("expected reminder immediately before final assistant entry, assistantIdx=%d reminderIdx=%d entries=%+v", assistantIdx, reminderIdx, snap.Entries)
	}
}

func TestRunStepLoopAppendsCompactionSoonReminderImmediatelyAfterToolOutputBoundary(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("checking"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
				ToolCalls: []llm.ToolCall{{ID: "call_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
				Usage:     llm.Usage{InputTokens: 890, WindowTokens: 2_000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
				Usage:     llm.Usage{InputTokens: 920, WindowTokens: 2_000},
			},
		},
	}

	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	msg, err := runStepLoopInActiveTestRun(t, context.Background(), eng)
	if err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("unexpected assistant message: %+v", msg)
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected two model requests, got %d", len(client.calls))
	}
	remindersInSecondRequest := 0
	for _, reqMsg := range requestMessages(client.calls[1]) {
		if reqMsg.Role == llm.RoleDeveloper && reqMsg.MessageType != nil && *reqMsg.MessageType == llm.MessageTypeCompactionSoonReminder {
			remindersInSecondRequest++
		}
	}
	if remindersInSecondRequest != 1 {
		t.Fatalf("expected exactly one reminder in second request, got %d messages=%+v", remindersInSecondRequest, requestMessages(client.calls[1]))
	}

	snap := eng.ChatSnapshot()
	toolIdx := -1
	reminderIdx := -1
	reminders := 0
	for idx, entry := range snap.Entries {
		if strings.HasPrefix(entry.Role, "tool_result") {
			toolIdx = idx
		}
		if entry.Role == "warning" && entry.MessageType == llm.MessageTypeCompactionSoonReminder {
			reminders++
			reminderIdx = idx
		}
	}
	if reminders != 1 {
		t.Fatalf("expected exactly one reminder entry, got %d entries=%+v", reminders, snap.Entries)
	}
	if toolIdx < 0 || reminderIdx != toolIdx+1 {
		t.Fatalf("expected reminder immediately after tool output, toolIdx=%d reminderIdx=%d entries=%+v", toolIdx, reminderIdx, snap.Entries)
	}
}
