package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestCompactionOverflowRepairCollapsesShellOutputAndPreservesInput(t *testing.T) {
	t.Parallel()
	const staleRawMarker = "raw-stale-shell-output"
	originalRaw := json.RawMessage(`{"type":"function_call_output","call_id":"call-shell","output":"` + staleRawMarker + strings.Repeat("raw", 40_000) + `"}`)
	items := []llm.ResponseItem{
		{Type: llm.ResponseItemTypeFunctionCall, ID: textutil.Value("call-shell"), CallID: textutil.Value("call-shell"), Name: textutil.Value(string(toolspec.ToolExecCommand)), Arguments: json.RawMessage(`{"cmd":"go test ./..."}`)},
		{Type: llm.ResponseItemTypeFunctionCallOutput, CallID: textutil.Value("call-shell"), Name: textutil.Value(string(toolspec.ToolExecCommand)), Output: json.RawMessage(`{"output":"` + strings.Repeat("x", 120_000) + `"}`), Raw: originalRaw},
	}

	repaired, stats := collapseCompactionOverflowToolPayloadsForDefaultWindowRepairAttempt(items, 1)
	if !stats.Collapsed() {
		t.Fatalf("expected repair stats, got %+v", stats)
	}
	if stats.ShellOutputsCollapsed != 1 {
		t.Fatalf("shell outputs collapsed = %d, want 1", stats.ShellOutputsCollapsed)
	}
	if len(repaired) != len(items) {
		t.Fatalf("repair removed items: got %d want %d", len(repaired), len(items))
	}
	if string(repaired[0].Arguments) != string(items[0].Arguments) {
		t.Fatalf("shell input changed: %s", repaired[0].Arguments)
	}
	if bytes.Contains(repaired[1].Output, []byte(strings.Repeat("x", 100))) {
		t.Fatalf("shell output was not collapsed")
	}
	var collapsed string
	if err := json.Unmarshal(repaired[1].Output, &collapsed); err != nil {
		t.Fatalf("decode collapsed shell output: %v", err)
	}
	if collapsed != compactionOverflowCollapsedText {
		t.Fatalf("collapsed shell output = %q, want %q", collapsed, compactionOverflowCollapsedText)
	}
	if len(repaired[1].Raw) == 0 {
		t.Fatal("expected collapsed shell output to be rematerialized")
	}
	if bytes.Contains(mustMarshalItemsForRepairTestBytes(t, repaired), []byte(staleRawMarker)) {
		t.Fatalf("repaired request still contains stale raw marker")
	}
}

func TestCompactionOverflowRepairCollapsesWriteStdinOutput(t *testing.T) {
	t.Parallel()
	items := []llm.ResponseItem{
		{Type: llm.ResponseItemTypeFunctionCall, ID: textutil.Value("call-stdin"), CallID: textutil.Value("call-stdin"), Name: textutil.Value(string(toolspec.ToolWriteStdin)), Arguments: json.RawMessage(`{"session_id":1,"chars":""}`)},
		{Type: llm.ResponseItemTypeFunctionCallOutput, CallID: textutil.Value("call-stdin"), Name: textutil.Value(string(toolspec.ToolWriteStdin)), Output: json.RawMessage(`{"output":"` + strings.Repeat("x", 120_000) + `"}`)},
	}

	repaired, stats := collapseCompactionOverflowToolPayloadsForDefaultWindowRepairAttempt(items, 1)
	if stats.ShellOutputsCollapsed != 1 {
		t.Fatalf("shell outputs collapsed = %d, want 1", stats.ShellOutputsCollapsed)
	}
	if !isCollapsedCompactionOverflowShellOutput(repaired[1].Output) {
		t.Fatalf("expected write_stdin output to collapse, got %s", repaired[1].Output)
	}
	if string(repaired[0].Arguments) != string(items[0].Arguments) {
		t.Fatalf("write_stdin input changed: %s", repaired[0].Arguments)
	}
}

