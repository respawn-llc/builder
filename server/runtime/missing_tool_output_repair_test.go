package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func appendRepairEvent(t *testing.T, store *session.Store, kind string, payload any) session.Event {
	t.Helper()
	event, _, err := store.AppendEvent("step", kind, payload)
	if err != nil {
		t.Fatalf("append %q event: %v", kind, err)
	}
	return event
}

func readRepairEvents(t *testing.T, store *session.Store) []session.Event {
	t.Helper()
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	return events
}

func repairItemsContainCall(items []llm.ResponseItem, callID string) bool {
	for _, item := range items {
		if strings.TrimSpace(item.CallID) == callID && isToolCallItem(item.Type) {
			return true
		}
	}
	return false
}

func repairItemsContainOutput(items []llm.ResponseItem, callID string) bool {
	for _, item := range items {
		if strings.TrimSpace(item.CallID) == callID && isToolOutputItem(item.Type) {
			return true
		}
	}
	return false
}

func countPersistedLocalEntries(events []session.Event) int {
	count := 0
	for _, event := range events {
		if event.Kind == "local_entry" {
			count++
		}
	}
	return count
}

// On a provider HTTP 400 caused by an interrupted tool call without output, the
// repair appends a synthetic completion (it does not remove the call) and the
// retry succeeds with the call still present and now answered.
func TestMissingToolOutputRepairAppendsSyntheticOutputAndRetries(t *testing.T) {
	store := mustCreateTestSession(t)
	appendRepairEvent(t, store, "message", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec", Input: json.RawMessage(`{}`)}},
	})
	client := &fakeClient{
		errors: []error{&llm.APIStatusError{StatusCode: 400, Body: "tool call without output"}},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "repaired"},
			Usage:     llm.Usage{InputTokens: 10, OutputTokens: 2, WindowTokens: 100},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "continue")
	if err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if msg.Content != "repaired" {
		t.Fatalf("assistant content = %q, want repaired", msg.Content)
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d, want 2 (initial 400 + repaired retry)", len(client.calls))
	}
	if !repairItemsContainCall(client.calls[0].Items, "missing") {
		t.Fatalf("first request should include the dangling call, got %+v", client.calls[0].Items)
	}
	if !repairItemsContainCall(client.calls[1].Items, "missing") {
		t.Fatalf("retry request should still include the call (append-only), got %+v", client.calls[1].Items)
	}
	if !repairItemsContainOutput(client.calls[1].Items, "missing") {
		t.Fatalf("retry request should include the synthetic output, got %+v", client.calls[1].Items)
	}
	if !repairItemsContainCall(eng.transcriptRuntimeState().SnapshotItems(), "missing") {
		t.Fatalf("projection must keep the call (not remove it), got %+v", eng.transcriptRuntimeState().SnapshotItems())
	}
	if !repairItemsContainOutput(eng.transcriptRuntimeState().SnapshotItems(), "missing") {
		t.Fatalf("projection must include the synthetic output, got %+v", eng.transcriptRuntimeState().SnapshotItems())
	}
	if got := countPersistedLocalEntries(readRepairEvents(t, store)); got != 1 {
		t.Fatalf("persisted operator warnings = %d, want 1", got)
	}
}

// A 400 that is not caused by a missing tool output (nothing dangling) must
// surface the original error without retrying.
func TestMissingToolOutputRepairLeavesUnrelated400Unrepaired(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{
		errors: []error{
			&llm.APIStatusError{StatusCode: 400, Body: "malformed request"},
		},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); err == nil {
		t.Fatal("expected the unrelated 400 to surface as an error")
	}
	if len(client.calls) != 1 {
		t.Fatalf("model calls = %d, want 1 (no repair retry)", len(client.calls))
	}
}

func TestRepairMissingToolOutputsByAppendingIsIdempotent(t *testing.T) {
	store := mustCreateTestSession(t)
	appendRepairEvent(t, store, "message", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec", Input: json.RawMessage(`{}`)}},
	})
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	repaired, err := eng.repairMissingToolOutputsByAppending("step")
	if err != nil {
		t.Fatalf("first repair: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("first repair count = %d, want 1", repaired)
	}
	repaired, err = eng.repairMissingToolOutputsByAppending("step")
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("second repair count = %d, want 0 (already closed)", repaired)
	}
}

