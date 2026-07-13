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
	"core/shared/toolspec"
)

func TestCompactionOverflowRepairCollapsesShellOutputAndPreservesInput(t *testing.T) {
	const staleRawMarker = "raw-stale-shell-output"
	originalRaw := json.RawMessage(`{"type":"function_call_output","call_id":"call-shell","output":"` + staleRawMarker + strings.Repeat("raw", 40_000) + `"}`)
	items := []llm.ResponseItem{
		{Type: llm.ResponseItemTypeFunctionCall, ID: "call-shell", CallID: "call-shell", Name: string(toolspec.ToolExecCommand), Arguments: json.RawMessage(`{"cmd":"go test ./..."}`)},
		{Type: llm.ResponseItemTypeFunctionCallOutput, CallID: "call-shell", Name: string(toolspec.ToolExecCommand), Output: json.RawMessage(`{"output":"` + strings.Repeat("x", 120_000) + `"}`), Raw: originalRaw},
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
	if collapsed != "<collapsed>" {
		t.Fatalf("collapsed shell output = %q, want <collapsed>", collapsed)
	}
	if len(repaired[1].Raw) != 0 {
		t.Fatalf("expected stale provider raw payload to be cleared, got %s", repaired[1].Raw)
	}
	prepared := llm.PrepareOpenAIInputItems(repaired)
	if bytes.Contains(mustMarshalItemsForRepairTestBytes(t, prepared), []byte(staleRawMarker)) {
		t.Fatalf("prepared repaired request still contains stale raw marker")
	}
}

func TestCompactionOverflowRepairCollapsesWriteStdinOutput(t *testing.T) {
	items := []llm.ResponseItem{
		{Type: llm.ResponseItemTypeFunctionCall, ID: "call-stdin", CallID: "call-stdin", Name: string(toolspec.ToolWriteStdin), Arguments: json.RawMessage(`{"session_id":1,"chars":""}`)},
		{Type: llm.ResponseItemTypeFunctionCallOutput, CallID: "call-stdin", Name: string(toolspec.ToolWriteStdin), Output: json.RawMessage(`{"output":"` + strings.Repeat("x", 120_000) + `"}`)},
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
	const staleRawMarker = "raw-stale-patch-input"
	patchInput := "*** Begin Patch\n*** Add File: big.txt\n+" + strings.Repeat("x", 120_000) + "\n*** End Patch\n"
	originalRaw := json.RawMessage(`{"type":"custom_tool_call","call_id":"call-patch","name":"patch","input":` + strconv.Quote(staleRawMarker+patchInput) + `}`)
	items := []llm.ResponseItem{
		{Type: llm.ResponseItemTypeCustomToolCall, ID: "call-patch", CallID: "call-patch", Name: string(toolspec.ToolPatch), CustomInput: patchInput, Raw: originalRaw},
		{Type: llm.ResponseItemTypeCustomToolOutput, CallID: "call-patch", Name: string(toolspec.ToolPatch), Output: json.RawMessage(`{"ok":true}`)},
	}

	repaired, stats := collapseCompactionOverflowToolPayloadsForDefaultWindowRepairAttempt(items, 1)
	if stats.PatchInputsCollapsed != 1 {
		t.Fatalf("patch inputs collapsed = %d, want 1", stats.PatchInputsCollapsed)
	}
	if len(repaired) != len(items) {
		t.Fatalf("repair removed items: got %d want %d", len(repaired), len(items))
	}
	if strings.Contains(repaired[0].CustomInput, strings.Repeat("x", 100)) {
		t.Fatalf("patch input was not collapsed")
	}
	if repaired[0].CustomInput != "<collapsed>" {
		t.Fatalf("collapsed patch input = %q, want <collapsed>", repaired[0].CustomInput)
	}
	if string(repaired[1].Output) != string(items[1].Output) {
		t.Fatalf("patch output changed: %s", repaired[1].Output)
	}
	if len(repaired[0].Raw) != 0 {
		t.Fatalf("expected stale provider raw payload to be cleared, got %s", repaired[0].Raw)
	}
	prepared := llm.PrepareOpenAIInputItems(repaired)
	if bytes.Contains(mustMarshalItemsForRepairTestBytes(t, prepared), []byte(staleRawMarker)) {
		t.Fatalf("prepared repaired request still contains stale raw marker")
	}
}

func TestCompactionOverflowRepairLeavesUnsupportedToolsUnchanged(t *testing.T) {
	items := []llm.ResponseItem{
		{Type: llm.ResponseItemTypeFunctionCall, ID: "call-ask", CallID: "call-ask", Name: string(toolspec.ToolAskQuestion), Arguments: json.RawMessage(`{"question":"` + strings.Repeat("x", 80_000) + `"}`)},
		{Type: llm.ResponseItemTypeFunctionCallOutput, CallID: "call-ask", Name: string(toolspec.ToolAskQuestion), Output: json.RawMessage(`{"answer":"` + strings.Repeat("y", 80_000) + `"}`)},
		{Type: llm.ResponseItemTypeCustomToolCall, ID: "call-custom", CallID: "call-custom", Name: "custom", CustomInput: strings.Repeat("z", 80_000)},
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

	second, secondStats := collapseCompactionOverflowToolPayloadsAfterSavings(first, compactionOverflowRepairTargetTokens(defaultContextWindowTokens, 2), firstStats.EstimatedSavedTokens)
	if secondStats.ShellOutputsCollapsed != 1 {
		t.Fatalf("second attempt newly collapsed %d shell outputs, want 1", secondStats.ShellOutputsCollapsed)
	}
	if bytes.Contains(second[2].Output, []byte("ccc")) {
		t.Fatalf("expected third output collapsed by second attempt")
	}
}

func TestCompactionOverflowRepairTargetsUseContextWindow(t *testing.T) {
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
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)
	client := &fakeCompactionClient{
		errors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "local summary"},
			Usage:     llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID:    "call-shell",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"go test ./..."}`),
	}}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:       llm.RoleTool,
		ToolCallID: "call-shell",
		Name:       string(toolspec.ToolExecCommand),
		Content:    `{"output":"` + strings.Repeat("x", 120_000) + `"}`,
	}})); err != nil {
		t.Fatalf("append tool output: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("local compaction model calls = %d, want 2", len(client.calls))
	}
	if len(client.calls[1].Items) != len(client.calls[0].Items) {
		t.Fatalf("expected repair to preserve item count, first=%d second=%d", len(client.calls[0].Items), len(client.calls[1].Items))
	}
	foundCollapsed := false
	for _, item := range client.calls[1].Items {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == "call-shell" {
			foundCollapsed = isCollapsedCompactionOverflowShellOutput(item.Output)
		}
	}
	if !foundCollapsed {
		t.Fatalf("expected local compaction retry to collapse shell output, got %+v", client.calls[1].Items)
	}
	foundDiagnostic := false
	for _, entry := range eng.ChatSnapshot().Entries {
		if entry.Role == "developer_error_feedback" && strings.Contains(entry.Text, "Context compaction succeeded after collapsing tool payloads") && strings.Contains(entry.Text, "1 shell outputs") {
			foundDiagnostic = true
			break
		}
	}
	if !foundDiagnostic {
		t.Fatalf("expected compaction repair diagnostic in transcript, got %+v", eng.ChatSnapshot().Entries)
	}
}

func TestLocalCompactionFailsFastWhenOverflowHasNoCollapsibleToolPayload(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)
	client := &fakeCompactionClient{
		errors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "unexpected retry"},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("chat-heavy-history", 12_000)}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
		ID:               "rs-heavy",
		EncryptedContent: strings.Repeat("reasoning-heavy-history", 12_000),
	}}}})); err != nil {
		t.Fatalf("append reasoning message: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err == nil {
		t.Fatal("expected ordinary-history overflow to fail without retry")
	} else if !llm.IsContextLengthOverflowError(err) {
		t.Fatalf("expected context overflow error, got %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected no retry without collapsible tool payloads, got %d local compaction model calls", len(client.calls))
	}
}

func TestLocalCompactionUsesTenTwentyFortyPercentRepairScheduleFromConfiguredContextWindow(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)
	client := &fakeCompactionClient{
		errors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 0, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 0, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 0, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "local summary"},
			Usage:     llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local", ContextWindowTokens: 100_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
		ID:               "rs-keep",
		EncryptedContent: strings.Repeat("reasoning", 2_000),
	}}}})); err != nil {
		t.Fatalf("append reasoning item: %v", err)
	}
	for idx := 0; idx < 5; idx++ {
		callID := fmt.Sprintf("call-shell-%d", idx)
		if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:    callID,
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"echo hi"}`),
		}}}})); err != nil {
			t.Fatalf("append assistant tool call %d: %v", idx, err)
		}
		if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
			Role:       llm.RoleTool,
			ToolCallID: callID,
			Name:       string(toolspec.ToolExecCommand),
			Content:    `{"output":"` + strings.Repeat("x", 48_000) + `"}`,
		}})); err != nil {
			t.Fatalf("append tool output %d: %v", idx, err)
		}
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
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
			if item.Type == llm.ResponseItemTypeReasoning && item.ID == "rs-keep" {
				reasoningPreserved = item.EncryptedContent == strings.Repeat("reasoning", 2_000)
			}
		}
		if collapsed != wantCollapsedByCall[callIdx] {
			t.Fatalf("collapsed shell outputs on call %d = %d, want %d", callIdx+1, collapsed, wantCollapsedByCall[callIdx])
		}
		if !reasoningPreserved {
			t.Fatalf("reasoning item changed or missing on compaction repair call %d: %+v", callIdx+1, call.Items)
		}
	}
	collapsed := 0
	for _, item := range client.calls[3].Items {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput && isCollapsedCompactionOverflowShellOutput(item.Output) {
			collapsed++
		}
	}
	if collapsed != 4 {
		t.Fatalf("collapsed shell outputs on fourth attempt = %d, want 4", collapsed)
	}
}

func TestGenerateWithRetryDoesNotRetryContextOverflow(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)
	client := &fakeClient{
		errors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 0, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "unexpected"}}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	req := llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic, Model: "gpt-5", Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: "hello"}})}

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
		CallID: callID,
		Name:   string(toolspec.ToolExecCommand),
		Output: json.RawMessage(`{"output":"` + output + `"}`),
	}
}

func collapseCompactionOverflowToolPayloadsForDefaultWindowRepairAttempt(items []llm.ResponseItem, repairAttempt int) ([]llm.ResponseItem, compactionOverflowRepairStats) {
	return collapseCompactionOverflowToolPayloadsAfterSavings(items, compactionOverflowRepairTargetTokens(defaultContextWindowTokens, repairAttempt), 0)
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