func TestCompactionOverflowRepairCollapsesPatchInputAndPreservesPair(t *testing.T) {
	t.Parallel()
	const staleRawMarker = "raw-stale-patch-input"
	patchInput := "*** Begin Patch\n*** Add File: big.txt\n+" + strings.Repeat("x", 120_000) + "\n*** End Patch\n"
	originalRaw := json.RawMessage(`{"type":"custom_tool_call","call_id":"call-patch","name":"patch","input":` + strconv.Quote(staleRawMarker+patchInput) + `}`)
	items := []llm.ResponseItem{
		{Type: llm.ResponseItemTypeCustomToolCall, ID: textutil.Value("call-patch"), CallID: textutil.Value("call-patch"), Name: textutil.Value(string(toolspec.ToolPatch)), CustomInput: textutil.Value(patchInput), Raw: originalRaw},
		{Type: llm.ResponseItemTypeCustomToolOutput, CallID: textutil.Value("call-patch"), Name: textutil.Value(string(toolspec.ToolPatch)), Output: json.RawMessage(`{"ok":true}`)},
	}

	repaired, stats := collapseCompactionOverflowToolPayloadsForDefaultWindowRepairAttempt(items, 1)
	if stats.PatchInputsCollapsed != 1 {
		t.Fatalf("patch inputs collapsed = %d, want 1", stats.PatchInputsCollapsed)
	}
	if len(repaired) != len(items) {
		t.Fatalf("repair removed items: got %d want %d", len(repaired), len(items))
	}
	if repaired[0].CustomInput == nil || strings.Contains(*repaired[0].CustomInput, strings.Repeat("x", 100)) {
		t.Fatalf("patch input was not collapsed")
	}
	if repaired[0].CustomInput == nil || *repaired[0].CustomInput != compactionOverflowCollapsedText {
		t.Fatalf("collapsed patch input = %q, want %q", *repaired[0].CustomInput, compactionOverflowCollapsedText)
	}
	if string(repaired[1].Output) != string(items[1].Output) {
		t.Fatalf("patch output changed: %s", repaired[1].Output)
	}
	if len(repaired[0].Raw) == 0 {
		t.Fatal("expected collapsed patch input to be rematerialized")
	}
	if bytes.Contains(mustMarshalItemsForRepairTestBytes(t, repaired), []byte(staleRawMarker)) {
		t.Fatalf("repaired request still contains stale raw marker")
	}
}

func TestCompactionOverflowRepairLeavesUnsupportedToolsUnchanged(t *testing.T) {
	t.Parallel()
	items := []llm.ResponseItem{
		{Type: llm.ResponseItemTypeFunctionCall, ID: textutil.Value("call-ask"), CallID: textutil.Value("call-ask"), Name: textutil.Value(string(toolspec.ToolAskQuestion)), Arguments: json.RawMessage(`{"question":"` + strings.Repeat("x", 80_000) + `"}`)},
		{Type: llm.ResponseItemTypeFunctionCallOutput, CallID: textutil.Value("call-ask"), Name: textutil.Value(string(toolspec.ToolAskQuestion)), Output: json.RawMessage(`{"answer":"` + strings.Repeat("y", 80_000) + `"}`)},
		{Type: llm.ResponseItemTypeCustomToolCall, ID: textutil.Value("call-custom"), CallID: textutil.Value("call-custom"), Name: textutil.Value("custom"), CustomInput: textutil.Value(strings.Repeat("z", 80_000))},
	}

	repaired, stats := collapseCompactionOverflowToolPayloadsForDefaultWindowRepairAttempt(items, 1)
	if stats.Collapsed() {
		t.Fatalf("did not expect unsupported repair stats, got %+v", stats)
	}
	if got, want := mustMarshalItemsForRepairTest(t, repaired), mustMarshalItemsForRepairTest(t, items); got != want {
		t.Fatalf("unsupported tools changed\nwant=%s\n got=%s", want, got)
	}
}