// The resume path re-executes interrupted calls; the repair must defer while
// pending tool-call starts remain so it never pre-empts a real output.
func TestRepairMissingToolOutputsDefersToPendingToolCallStarts(t *testing.T) {
	store := mustCreateTestSession(t)
	appendRepairEvent(t, store, "message", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec", Input: json.RawMessage(`{}`)}},
	})
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.rememberPendingToolCallStarts(map[string]int{"missing": 1})

	repaired, err := eng.repairMissingToolOutputsByAppending("step")
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("repair count = %d, want 0 while a pending start exists", repaired)
	}
}

func TestRepairMissingToolOutputsMarksResultAsError(t *testing.T) {
	store := mustCreateTestSession(t)
	appendRepairEvent(t, store, "message", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec", Input: json.RawMessage(`{}`)}},
	})
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.repairMissingToolOutputsByAppending("step"); err != nil {
		t.Fatalf("repair: %v", err)
	}
	var completion *storedToolCompletion
	for _, event := range readRepairEvents(t, store) {
		if event.Kind != "tool_completed" {
			continue
		}
		var stored storedToolCompletion
		if err := json.Unmarshal(event.Payload, &stored); err != nil {
			t.Fatalf("decode completion: %v", err)
		}
		if stored.CallID == "missing" {
			completion = &stored
		}
	}
	if completion == nil {
		t.Fatal("expected a persisted synthetic completion for the dangling call")
	}
	if !completion.IsError {
		t.Fatal("synthetic completion should be marked as an error result")
	}
}

// A compaction request that 400s on a dangling tool call is repaired by
// appending synthetic outputs and retried; the retry keeps the call and adds its
// output.
func TestCompactionMissingToolOutputRepairAppendsAndRetries(t *testing.T) {
	store := mustCreateTestSession(t)
	appendRepairEvent(t, store, "message", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing-compact", Name: "exec", Input: json.RawMessage(`{}`)}},
	})
	client := &fakeCompactionClient{
		compactionErrors: []error{&llm.APIStatusError{StatusCode: 400, Body: "tool call without output"}},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "summary"}},
			Usage:       llm.Usage{InputTokens: 10, OutputTokens: 2, WindowTokens: 100},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	baseRequest := llm.CompactionRequest{Model: "gpt-5", SessionID: store.Meta().SessionID, InputItems: eng.transcriptRuntimeState().SnapshotItems()}

	if _, _, _, err := eng.compactWithContextRepairRetry(context.Background(), "compact", client, baseRequest); err != nil {
		t.Fatalf("compact with repair retry: %v", err)
	}
	if len(client.compactionCalls) != 2 {
		t.Fatalf("compaction calls = %d, want 2", len(client.compactionCalls))
	}
	retryInput := client.compactionCalls[1].InputItems
	if !repairItemsContainCall(retryInput, "missing-compact") {
		t.Fatalf("retry compaction request should still include the call, got %+v", retryInput)
	}
	if !repairItemsContainOutput(retryInput, "missing-compact") {
		t.Fatalf("retry compaction request should include the synthetic output, got %+v", retryInput)
	}
}

// A repeated 400 must not loop: the repair runs a single pass, then the error
// surfaces.
func TestCompactionMissingToolOutputRepairRunsSinglePass(t *testing.T) {
	store := mustCreateTestSession(t)
	appendRepairEvent(t, store, "message", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing-compact", Name: "exec", Input: json.RawMessage(`{}`)}},
	})
	client := &fakeCompactionClient{
		compactionErrors: []error{
			&llm.APIStatusError{StatusCode: 400, Body: "bad request"},
			&llm.APIStatusError{StatusCode: 400, Body: "bad request"},
		},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	baseRequest := llm.CompactionRequest{Model: "gpt-5", SessionID: store.Meta().SessionID, InputItems: eng.transcriptRuntimeState().SnapshotItems()}

	_, _, _, err := eng.compactWithContextRepairRetry(context.Background(), "compact", client, baseRequest)
	if !llm.HasHTTPStatus(err, 400) {
		t.Fatalf("compaction error = %v, want HTTP 400 to surface", err)
	}
	if len(client.compactionCalls) != 2 {
		t.Fatalf("compaction calls = %d, want single repair retry", len(client.compactionCalls))
	}
}

