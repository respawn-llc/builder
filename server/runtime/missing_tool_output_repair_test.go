package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestMissingToolOutputRepairAppendsSyntheticOutputAndRetries(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	client := &fakeClient{
		errors: []error{&llm.APIStatusError{StatusCode: 400, Body: "tool call without output"}},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("repaired")},
			Usage:     llm.Usage{InputTokens: 10, OutputTokens: 2, WindowTokens: 100},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	message, err := eng.SubmitUserMessage(context.Background(), "continue")
	if err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if content, ok := textutil.OptionalExact(message.Content); !ok || content != "repaired" {
		t.Fatalf("assistant content = %q present=%t, want repaired", content, ok)
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d, want initial 400 plus repaired retry", len(client.calls))
	}
	if !repairRequestHasToolCall(client.calls[0].Items, "missing") ||
		!repairRequestHasToolCall(client.calls[1].Items, "missing") ||
		!repairRequestHasToolOutput(client.calls[1].Items, "missing") {
		t.Fatalf("repair must append an output without removing the original call")
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded repair records: %v", err)
	}
	var completion *storedToolCompletion
	var warning *storedLocalEntry
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			got, err := storedToolCompletionFromSessionRecord(payload)
			if err != nil {
				t.Fatalf("restore completion: %v", err)
			}
			if got.CallID == "missing" {
				completion = &got
			}
		case session.LocalEntryRecord:
			got, err := storedLocalEntryFromSessionRecord(payload)
			if err != nil {
				t.Fatalf("restore local entry: %v", err)
			}
			if got.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				warning = &got
			}
		}
	}
	if completion == nil || !completion.IsError {
		t.Fatalf("missing synthetic error completion: %+v", completion)
	}
	if warning == nil || strings.TrimSpace(warning.Text) == "" {
		t.Fatalf("missing operator-facing repair warning: %+v", warning)
	}
}

func TestMissingToolOutputRepairRetryIncludesQueuedSteering(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}

	queued := false
	var eng *Engine
	client := &hookClient{
		errors:   []error{&llm.APIStatusError{StatusCode: 400}},
		response: finalTextResponse("result"),
		beforeReturn: func() error {
			if queued {
				return nil
			}
			queued = true
			if _, accepted, err := eng.QueueUserMessageForActiveRun(
				context.Background(),
				"queued steering",
				runtimeids.NewRuntimeClientRequestID(),
				nil,
			); err != nil {
				return err
			} else if !accepted {
				return ErrNoActiveLiveRun
			}
			return nil
		},
	}
	eng = mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d, want initial 400 plus repaired retry", len(client.calls))
	}
	countUserItems := func(items []llm.ResponseItem) int {
		count := 0
		for _, item := range items {
			if item.Type == llm.ResponseItemTypeMessage && item.Role != nil && *item.Role == llm.RoleUser {
				count++
			}
		}
		return count
	}
	if got, want := countUserItems(client.calls[1].Items), countUserItems(client.calls[0].Items)+1; got != want {
		t.Fatalf("retry user-message count = %d, want queued steering to add one item to %d", got, want)
	}
}

func TestMissingToolOutputRepairLeavesUnrelated400Unrepaired(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{
		errors: []error{&llm.APIStatusError{StatusCode: 400, Body: "malformed request"}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); err == nil {
		t.Fatal("expected unrelated provider 400 to surface")
	}
	if len(client.calls) != 1 {
		t.Fatalf("model calls = %d, want no repair retry", len(client.calls))
	}
}

func TestRequiredToolChoiceRepairsDanglingOutputAndRebuildsRequest(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	client := &fakeClient{
		errors: []error{
			&llm.APIStatusError{StatusCode: 400},
			&llm.APIStatusError{StatusCode: 401},
		},
	}
	eng := mustNewExecTestEngine(
		t,
		store,
		client,
		Config{WorkflowRun: &workflowruntime.Config{
			CompletionMode: workflowruntime.CompletionModeTool,
			Contract: workflowruntime.CompletionContract{
				RunID: workflow.RunID("workflow-run"),
			},
		}},
	)

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); !llm.HasHTTPStatus(err, 401) {
		t.Fatalf("submit error = %v, want unrepaired HTTP 401 after repaired retry", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d, want initial 400 plus repaired retry", len(client.calls))
	}
	for index, call := range client.calls {
		if call.ToolChoiceMode != llm.ToolChoiceModeRequired {
			t.Fatalf("model call %d tool choice = %q, want required", index, call.ToolChoiceMode)
		}
	}
	if !repairRequestHasToolOutput(client.calls[1].Items, "missing") {
		t.Fatal("required-tool retry omitted the synthetic output")
	}
}

func TestRepairMissingToolOutputsByAppendingIsIdempotent(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	first, err := eng.repairMissingToolOutputsByAppending(textutil.Value("step"))
	if err != nil {
		t.Fatalf("first repair: %v", err)
	}
	second, err := eng.repairMissingToolOutputsByAppending(textutil.Value("step"))
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("repair counts = first:%d second:%d, want one append then no-op", first, second)
	}
}