func TestCompactionOverflowRepairUsesCumulativeAttemptCapOldestFirst(t *testing.T) {
	t.Parallel()
	items := []llm.ResponseItem{
		shellOutputRepairItem("call-1", strings.Repeat("a", 48_000)),
		shellOutputRepairItem("call-2", strings.Repeat("b", 48_000)),
		shellOutputRepairItem("call-3", strings.Repeat("c", 48_000)),
	}

	first, firstStats := collapseCompactionOverflowToolPayloadsForDefaultWindowRepairAttempt(items, 1)
	if firstStats.ShellOutputsCollapsed != 2 {
		t.Fatalf("first attempt collapsed %d shell outputs, want 2", firstStats.ShellOutputsCollapsed)
	}
	if bytes.Contains(first[0].Output, []byte("aaa")) || bytes.Contains(first[1].Output, []byte("bbb")) {
		t.Fatalf("expected first two outputs collapsed")
	}
	if !bytes.Contains(first[2].Output, []byte("ccc")) {
		t.Fatalf("expected third output to remain for first attempt")
	}

	second, secondStats := collapseCompactionOverflowToolPayloadsAfterSavings(first, compactionOverflowRepairTargetTokens(200_000, 2), firstStats.EstimatedSavedTokens)
	if secondStats.ShellOutputsCollapsed != 1 {
		t.Fatalf("second attempt newly collapsed %d shell outputs, want 1", secondStats.ShellOutputsCollapsed)
	}
	if bytes.Contains(second[2].Output, []byte("ccc")) {
		t.Fatalf("expected third output collapsed by second attempt")
	}
}

func TestCompactionOverflowRepairTargetsUseContextWindow(t *testing.T) {
	t.Parallel()
	if got, want := compactionOverflowRepairTargetTokens(100_000, 1), 10_000; got != want {
		t.Fatalf("first repair target = %d, want %d", got, want)
	}
	if got, want := compactionOverflowRepairTargetTokens(100_000, 2), 20_000; got != want {
		t.Fatalf("second repair target = %d, want %d", got, want)
	}
	if got, want := compactionOverflowRepairTargetTokens(100_000, 3), 40_000; got != want {
		t.Fatalf("third repair target = %d, want %d", got, want)
	}
	if got := compactionOverflowRepairTargetTokens(100_000, 4); got != 0 {
		t.Fatalf("fourth repair target = %d, want 0", got)
	}
}