// A missing-output 400 repair must not consume a context-overflow collapse
// attempt: after the repair, the repaired request that overflows still gets
// collapsed and retried, so both fixes compose within one compaction.
func TestCompactionMissingOutputRepairDoesNotConsumeOverflowAttempt(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)
	client := &fakeCompactionClient{
		errors: []error{
			&llm.APIStatusError{StatusCode: 400, Body: "tool call without output"},
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
		t.Fatalf("seed: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID: "call-shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"go test ./..."}`),
	}}}})); err != nil {
		t.Fatalf("append shell call: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role: llm.RoleTool, ToolCallID: "call-shell", Name: string(toolspec.ToolExecCommand),
		Content: `{"output":"` + strings.Repeat("x", 120_000) + `"}`,
	}})); err != nil {
		t.Fatalf("append shell output: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID: "call-missing", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`),
	}}}})); err != nil {
		t.Fatalf("append dangling call: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(client.calls) != 3 {
		t.Fatalf("model calls = %d, want 3 (missing-output 400, overflow 400, success)", len(client.calls))
	}
	final := client.calls[2].Items
	if !repairItemsContainOutput(final, "call-missing") {
		t.Fatalf("final request should include the synthetic output for the dangling call, got %+v", final)
	}
	collapsed := false
	for _, item := range final {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == "call-shell" {
			collapsed = isCollapsedCompactionOverflowShellOutput(item.Output)
		}
	}
	if !collapsed {
		t.Fatalf("final request should collapse the shell output (overflow attempt preserved), got %+v", final)
	}
}

// Overflow collapse only shrinks existing tool output payloads; it never removes
// output items. A missing-tool-output 400 observed after a collapse therefore
// means the transcript is corrupt, not merely interrupted, so the repair must
// not silently re-snapshot and reapply — it must panic.
func TestCompactionMissingOutputAfterCollapsePanics(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)
	client := &fakeCompactionClient{
		compactionErrors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			&llm.APIStatusError{StatusCode: 400, Body: "tool call without output"},
		},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID: "call-shell", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"go test ./..."}`),
	}}}})); err != nil {
		t.Fatalf("append shell call: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role: llm.RoleTool, ToolCallID: "call-shell", Name: string(toolspec.ToolExecCommand),
		Content: `{"output":"` + strings.Repeat("x", 120_000) + `"}`,
	}})); err != nil {
		t.Fatalf("append shell output: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID: "call-missing", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`),
	}}}})); err != nil {
		t.Fatalf("append dangling call: %v", err)
	}
	baseRequest := llm.CompactionRequest{Model: "gpt-5", SessionID: store.Meta().SessionID, InputItems: eng.transcriptRuntimeState().SnapshotItems()}

	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic when a missing-output 400 surfaces after overflow collapse")
		}
	}()
	_, _, _, _ = eng.compactWithContextRepairRetry(context.Background(), "compact", client, baseRequest)
}

func TestRepairWarningUsesOperatorFacingRole(t *testing.T) {
	store := mustCreateTestSession(t)
	appendRepairEvent(t, store, "message", llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "missing", Name: "exec", Input: json.RawMessage(`{}`)}},
	})
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.repairMissingToolOutputsByAppending("step"); err != nil {
		t.Fatalf("repair: %v", err)
	}
	found := false
	for _, event := range readRepairEvents(t, store) {
		if event.Kind != "local_entry" {
			continue
		}
		var entry storedLocalEntry
		if err := json.Unmarshal(event.Payload, &entry); err != nil {
			t.Fatalf("decode local entry: %v", err)
		}
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) && strings.TrimSpace(entry.Text) != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an operator-facing repair warning entry")
	}
}