func TestRepairMissingToolOutputsRetainsDanglingCallStepIdentity(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, chatStoreTestStepID, llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.repairMissingToolOutputsByAppending(nil); err != nil {
		t.Fatalf("repair: %v", err)
	}
	record, _ := repairCompletionRecord(t, store, "missing")
	stepID := record.StepID()
	if stepID == nil || *stepID != chatStoreTestStepID {
		t.Fatalf("repair step id = %v, want original call step %q", stepID, chatStoreTestStepID)
	}
}

func TestRepairMissingToolOutputsUsesRepairStepForUnownedLegacyCall(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	legacyMessage, err := sessionMessageRecordFromLLM(llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "unowned", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	})
	if err != nil {
		t.Fatalf("adapt unowned dangling tool call: %v", err)
	}
	if _, _, err := mustMaterializeTestEventLog(t, store).AppendRecord(nil, legacyMessage); err != nil {
		t.Fatalf("append unowned dangling tool call: %v", err)
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.repairMissingToolOutputsByAppending(textutil.Value(chatStoreTestStepID)); err != nil {
		t.Fatalf("repair: %v", err)
	}
	record, _ := repairCompletionRecord(t, store, "unowned")
	stepID := record.StepID()
	if stepID == nil || *stepID != chatStoreTestStepID {
		t.Fatalf("repair step id = %v, want repair step %q", stepID, chatStoreTestStepID)
	}
}

func TestRepairMissingToolOutputsRejectsUnownedCallsBeforeAppending(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, chatStoreTestStepID, llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "owned", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append owned dangling tool call: %v", err)
	}
	legacyMessage, err := sessionMessageRecordFromLLM(llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "unowned", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	})
	if err != nil {
		t.Fatalf("adapt unowned dangling tool call: %v", err)
	}
	if _, _, err := mustMaterializeTestEventLog(t, store).AppendRecord(nil, legacyMessage); err != nil {
		t.Fatalf("append unowned dangling tool call: %v", err)
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	repaired, err := eng.repairMissingToolOutputsByAppending(nil)
	if err == nil {
		t.Fatal("repair accepted a dangling tool call with no recoverable step identity")
	}
	if repaired != 0 {
		t.Fatalf("repair count = %d, want no partial repair", repaired)
	}
	items := eng.transcriptRuntimeState().SnapshotItems()
	if repairRequestHasToolOutput(items, "owned") || repairRequestHasToolOutput(items, "unowned") {
		t.Fatal("repair appended output before validating every dangling call identity")
	}
}

func repairCompletionRecord(
	t *testing.T,
	store *session.Store,
	callID string,
) (session.EventRecord, storedToolCompletion) {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded repair records: %v", err)
	}
	for _, record := range window.Records {
		completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
		if !ok {
			continue
		}
		stored, err := storedToolCompletionFromSessionRecord(completion)
		if err != nil {
			t.Fatalf("restore completion: %v", err)
		}
		if stored.CallID == callID {
			return record, stored
		}
	}
	t.Fatalf("missing synthetic completion for call %q", callID)
	return session.EventRecord{}, storedToolCompletion{}
}

func TestRepairMissingToolOutputsDefersToPendingToolCallStarts(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.rememberPendingToolCallStarts(map[string]int{"missing": 1})

	repaired, err := eng.repairMissingToolOutputsByAppending(textutil.Value("step"))
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("repair count = %d, want no synthetic output while a real start is pending", repaired)
	}
	if repairRequestHasToolOutput(eng.transcriptRuntimeState().SnapshotItems(), "missing") {
		t.Fatal("pending real tool start was pre-empted by a synthetic output")
	}
}

func TestRepairMissingToolOutputsPersistSyntheticErrorPresentation(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:    "missing",
			Name:  "exec_command",
			Input: json.RawMessage(`{"cmd":"true"}`),
		}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.repairMissingToolOutputsByAppending(textutil.Value("step")); err != nil {
		t.Fatalf("repair: %v", err)
	}
	_, completion := repairCompletionRecord(t, store, "missing")
	if completion.Presentation == nil || !completion.Presentation.IsShell || completion.Presentation.Command == "" {
		t.Fatalf("synthetic completion presentation = %+v, want typed shell presentation", completion.Presentation)
	}
}