func TestLocalCompactionCollapsesToolPayloadAfterOverflow(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		errors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("local summary")},
			Usage:     llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
		}},
	}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID:    "call-shell",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"go test ./..."}`),
	}}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:       llm.RoleTool,
		ToolCallID: textutil.Value("call-shell"),
		Name:       textutil.Value(string(toolspec.ToolExecCommand)),
		Content:    textutil.Value(`{"output":"` + strings.Repeat("x", 120_000) + `"}`),
	}})); err != nil {
		t.Fatalf("append tool output: %v", err)
	}

	scheduleManualCompactionAndWait(t, eng)
	if len(client.calls) != 2 {
		t.Fatalf("local compaction model calls = %d, want 2", len(client.calls))
	}
	if len(client.calls[1].Items) != len(client.calls[0].Items) {
		t.Fatalf("expected repair to preserve item count, first=%d second=%d", len(client.calls[0].Items), len(client.calls[1].Items))
	}
	foundCollapsed := false
	for _, item := range client.calls[1].Items {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID != nil && *item.CallID == "call-shell" {
			foundCollapsed = isCollapsedCompactionOverflowShellOutput(item.Output)
		}
	}
	if !foundCollapsed {
		t.Fatalf("expected local compaction retry to collapse shell output, got %+v", client.calls[1].Items)
	}
	page := mustEngineNewestSegmentPage(t, eng)
	for _, entry := range page.Snapshot.Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			return
		}
	}
	t.Fatalf("expected repair diagnostic in bounded newest segment, got %+v", page.Snapshot.Entries)
}

func TestLocalCompactionFailsFastWhenOverflowHasNoCollapsibleToolPayload(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		errors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("unexpected retry")},
		}},
	}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("chat-heavy-history", 12_000))}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
		ID:               "rs-heavy",
		EncryptedContent: strings.Repeat("reasoning-heavy-history", 12_000),
	}}}})); err != nil {
		t.Fatalf("append reasoning message: %v", err)
	}

	var events []Event
	eng.cfg.OnEvent = func(event Event) {
		events = append(events, event)
	}
	scheduleManualCompactionAndWait(t, eng)
	if !hasEventKind(events, EventCompactionFailed) {
		t.Fatalf("ordinary-history overflow events = %+v, want failed event", events)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected no retry without collapsible tool payloads, got %d local compaction model calls", len(client.calls))
	}
}

func TestLocalCompactionUsesTenTwentyFortyPercentRepairScheduleFromConfiguredContextWindow(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		errors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 0, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 0, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 0, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("local summary")},
			Usage:     llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
		}},
	}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local", ContextWindowTokens: 100_000})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
		ID:               "rs-keep",
		EncryptedContent: strings.Repeat("reasoning", 2_000),
	}}}})); err != nil {
		t.Fatalf("append reasoning item: %v", err)
	}
	for idx := 0; idx < 5; idx++ {
		callID := fmt.Sprintf("call-shell-%d", idx)
		if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:    callID,
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"echo hi"}`),
		}}}})); err != nil {
			t.Fatalf("append assistant tool call %d: %v", idx, err)
		}
		if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value(callID),
			Name:       textutil.Value(string(toolspec.ToolExecCommand)),
			Content:    textutil.Value(`{"output":"` + strings.Repeat("x", 48_000) + `"}`),
		}})); err != nil {
			t.Fatalf("append tool output %d: %v", idx, err)
		}
	}

	scheduleManualCompactionAndWait(t, eng)
	if len(client.calls) != 4 {
		t.Fatalf("local compaction model calls = %d, want 4", len(client.calls))
	}
	wantCollapsedByCall := []int{0, 1, 2, 4}
	for callIdx, call := range client.calls {
		collapsed := 0
		reasoningPreserved := false
		for _, item := range call.Items {
			if item.Type == llm.ResponseItemTypeFunctionCallOutput && isCollapsedCompactionOverflowShellOutput(item.Output) {
				collapsed++
			}
			if item.Type == llm.ResponseItemTypeReasoning && item.ID != nil && *item.ID == "rs-keep" {
				reasoningPreserved = item.EncryptedContent != nil && *item.EncryptedContent == strings.Repeat("reasoning", 2_000)
			}
		}
		if collapsed != wantCollapsedByCall[callIdx] {
			t.Fatalf("collapsed shell outputs on call %d = %d, want %d", callIdx+1, collapsed, wantCollapsedByCall[callIdx])
		}
		if !reasoningPreserved {
			t.Fatalf("reasoning item changed or missing on compaction repair call %d: %+v", callIdx+1, call.Items)
		}
	}
}

func TestGenerateWithRetryDoesNotRetryContextOverflow(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{
		errors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 0, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("unexpected")}}},
	}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	req := llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "gpt-5", Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("hello")}})}

	_, err := eng.generateWithRetryClient(context.Background(), "step-context-overflow", client, req, nil, nil, nil)
	if err == nil {
		t.Fatal("expected context overflow error")
	}
	if !llm.IsContextLengthOverflowError(err) {
		t.Fatalf("expected context overflow classification, got %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(client.calls))
	}
}

func shellOutputRepairItem(callID string, output string) llm.ResponseItem {
	return llm.ResponseItem{
		Type:   llm.ResponseItemTypeFunctionCallOutput,
		CallID: textutil.Value(callID),
		Name:   textutil.Value(string(toolspec.ToolExecCommand)),
		Output: json.RawMessage(`{"output":"` + output + `"}`),
	}
}

func collapseCompactionOverflowToolPayloadsForDefaultWindowRepairAttempt(items []llm.ResponseItem, repairAttempt int) ([]llm.ResponseItem, compactionOverflowRepairStats) {
	return collapseCompactionOverflowToolPayloadsAfterSavings(items, compactionOverflowRepairTargetTokens(200_000, repairAttempt), 0)
}

func mustMarshalItemsForRepairTest(t *testing.T, items []llm.ResponseItem) string {
	return string(mustMarshalItemsForRepairTestBytes(t, items))
}

func mustMarshalItemsForRepairTestBytes(t *testing.T, items []llm.ResponseItem) []byte {
	t.Helper()
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	return data
}
