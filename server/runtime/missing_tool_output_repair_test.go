package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
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