func TestCompactionMissingToolOutputRepairAppendsAndRetries(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	client := &fakeCompactionClient{
		compactionErrors: []error{&llm.APIStatusError{StatusCode: 400}, nil},
		compactionResponses: []llm.CompactionResponse{{
			Usage: llm.Usage{WindowTokens: 100},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	request := llm.CompactionRequest{
		Model:      "gpt-5",
		SessionID:  store.Meta().SessionID,
		InputItems: eng.transcriptRuntimeState().SnapshotItems(),
	}

	if _, _, _, err := eng.compactWithContextRepairRetry(context.Background(), "step", client, request); err != nil {
		t.Fatalf("compact with repair retry: %v", err)
	}
	if len(client.compactionCalls) != 2 {
		t.Fatalf("compaction calls = %d, want initial 400 plus repaired retry", len(client.compactionCalls))
	}
	if !repairRequestHasToolCall(client.compactionCalls[1].InputItems, "missing") ||
		!repairRequestHasToolOutput(client.compactionCalls[1].InputItems, "missing") {
		t.Fatal("repaired compaction retry did not preserve the call with its synthetic output")
	}
}

func TestCompactionMissingToolOutputRepairRunsSinglePass(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	client := &fakeCompactionClient{
		compactionErrors: []error{
			&llm.APIStatusError{StatusCode: 400},
			&llm.APIStatusError{StatusCode: 400},
		},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	request := llm.CompactionRequest{
		Model:      "gpt-5",
		SessionID:  store.Meta().SessionID,
		InputItems: eng.transcriptRuntimeState().SnapshotItems(),
	}

	if _, _, _, err := eng.compactWithContextRepairRetry(context.Background(), "step", client, request); !llm.HasHTTPStatus(err, 400) {
		t.Fatalf("compaction error = %v, want second HTTP 400 to surface", err)
	}
	if len(client.compactionCalls) != 2 {
		t.Fatalf("compaction calls = %d, want one repair retry", len(client.compactionCalls))
	}
}

func TestCompactionMissingOutputRepairDoesNotConsumeOverflowAttempt(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		errors: []error{
			&llm.APIStatusError{StatusCode: 400},
			&llm.ProviderAPIError{
				ProviderID:   "openai",
				StatusCode:   400,
				Code:         llm.UnifiedErrorCodeContextLengthOverflow,
				ProviderCode: "context_length_exceeded",
			},
			nil,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
			Usage:     llm.Usage{WindowTokens: 200_000},
		}},
	}
	eng := mustNewExecTestEngine(t, store, client, Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}},
	)); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:    "call-shell",
			Name:  "exec_command",
			Input: json.RawMessage(`{}`),
		}}}},
	)); err != nil {
		t.Fatalf("append shell tool call: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value("call-shell"),
			Name:       textutil.Value("exec_command"),
			Content:    textutil.Value(`{"output":"` + strings.Repeat("x", 120_000) + `"}`),
		}},
	)); err != nil {
		t.Fatalf("append shell tool output: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:    "call-missing",
			Name:  "exec_command",
			Input: json.RawMessage(`{}`),
		}}}},
	)); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(client.calls) != 3 {
		t.Fatalf("local compaction calls = %d, want repair, overflow, and collapsed retry", len(client.calls))
	}
	final := client.calls[2].Items
	if !repairRequestHasToolOutput(final, "call-missing") {
		t.Fatal("final local compaction request omitted synthetic output")
	}
	for _, item := range final {
		if isToolOutputItem(item.Type) && item.CallID != nil && *item.CallID == "call-shell" {
			if isCollapsedCompactionOverflowShellOutput(item.Output) {
				return
			}
		}
	}
	t.Fatal("final local compaction request omitted collapsed shell output")
}

func TestCompactionMissingOutputAfterCollapsePanics(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{
			&llm.ProviderAPIError{
				ProviderID:   "openai",
				StatusCode:   400,
				Code:         llm.UnifiedErrorCodeContextLengthOverflow,
				ProviderCode: "context_length_exceeded",
			},
			&llm.APIStatusError{StatusCode: 400},
		},
	}
	eng := mustNewExecTestEngine(t, store, client, Config{Model: "gpt-5"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:    "call-shell",
			Name:  "exec_command",
			Input: json.RawMessage(`{}`),
		}}}},
	)); err != nil {
		t.Fatalf("append shell tool call: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value("call-shell"),
			Name:       textutil.Value("exec_command"),
			Content:    textutil.Value(`{"output":"` + strings.Repeat("x", 120_000) + `"}`),
		}},
	)); err != nil {
		t.Fatalf("append shell tool output: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:    "call-missing",
			Name:  "exec_command",
			Input: json.RawMessage(`{}`),
		}}}},
	)); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	request := llm.CompactionRequest{
		Model:      "gpt-5",
		SessionID:  store.Meta().SessionID,
		InputItems: eng.transcriptRuntimeState().SnapshotItems(),
	}

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected a missing-output provider error after collapse to violate the invariant")
		}
	}()
	_, _, _, _ = eng.compactWithContextRepairRetry(context.Background(), "step", client, request)
}

func repairRequestHasToolCall(items []llm.ResponseItem, callID string) bool {
	for _, item := range items {
		if !isToolCallItem(item.Type) {
			continue
		}
		if got, ok := textutil.FirstOptionalTrimmed(item.CallID, item.ID); ok && got == callID {
			return true
		}
	}
	return false
}

func repairRequestHasToolOutput(items []llm.ResponseItem, callID string) bool {
	for _, item := range items {
		if !isToolOutputItem(item.Type) {
			continue
		}
		if got, ok := textutil.OptionalTrimmed(item.CallID); ok && got == callID {
			return true
		}
	}
	return false
}
