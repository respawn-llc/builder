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
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestMissingToolOutputRepairAppendsSyntheticOutputAndRetries(t *testing.T) {
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

func TestMissingToolOutputRepairLeavesUnrelated400Unrepaired(t *testing.T) {
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
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	first, err := eng.repairMissingToolOutputsByAppending("step")
	if err != nil {
		t.Fatalf("first repair: %v", err)
	}
	second, err := eng.repairMissingToolOutputsByAppending("step")
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("repair counts = first:%d second:%d, want one append then no-op", first, second)
	}
}

func TestRepairMissingToolOutputsDefersToPendingToolCallStarts(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.rememberPendingToolCallStarts(map[string]int{"missing": 1})

	repaired, err := eng.repairMissingToolOutputsByAppending("step")
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
