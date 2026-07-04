package runtime

import (
	"fmt"
	"reflect"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func toolCallWithPresentation(t *testing.T, s *chatStore, call llm.ToolCall) llm.ToolCall {
	t.Helper()
	normalized := normalizeToolCallsForTranscript([]llm.ToolCall{call}, s.cwd)
	if len(normalized) != 1 {
		t.Fatalf("expected exactly one normalized tool call, got %d", len(normalized))
	}
	if len(normalized[0].Presentation) == 0 {
		t.Fatalf("expected normalized tool presentation for %+v", call)
	}
	return normalized[0]
}

func TestChatStoreSnapshotProjectsConversation(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "hello"})
	s.appendMessage(llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   llm.MessagePhaseCommentary,
		Content: "Let me check.",
		ToolCalls: []llm.ToolCall{
			{ID: "call_1", Name: "exec_command", Input: json.RawMessage(`{"command":"pwd","workdir":"/tmp","timeout_seconds":300}`)},
		},
	})
	s.recordToolCompletionWithProviderItems(tools.Result{
		CallID:  "call_1",
		Name:    toolspec.ToolExecCommand,
		IsError: false,
		Output:  json.RawMessage(`{"output":"/tmp","exit_code":0,"truncated":false}`),
	}, nil)
	s.appendMessage(llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: "call_1",
		Name:       string(toolspec.ToolExecCommand),
		Content:    `{"output":"/tmp","exit_code":0,"truncated":false}`,
	})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "done"})

	s.appendStreamingDelta("step-1", 12, 6, "stream", llm.MessagePhaseFinal)
	s.setStreamingError("failed")
	s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "system", Text: "note"})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 6 {
		t.Fatalf("expected 6 entries, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	if snap.Entries[0].Role != "user" || snap.Entries[0].Text != "hello" {
		t.Fatalf("unexpected first entry: %+v", snap.Entries[0])
	}
	if snap.Entries[1].Role != "assistant" || snap.Entries[1].Text != "Let me check." {
		t.Fatalf("unexpected commentary entry: %+v", snap.Entries[1])
	}
	if snap.Entries[2].Role != "tool_call" || !strings.Contains(snap.Entries[2].Text, "pwd") {
		t.Fatalf("unexpected tool_call entry: %+v", snap.Entries[2])
	}
	if snap.Entries[2].ToolCallID != "call_1" {
		t.Fatalf("unexpected tool_call id: %+v", snap.Entries[2])
	}
	if snap.Entries[2].ToolCall == nil || !snap.Entries[2].ToolCall.IsShell {
		t.Fatalf("expected shell tool metadata, got %+v", snap.Entries[2].ToolCall)
	}
	if snap.Entries[2].ToolCall.TimeoutLabel != "" {
		t.Fatalf("unexpected timeout label: %+v", snap.Entries[2].ToolCall)
	}
	if strings.Contains(snap.Entries[2].Text, "workdir:") {
		t.Fatalf("tool call should not include workdir line: %+v", snap.Entries[2])
	}
	if snap.Entries[3].Role != "tool_result_ok" || strings.TrimSpace(snap.Entries[3].Text) != "/tmp" {
		t.Fatalf("unexpected tool_result entry: %+v", snap.Entries[3])
	}
	if snap.Entries[3].ToolCallID != "call_1" {
		t.Fatalf("unexpected tool_result call id: %+v", snap.Entries[3])
	}
	if snap.Entries[4].Role != "assistant" || snap.Entries[4].Text != "done" {
		t.Fatalf("unexpected assistant entry: %+v", snap.Entries[4])
	}
	if snap.Entries[5].Role != "system" || snap.Entries[5].Text != "note" {
		t.Fatalf("unexpected local entry: %+v", snap.Entries[5])
	}
	if snap.Streaming != "stream" {
		t.Fatalf("unexpected ongoing text: %q", snap.Streaming)
	}
	if snap.StreamingError != "failed" {
		t.Fatalf("unexpected ongoing error: %q", snap.StreamingError)
	}
}

func TestChatStoreSnapshotKeepsShortCommentaryInTranscript(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "hello"})
	s.appendMessage(llm.Message{
		Role:    llm.RoleAssistant,
		Phase:   llm.MessagePhaseCommentary,
		Content: "Checking out repository",
		ToolCalls: []llm.ToolCall{
			{ID: "call_1", Name: "exec_command", Input: json.RawMessage(`{"command":"pwd"}`)},
		},
	})

	snap := s.snapshotWithMetadata().Snapshot
	for _, entry := range snap.Entries {
		if entry.Role == "assistant" && entry.Text == "Checking out repository" {
			return
		}
	}
	t.Fatalf("expected short commentary preserved in transcript entries, got %+v", snap.Entries)
}

func TestChatStoreSnapshotSynthesizesCompletedToolResultBeforeToolMessage(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{
		Role:    llm.RoleAssistant,
		Content: "working",
		ToolCalls: []llm.ToolCall{
			{ID: "call_a", Name: "exec_command", Input: json.RawMessage(`{"command":"sleep 1"}`)},
			{ID: "call_b", Name: "exec_command", Input: json.RawMessage(`{"command":"pwd"}`)},
		},
	})
	s.recordToolCompletionWithProviderItems(tools.Result{
		CallID: "call_b",
		Name:   toolspec.ToolExecCommand,
		Output: json.RawMessage(`{"output":"/tmp","exit_code":0,"truncated":false}`),
	}, nil)

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 4 {
		t.Fatalf("expected assistant, two tool calls, and synthesized tool result, got %+v", snap.Entries)
	}
	if snap.Entries[1].Role != "tool_call" || snap.Entries[1].ToolCallID != "call_a" {
		t.Fatalf("unexpected first tool call entry: %+v", snap.Entries[1])
	}
	if snap.Entries[2].Role != "tool_call" || snap.Entries[2].ToolCallID != "call_b" {
		t.Fatalf("unexpected second tool call entry: %+v", snap.Entries[2])
	}
	if snap.Entries[3].Role != "tool_result_ok" || snap.Entries[3].ToolCallID != "call_b" || strings.TrimSpace(snap.Entries[3].Text) != "/tmp" {
		t.Fatalf("expected synthesized completed tool result for call_b, got %+v", snap.Entries[3])
	}
}

func TestChatStoreRestoreToolCompletionPreservesCondensedText(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:    "call_ask",
			Name:  string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"Choose scope?","suggestions":["full","fast"]}`),
		}},
	})
	payload, err := json.Marshal(storedToolCompletion{
		CallID:        "call_ask",
		Name:          string(toolspec.ToolAskQuestion),
		Output:        json.RawMessage(`"User chose option #2. They also said: include tests"`),
		CondensedText: "fast\nUser also said:\ninclude tests",
	})
	if err != nil {
		t.Fatalf("marshal completion: %v", err)
	}
	if err := s.restoreToolCompletionPayload(payload); err != nil {
		t.Fatalf("restore completion: %v", err)
	}

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 2 {
		t.Fatalf("expected ask call and synthesized result, got %+v", snap.Entries)
	}
	result := snap.Entries[1]
	if result.Role != "tool_result_ok" || result.ToolCallID != "call_ask" {
		t.Fatalf("unexpected restored result entry: %+v", result)
	}
	if !strings.Contains(result.Text, "User chose option #2. They also said: include tests") || result.CondensedText != "fast\nUser also said:\ninclude tests" {
		t.Fatalf("unexpected restored result text: %+v", result)
	}
}

func TestChatStoreSnapshotKeepsSubstantiveCommentaryInTranscript(t *testing.T) {
	s := newChatStore()
	content := strings.Repeat("reasoning detail ", 20)
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseCommentary, Content: content})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 1 || snap.Entries[0].Role != "assistant" || snap.Entries[0].Phase != llm.MessagePhaseCommentary || snap.Entries[0].Text != content {
		t.Fatalf("expected substantive commentary preserved in transcript, got %+v", snap.Entries)
	}
}

func TestChatStoreSnapshotPreservesLocalEntryCondensedText(t *testing.T) {
	s := newChatStore()
	s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "reviewer_suggestions", Text: "Supervisor suggested:\n1. First", CondensedText: "Supervisor made 1 suggestion."})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 1 {
		t.Fatalf("expected one entry, got %+v", snap.Entries)
	}
	if snap.Entries[0].Role != "reviewer_suggestions" || snap.Entries[0].Text != "Supervisor suggested:\n1. First" || snap.Entries[0].CondensedText != "Supervisor made 1 suggestion." {
		t.Fatalf("unexpected local entry snapshot: %+v", snap.Entries[0])
	}
}

func TestChatStoreTranscriptPageSnapshotCollectsRequestedWindow(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "u1"})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "a1"})
	s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "system", Text: "note"})
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "u2"})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "a2"})

	page := s.transcriptPageSnapshot(1, 3)
	if page.TotalEntries != 5 {
		t.Fatalf("total entries = %d, want 5", page.TotalEntries)
	}
	if page.Offset != 1 {
		t.Fatalf("offset = %d, want 1", page.Offset)
	}
	if len(page.Snapshot.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(page.Snapshot.Entries))
	}
	if page.Snapshot.Entries[0].Role != "assistant" || page.Snapshot.Entries[0].Text != "a1" {
		t.Fatalf("unexpected first page entry: %+v", page.Snapshot.Entries[0])
	}
	if page.Snapshot.Entries[1].Role != "system" || page.Snapshot.Entries[1].Text != "note" {
		t.Fatalf("unexpected local page entry: %+v", page.Snapshot.Entries[1])
	}
	if page.Snapshot.Entries[2].Role != "user" || page.Snapshot.Entries[2].Text != "u2" {
		t.Fatalf("unexpected trailing page entry: %+v", page.Snapshot.Entries[2])
	}
}

func TestChatStoreTranscriptPageSnapshotSynthesizesCompletedToolResultBeforeToolMessage(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{
		Role:    llm.RoleAssistant,
		Content: "working",
		ToolCalls: []llm.ToolCall{
			{ID: "call_b", Name: "exec_command", Input: json.RawMessage(`{"command":"pwd"}`)},
		},
	})
	s.recordToolCompletionWithProviderItems(tools.Result{
		CallID: "call_b",
		Name:   toolspec.ToolExecCommand,
		Output: json.RawMessage(`{"output":"/tmp","exit_code":0,"truncated":false}`),
	}, nil)

	page := s.transcriptPageSnapshot(0, 0)
	if page.TotalEntries != 3 {
		t.Fatalf("total entries = %d, want 3", page.TotalEntries)
	}
	if len(page.Snapshot.Entries) != 3 {
		t.Fatalf("entries = %d, want 3 (%+v)", len(page.Snapshot.Entries), page.Snapshot.Entries)
	}
	if page.Snapshot.Entries[2].Role != "tool_result_ok" || page.Snapshot.Entries[2].ToolCallID != "call_b" || strings.TrimSpace(page.Snapshot.Entries[2].Text) != "/tmp" {
		t.Fatalf("expected synthesized completed tool result for call_b, got %+v", page.Snapshot.Entries[2])
	}
}

func historyReplacedEvent(t *testing.T, messages []llm.Message) session.Event {
	t.Helper()
	return mustPersistedEvent(t, "history_replaced", historyReplacementPayload{
		Engine: "compaction",
		Items:  llm.ItemsFromMessages(messages),
	})
}

func TestPersistedTranscriptScanPreservesHistoryAcrossCompaction(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedScanEvents(t, scan, []session.Event{
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "before compaction"}),
		mustPersistedEvent(t, "local_entry", storedLocalEntry{Role: "error", Text: "before replace"}),
		historyReplacedEvent(t, []llm.Message{
			{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeEnvironment, Content: "environment info"},
			{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "condensed summary"},
		}),
		mustPersistedEvent(t, "local_entry", storedLocalEntry{Role: "compaction_notice", Text: "after replace notice"}),
	})

	entries := scan.CollectedPageSnapshot().Entries
	if got := len(entries); got != 5 {
		t.Fatalf("entry count = %d, want 5 (%+v)", got, entries)
	}
	if got := entries[0]; got.Role != "user" || got.Text != "before compaction" {
		t.Fatalf("entry[0] = %+v, want preserved pre-compaction user entry", got)
	}
	if got := entries[1]; got.Role != "error" || got.Text != "before replace" {
		t.Fatalf("entry[1] = %+v, want preserved pre-compaction local entry", got)
	}
	if got := entries[2]; got.Role != string(transcript.EntryRoleDeveloperContext) || got.Text != "environment info" {
		t.Fatalf("entry[2] = %+v, want compacted developer context", got)
	}
	if got := entries[3]; got.Role != string(transcript.EntryRoleCompactionSummary) || got.Text != "condensed summary" || got.CompactLabel != "Context compacted" || got.CondensedText != "Context compacted" {
		t.Fatalf("entry[3] = %+v, want compacted summary", got)
	}
	if got := entries[4]; got.Role != "compaction_notice" || got.Text != "after replace notice" {
		t.Fatalf("entry[4] = %+v, want legacy local entry preserved without special handling", got)
	}
}

func TestPersistedTranscriptScanRecentTailUsesLatestCompactionBoundaryAsFloor(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{TrackRecentTail: true, TailLimit: 1})
	applyPersistedScanEvents(t, scan, []session.Event{
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "before compaction"}),
		historyReplacedEvent(t, []llm.Message{
			{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeEnvironment, Content: "environment info"},
			{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "condensed summary"},
		}),
		mustPersistedEvent(t, "local_entry", storedLocalEntry{Role: "compaction_notice", Text: "after replace notice"}),
	})

	window := scan.RecentTailSnapshot()
	if got := len(window.Snapshot.Entries); got != 3 {
		t.Fatalf("entry count = %d, want 3 (%+v)", got, window.Snapshot.Entries)
	}
	if got := window.TotalEntries; got != 4 {
		t.Fatalf("total entries = %d, want 4", got)
	}
	if got := window.Offset; got != 1 {
		t.Fatalf("offset = %d, want 1", got)
	}
	if got := window.Snapshot.Entries[0]; got.Role != string(transcript.EntryRoleDeveloperContext) || got.Text != "environment info" {
		t.Fatalf("entry[0] = %+v, want compacted developer context", got)
	}
	if got := window.Snapshot.Entries[1]; got.Role != string(transcript.EntryRoleCompactionSummary) || got.Text != "condensed summary" || got.CompactLabel != "Context compacted" || got.CondensedText != "Context compacted" {
		t.Fatalf("entry[1] = %+v, want compacted summary", got)
	}
	if got := window.Snapshot.Entries[2]; got.Role != "compaction_notice" || got.Text != "after replace notice" {
		t.Fatalf("entry[2] = %+v, want legacy local entry preserved without special handling", got)
	}
}

func TestPersistedTranscriptScanRecentTailUsesMostRecentCompactionBoundary(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{TrackRecentTail: true, TailLimit: 1})
	applyPersistedScanEvents(t, scan, []session.Event{
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "before"}),
		historyReplacedEvent(t, []llm.Message{{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary-1"}}),
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "between"}),
		historyReplacedEvent(t, []llm.Message{{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary-2"}}),
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: "after"}),
	})

	window := scan.RecentTailSnapshot()
	if got := window.TotalEntries; got != 5 {
		t.Fatalf("total entries = %d, want 5", got)
	}
	if got := window.Offset; got != 3 {
		t.Fatalf("offset = %d, want 3", got)
	}
	if got := len(window.Snapshot.Entries); got != 2 {
		t.Fatalf("entry count = %d, want 2 (%+v)", got, window.Snapshot.Entries)
	}
	if got := window.Snapshot.Entries[0].Text; got != "summary-2" {
		t.Fatalf("entry[0] = %q, want summary-2", got)
	}
	if got := window.Snapshot.Entries[1].Text; got != "after" {
		t.Fatalf("entry[1] = %q, want after", got)
	}
}

func TestPatchToolCallFormattingCapturesSummaryAndDetailMeta(t *testing.T) {
	s := newChatStore()
	s.cwd = "/workspace"

	patchText := "*** Begin Patch\n*** Update File: dir/a.go\n line1\n-old\n+new\n*** Add File: b.go\n+hello\n*** End Patch\n"
	call := llm.ToolCall{
		ID:          "call_patch",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: patchText,
	}
	call = toolCallWithPresentation(t, s, call)
	rendered := s.formatToolCall(call)
	if rendered.ToolCall == nil {
		t.Fatalf("expected tool metadata on patch call")
	}
	if rendered.ToolCall.RenderHint == nil || rendered.ToolCall.RenderHint.Kind != transcript.ToolRenderKindDiff {
		t.Fatalf("expected diff render hint for patch, got %+v", rendered.ToolCall.RenderHint)
	}
	if rendered.ToolCallID != "call_patch" {
		t.Fatalf("unexpected patch call id: %+v", rendered)
	}
	summary := rendered.ToolCall.PatchSummary
	detail := rendered.ToolCall.PatchDetail
	if !rendered.ToolCall.HasPatchSummary() || !rendered.ToolCall.HasPatchDetail() {
		t.Fatalf("expected patch summary/detail metadata, got %+v", rendered.ToolCall)
	}
	if rendered.ToolCall.PatchRender == nil {
		t.Fatalf("expected typed patch render metadata, got %+v", rendered.ToolCall)
	}
	if strings.Contains(summary, "Edited:") || !strings.Contains(summary, "./dir/a.go +1 -1") || !strings.Contains(summary, "./b.go +1") {
		t.Fatalf("unexpected summary output: %q", summary)
	}
	if !strings.Contains(detail, "/workspace/dir/a.go") || !strings.Contains(detail, "/workspace/b.go") {
		t.Fatalf("unexpected detail paths: %q", detail)
	}
	if !strings.Contains(detail, "+new") || !strings.Contains(detail, "-old") || !strings.Contains(detail, "+hello") {
		t.Fatalf("unexpected detail diff: %q", detail)
	}
}

func TestCustomPatchToolCallFormattingUsesFreeformInput(t *testing.T) {
	s := newChatStore()
	s.cwd = "/workspace"

	patchText := "*** Begin Patch\n*** Update File: cli/app/ui_status.go\n@@\n type uiStatusAuthInfo struct {\n-\tSummary string\n+\tSummary string\n+\tReady bool\n }\n*** End Patch\n"
	call := llm.ToolCall{
		ID:          "call_patch_custom",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: patchText,
	}
	call = toolCallWithPresentation(t, s, call)
	rendered := s.formatToolCall(call)
	if rendered.ToolCall == nil {
		t.Fatalf("expected tool metadata on custom patch call")
	}
	if rendered.Text != rendered.ToolCall.PatchDetail {
		t.Fatalf("expected custom patch call text to use rendered detail, text=%q detail=%q", rendered.Text, rendered.ToolCall.PatchDetail)
	}
	if strings.Contains(rendered.ToolCall.PatchSummary, "*** Begin Patch") {
		t.Fatalf("expected ongoing summary to hide raw patch payload, got %q", rendered.ToolCall.PatchSummary)
	}
	if rendered.ToolCall.PatchSummary != "./cli/app/ui_status.go +2 -1" {
		t.Fatalf("unexpected custom patch summary: %q", rendered.ToolCall.PatchSummary)
	}
	if rendered.ToolCall.PatchRender == nil {
		t.Fatalf("expected typed patch render metadata, got %+v", rendered.ToolCall)
	}
}

func TestPatchToolCallFormattingSingleFileUsesInlineEditedHeader(t *testing.T) {
	s := newChatStore()
	s.cwd = "/workspace"

	patchText := "*** Begin Patch\n*** Update File: dir/a.go\n-old\n+new\n*** End Patch\n"
	call := llm.ToolCall{
		ID:          "call_patch_single",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: patchText,
	}
	call = toolCallWithPresentation(t, s, call)
	rendered := s.formatToolCall(call)
	if rendered.ToolCall == nil {
		t.Fatalf("expected tool metadata on patch call")
	}
	summary := rendered.ToolCall.PatchSummary
	detail := rendered.ToolCall.PatchDetail
	if summary != "./dir/a.go +1 -1" {
		t.Fatalf("unexpected one-line summary: %q", summary)
	}
	if rendered.ToolCall.PatchRender == nil {
		t.Fatalf("expected typed patch render metadata, got %+v", rendered.ToolCall)
	}
	if strings.Contains(summary, "\n") {
		t.Fatalf("expected one-line summary, got %q", summary)
	}
	if !strings.HasPrefix(detail, "/workspace/dir/a.go") {
		t.Fatalf("expected one-line detail header, got %q", detail)
	}
}

func TestPatchToolCallFormattingFallsBackToRawPatchWhenFileViewParseFails(t *testing.T) {
	s := newChatStore()
	s.cwd = "/workspace"

	patchText := "not a structured patch payload"
	call := llm.ToolCall{
		ID:          "call_patch_raw",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: patchText,
	}
	call = toolCallWithPresentation(t, s, call)
	rendered := s.formatToolCall(call)
	if rendered.ToolCall == nil {
		t.Fatalf("expected tool metadata on patch call fallback")
	}
	if rendered.ToolCall.RenderHint == nil || rendered.ToolCall.RenderHint.Kind != transcript.ToolRenderKindDiff {
		t.Fatalf("expected diff render hint for patch fallback, got %+v", rendered.ToolCall.RenderHint)
	}
	if rendered.ToolCall.PatchSummary != "Patch" {
		t.Fatalf("expected fallback patch summary, got %q", rendered.ToolCall.PatchSummary)
	}
	if rendered.ToolCall.PatchRender == nil {
		t.Fatalf("expected fallback typed patch render metadata, got %+v", rendered.ToolCall)
	}
	if !strings.Contains(rendered.ToolCall.PatchDetail, patchText) {
		t.Fatalf("expected fallback patch detail to include raw payload, got %q", rendered.ToolCall.PatchDetail)
	}
}

func TestFormatToolCallShellAddsShellMetadata(t *testing.T) {
	s := newChatStore()
	call := llm.ToolCall{
		ID:    "call_shell",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"cat cli/tui/model.go"}`),
	}
	call = toolCallWithPresentation(t, s, call)

	rendered := s.formatToolCall(call)
	if rendered.ToolCall == nil || !rendered.ToolCall.IsShell {
		t.Fatalf("expected shell metadata, got %+v", rendered.ToolCall)
	}
	if rendered.ToolCallID != "call_shell" {
		t.Fatalf("unexpected shell call id: %+v", rendered)
	}
	if rendered.ToolCall.RenderHint == nil {
		t.Fatalf("expected shell render hint, got %+v", rendered.ToolCall)
	}
	if rendered.ToolCall.RenderHint.Kind != transcript.ToolRenderKindSource {
		t.Fatalf("expected source render hint kind, got %+v", rendered.ToolCall.RenderHint)
	}
	if rendered.ToolCall.RenderHint.Path != "cli/tui/model.go" {
		t.Fatalf("unexpected source render hint path: %+v", rendered.ToolCall.RenderHint)
	}
	if !rendered.ToolCall.RenderHint.ResultOnly {
		t.Fatalf("expected result-only shell render hint, got %+v", rendered.ToolCall.RenderHint)
	}
	if !strings.Contains(rendered.Text, "cat cli/tui/model.go") {
		t.Fatalf("expected command in rendered shell call, got %q", rendered.Text)
	}
}

func TestFormatToolCallShellCapturesUserInitiatedMarker(t *testing.T) {
	s := newChatStore()
	call := llm.ToolCall{
		ID:    "call_shell_user",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd","user_initiated":true}`),
	}
	call = toolCallWithPresentation(t, s, call)

	rendered := s.formatToolCall(call)
	if rendered.ToolCall == nil {
		t.Fatalf("expected tool metadata, got nil")
	}
	if !rendered.ToolCall.UserInitiated {
		t.Fatalf("expected user initiated shell metadata, got %+v", rendered.ToolCall)
	}
}

func TestFormatToolCallWriteStdinPollUsesDurationInTranscript(t *testing.T) {
	s := newChatStore()
	call := llm.ToolCall{
		ID:    "call_poll",
		Name:  string(toolspec.ToolWriteStdin),
		Input: json.RawMessage(`{"session_id":1149,"yield_time_ms":2000}`),
	}
	call = toolCallWithPresentation(t, s, call)

	rendered := s.formatToolCall(call)
	if rendered.Role != "tool_call" {
		t.Fatalf("expected tool_call role, got %+v", rendered)
	}
	if rendered.Text != "Polled session 1149 for 2s" {
		t.Fatalf("expected transcript poll summary, got %q", rendered.Text)
	}
	if rendered.ToolCall == nil {
		t.Fatalf("expected tool metadata, got nil")
	}
	if rendered.ToolCall.Command != "Polled session 1149 for 2s" {
		t.Fatalf("expected tool command to match transcript summary, got %+v", rendered.ToolCall)
	}
	if rendered.ToolCall.TimeoutLabel != "" {
		t.Fatalf("did not expect timeout label for write_stdin poll, got %+v", rendered.ToolCall)
	}
	if !rendered.ToolCall.IsShell {
		t.Fatalf("expected write_stdin to remain marked as shell-like, got %+v", rendered.ToolCall)
	}
	if rendered.ToolCall.RenderHint == nil || rendered.ToolCall.RenderHint.Kind != transcript.ToolRenderKindPlain {
		t.Fatalf("expected plain render hint for write_stdin poll summary, got %+v", rendered.ToolCall.RenderHint)
	}
}

func TestFormatToolCallAskQuestionUsesQuestionAndSuggestionsMeta(t *testing.T) {
	s := newChatStore()
	call := llm.ToolCall{
		ID:    "call_ask",
		Name:  string(toolspec.ToolAskQuestion),
		Input: json.RawMessage(`{"question":"Choose scope?","suggestions":["flat scan","Recursive scan"],"recommended_option_index":1}`),
	}
	call = toolCallWithPresentation(t, s, call)

	rendered := s.formatToolCall(call)
	if rendered.Role != "tool_call" {
		t.Fatalf("expected tool_call role, got %+v", rendered)
	}
	if rendered.ToolCallID != "call_ask" {
		t.Fatalf("unexpected ask_question call id: %+v", rendered)
	}
	if rendered.ToolCall == nil {
		t.Fatalf("expected ask_question metadata, got nil")
	}
	if rendered.ToolCall.ToolName != string(toolspec.ToolAskQuestion) {
		t.Fatalf("unexpected ask_question tool name: %+v", rendered.ToolCall)
	}
	if rendered.Text != "Choose scope?" {
		t.Fatalf("expected rendered question text only, got %q", rendered.Text)
	}
	if strings.Contains(rendered.Text, "question:") || strings.Contains(rendered.Text, "suggestions:") {
		t.Fatalf("expected rendered ask_question text without labels, got %q", rendered.Text)
	}
	if rendered.ToolCall.Question != "Choose scope?" {
		t.Fatalf("unexpected ask_question metadata question: %+v", rendered.ToolCall)
	}
	if rendered.ToolCall.Command != "Choose scope?" {
		t.Fatalf("unexpected ask_question metadata command: %+v", rendered.ToolCall)
	}
	if len(rendered.ToolCall.Suggestions) != 2 {
		t.Fatalf("expected 2 suggestions, got %+v", rendered.ToolCall.Suggestions)
	}
	if rendered.ToolCall.Suggestions[0] != "flat scan" || rendered.ToolCall.Suggestions[1] != "Recursive scan" {
		t.Fatalf("unexpected ask_question suggestions: %+v", rendered.ToolCall.Suggestions)
	}
	if rendered.ToolCall.RecommendedOptionIndex != 1 {
		t.Fatalf("unexpected ask_question recommended option index: %+v", rendered.ToolCall)
	}
}

func TestChatStoreSnapshotFormatsAskQuestionStructuredAnswer(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:    "call_ask",
			Name:  string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"Choose scope?","suggestions":["full","fast"]}`),
		}},
	})
	s.appendMessage(llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: "call_ask",
		Name:       string(toolspec.ToolAskQuestion),
		Content:    `"ask result summary"`,
	})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %+v", snap.Entries)
	}
	if snap.Entries[1].Role != "tool_result_ok" {
		t.Fatalf("expected ask result entry, got %+v", snap.Entries[1])
	}
	want := "ask result summary"
	if snap.Entries[1].Text != want {
		t.Fatalf("unexpected ask result text: %q", snap.Entries[1].Text)
	}
}

func TestFormatToolCallAskQuestionRejectsApprovalShapeAtToolLayer(t *testing.T) {
	s := newChatStore()
	call := llm.ToolCall{
		ID:    "call_ask",
		Name:  string(toolspec.ToolAskQuestion),
		Input: json.RawMessage(`{"question":"Approve?","approval":true}`),
	}
	call = toolCallWithPresentation(t, s, call)

	rendered := s.formatToolCall(call)
	if rendered.Text != "Approve?" {
		t.Fatalf("expected ask question text preserved for invalid approval-shaped tool call, got %q", rendered.Text)
	}
	if rendered.ToolCall == nil || rendered.ToolCall.Question != "Approve?" {
		t.Fatalf("expected ask metadata question preserved, got %+v", rendered.ToolCall)
	}
}

func TestFormatToolCallAskQuestionDropsImpossibleRecommendedMetadataAfterNormalization(t *testing.T) {
	s := newChatStore()
	call := llm.ToolCall{
		ID:    "call_ask",
		Name:  string(toolspec.ToolAskQuestion),
		Input: json.RawMessage(`{"question":"Choose scope?","suggestions":["", "beta"],"recommended_option_index":2}`),
	}
	call = toolCallWithPresentation(t, s, call)

	rendered := s.formatToolCall(call)
	if rendered.ToolCall == nil {
		t.Fatalf("expected ask metadata, got nil")
	}
	if len(rendered.ToolCall.Suggestions) != 1 || rendered.ToolCall.Suggestions[0] != "beta" {
		t.Fatalf("unexpected normalized suggestions: %+v", rendered.ToolCall)
	}
	if rendered.ToolCall.RecommendedOptionIndex != 0 {
		t.Fatalf("expected impossible recommended index to be dropped, got %+v", rendered.ToolCall)
	}
}

func TestChatStoreShowsCompactionSummaryMessage(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"})
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "real user input"})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 2 {
		t.Fatalf("expected 2 visible entries, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	if snap.Entries[0].Role != string(transcript.EntryRoleCompactionSummary) || snap.Entries[0].Text != "summary" || snap.Entries[0].CondensedText == "" || snap.Entries[0].CondensedText != snap.Entries[0].CompactLabel {
		t.Fatalf("unexpected compaction summary entry: %+v", snap.Entries[0])
	}
	if snap.Entries[1].Role != "user" || snap.Entries[1].Text != "real user input" {
		t.Fatalf("unexpected visible entry: %+v", snap.Entries[1])
	}
}

func TestChatStoreSnapshotIncludesDeveloperErrorFeedbackAsOngoingVisibleRole(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "task"})
	s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: "phase mismatch warning"})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "done"})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	if snap.Entries[1].Role != string(transcript.EntryRoleDeveloperFeedback) || snap.Entries[1].Text != "phase mismatch warning" {
		t.Fatalf("expected developer error feedback mapped to ongoing-visible role, got %+v", snap.Entries[1])
	}
	if snap.Entries[1].CompactLabel != "" {
		t.Fatalf("expected developer error feedback to avoid generic compact label, got %+v", snap.Entries[1])
	}
}

func TestChatStoreSnapshotIncludesDeveloperContextAsVerboseRole(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeAgentsMD, Content: "AGENTS context"})
	s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeEnvironment, Content: "Environment context"})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	if snap.Entries[0].Role != string(transcript.EntryRoleDeveloperContext) || snap.Entries[0].Text != "AGENTS context" {
		t.Fatalf("unexpected developer context entry: %+v", snap.Entries[0])
	}
	if snap.Entries[1].Role != string(transcript.EntryRoleDeveloperContext) || snap.Entries[1].Text != "Environment context" {
		t.Fatalf("unexpected environment context entry: %+v", snap.Entries[1])
	}
	if snap.Entries[0].Visibility != transcript.EntryVisibilityVerbose || snap.Entries[1].Visibility != transcript.EntryVisibilityVerbose {
		t.Fatalf("expected developer context visibility to be verbose, got %+v", snap.Entries)
	}
}

func TestChatStoreSnapshotIncludesUnknownDeveloperMessagesAsVerboseContext(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageType("custom_internal"), Content: "Internal developer note"})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	if got := snap.Entries[0]; got.Role != string(transcript.EntryRoleDeveloperContext) || got.Text != "Internal developer note" || got.Visibility != transcript.EntryVisibilityVerbose || got.MessageType != llm.MessageType("custom_internal") || got.CompactLabel != "Developer context: custom_internal" {
		t.Fatalf("unexpected unknown developer context entry: %+v", got)
	}
}

func TestChatStoreSnapshotIncludesInterruptionAsVerboseRole(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeInterruption, Content: "Interrupted by user."})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	if snap.Entries[0].Role != string(transcript.EntryRoleInterruption) || snap.Entries[0].Text != "Interrupted by user." {
		t.Fatalf("unexpected interruption entry: %+v", snap.Entries[0])
	}
	if snap.Entries[0].Visibility != transcript.EntryVisibilityVerbose {
		t.Fatalf("expected interruption verbose visibility, got %+v", snap.Entries[0])
	}
}

func TestChatStoreSnapshotIncludesDeveloperCompactionSoonReminderAsWarningRole(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "task"})
	s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeCompactionSoonReminder, Content: "heads up"})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "done"})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	if snap.Entries[1].Role != "warning" || snap.Entries[1].Text != "heads up" {
		t.Fatalf("expected compaction reminder mapped to warning role, got %+v", snap.Entries[1])
	}
	if snap.Entries[1].CompactLabel != "Compaction reminder" {
		t.Fatalf("expected compaction reminder compact label, got %+v", snap.Entries[1])
	}
}

func TestChatStoreSnapshotIncludesHeadlessModeVariantsAsDeveloperContext(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessMode, Content: "headless mode instructions"})
	s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHeadlessModeExit, Content: "interactive mode instructions"})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	if snap.Entries[0].Role != string(transcript.EntryRoleDeveloperContext) || snap.Entries[0].Text != "headless mode instructions" {
		t.Fatalf("unexpected headless mode context entry: %+v", snap.Entries[0])
	}
	if snap.Entries[1].Role != string(transcript.EntryRoleDeveloperContext) || snap.Entries[1].Text != "interactive mode instructions" {
		t.Fatalf("unexpected headless mode exit context entry: %+v", snap.Entries[1])
	}
}

func TestChatStoreSnapshotIncludesHandoffFutureMessageAsDeveloperContext(t *testing.T) {
	s := newChatStore()
	s.appendMessage(handoffFutureAgentMessage("resume with tests"))

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	if snap.Entries[0].Role != string(transcript.EntryRoleDeveloperContext) || snap.Entries[0].Text != prompts.FormatHandoffFutureAgentMessage("resume with tests") {
		t.Fatalf("unexpected handoff future message entry: %+v", snap.Entries[0])
	}
}

func TestChatStoreSnapshotOmitsRawReviewerFeedbackDeveloperMessages(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "task"})
	s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeReviewerFeedback, Content: "reviewer internal prompt"})
	s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "reviewer_suggestions", Text: "Supervisor suggested:\n1. First", CondensedText: "Supervisor made 1 suggestion."})
	s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "reviewer_status", Text: "Supervisor ran: 1 suggestion, applied."})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 3 {
		t.Fatalf("expected 3 visible transcript entries, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	for _, entry := range snap.Entries {
		if entry.Text == "reviewer internal prompt" || entry.Role == string(transcript.EntryRoleDeveloperFeedback) {
			t.Fatalf("expected raw reviewer feedback developer message to stay hidden, got %+v", snap.Entries)
		}
	}
	if snap.Entries[1].Role != "reviewer_suggestions" || snap.Entries[2].Role != "reviewer_status" {
		t.Fatalf("expected reviewer transcript roles to represent reviewer feedback, got %+v", snap.Entries)
	}
}

func TestChatStoreSnapshotIncludesCompactTextForBackgroundNotice(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{
		Role:           llm.RoleDeveloper,
		MessageType:    llm.MessageTypeBackgroundNotice,
		Content:        "Background shell 1000 completed.\nExit code: 0\nOutput:\nlong output",
		CompactContent: "Background shell 1000 completed (exit 0)",
	})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d (%+v)", len(snap.Entries), snap.Entries)
	}
	if snap.Entries[0].Role != "system" {
		t.Fatalf("expected system role, got %+v", snap.Entries[0])
	}
	if snap.Entries[0].Text != "Background shell 1000 completed.\nExit code: 0\nOutput:\nlong output" {
		t.Fatalf("unexpected detail text: %+v", snap.Entries[0])
	}
	if snap.Entries[0].CondensedText != "Background shell 1000 completed (exit 0)" {
		t.Fatalf("unexpected ongoing text: %+v", snap.Entries[0])
	}
}

func TestChatStoreSnapshotShowsManualCompactionCarryoverAsVerboseMessage(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeManualCompactionCarryover,
		Content:     "# Last user message before manual compaction\n\nplease keep tests green",
	})

	snap := s.snapshotWithMetadata().Snapshot
	if len(snap.Entries) != 1 {
		t.Fatalf("expected carryover message to project once into transcript, got %+v", snap.Entries)
	}
	if got := snap.Entries[0]; got.Role != string(transcript.EntryRoleManualCompactionCarryover) || got.Text != "# Last user message before manual compaction\n\nplease keep tests green" || got.Visibility != transcript.EntryVisibilityVerbose {
		t.Fatalf("unexpected carryover transcript entry: %+v", got)
	}
}

func TestTranscriptDeliverySnapshotIncludesCompleteActiveSegmentBeyondLegacyTailLimit(t *testing.T) {
	s := newChatStore()
	const count = 650
	for i := 0; i < count; i++ {
		s.appendMessage(llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("message-%03d", i)})
	}

	snapshot := s.deliverySnapshot()
	if len(snapshot.Rows) != count {
		t.Fatalf("delivery snapshot rows = %d, want complete active segment %d", len(snapshot.Rows), count)
	}
	if snapshot.Rows[0].User == nil || snapshot.Rows[0].User.Text != "message-000" {
		t.Fatalf("first delivery row = %+v, want oldest active row", snapshot.Rows[0])
	}
	if last := snapshot.Rows[len(snapshot.Rows)-1]; last.User == nil || last.User.Text != "message-649" {
		t.Fatalf("last delivery row = %+v, want newest active row", last)
	}
}

func TestTranscriptDeliverySnapshotRetainsFinalizedAssistantStreamID(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "streamed", Phase: llm.MessagePhaseFinal})
	streamID := uuid.New()
	s.recordAssistantStreamFinalization(0, &streamID)

	snapshot := s.deliverySnapshot()
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].Assistant == nil || snapshot.Rows[0].Assistant.StreamID == nil {
		t.Fatalf("delivery rows = %+v, want assistant row with stream id", snapshot.Rows)
	}
	if *snapshot.Rows[0].Assistant.StreamID != streamID {
		t.Fatalf("stream id = %v, want %v", *snapshot.Rows[0].Assistant.StreamID, streamID)
	}
}

func TestTranscriptDeliverySnapshotRetainsFinalizedAssistantStreamIDAfterToolStart(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{
		Role:    llm.RoleAssistant,
		Content: "checking",
		ToolCalls: []llm.ToolCall{{
			ID:   "call-1",
			Name: "shell",
		}},
	})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "streamed", Phase: llm.MessagePhaseFinal})
	streamID := uuid.New()
	s.recordAssistantStreamFinalization(2, &streamID)

	snapshot := s.deliverySnapshot()
	if len(snapshot.Rows) != 2 || snapshot.Rows[1].Assistant == nil || snapshot.Rows[1].Assistant.StreamID == nil {
		t.Fatalf("delivery rows = %+v, want second assistant row with stream id", snapshot.Rows)
	}
	if *snapshot.Rows[1].Assistant.StreamID != streamID {
		t.Fatalf("stream id = %v, want %v", *snapshot.Rows[1].Assistant.StreamID, streamID)
	}
}

func TestTranscriptDeliverySnapshotRetainsFinalizedAssistantStreamIDAfterCompactionBase(t *testing.T) {
	s := newChatStore()
	prunedStreamID := uuid.New()
	s.recordAssistantStreamFinalization(4, &prunedStreamID)
	activeSegmentStart := 5
	s.replaceHistoryAtCommittedEntryStart(nil, &activeSegmentStart)
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "streamed", Phase: llm.MessagePhaseFinal})
	streamID := uuid.New()
	s.recordAssistantStreamFinalization(activeSegmentStart, &streamID)
	if _, ok := s.assistantStreamIDsByEntry[4]; ok {
		t.Fatalf("stream IDs below active segment start were retained: %+v", s.assistantStreamIDsByEntry)
	}

	snapshot := s.deliverySnapshot()
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].Assistant == nil || snapshot.Rows[0].Assistant.StreamID == nil {
		t.Fatalf("delivery rows = %+v, want assistant row with stream id", snapshot.Rows)
	}
	if *snapshot.Rows[0].Assistant.StreamID != streamID {
		t.Fatalf("stream id = %v, want %v", *snapshot.Rows[0].Assistant.StreamID, streamID)
	}
}

func TestTranscriptDeliverySnapshotPreservesProjectedHistoryReplacementRows(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "before compaction"})
	activeSegmentStart := 7
	items := llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleUser, Content: "user text"},
		{Role: llm.RoleAssistant, Content: "assistant text", Phase: llm.MessagePhaseFinal},
		{Role: llm.RoleTool, ToolCallID: "call-1", Name: "shell", Content: `{"output":"done"}`},
	})
	s.replaceHistoryAtCommittedEntryStart(items, &activeSegmentStart)

	snapshot := s.deliverySnapshot()
	if len(snapshot.Rows) != 3 {
		t.Fatalf("delivery rows = %+v, want user, assistant, tool rows", snapshot.Rows)
	}
	if snapshot.Rows[0].User == nil || snapshot.Rows[0].User.Text != "user text" {
		t.Fatalf("first row = %+v, want projected user row", snapshot.Rows[0])
	}
	if snapshot.Rows[1].Assistant == nil || snapshot.Rows[1].Assistant.Text != "assistant text" {
		t.Fatalf("second row = %+v, want projected assistant row", snapshot.Rows[1])
	}
	if snapshot.Rows[2].Tool == nil || snapshot.Rows[2].Tool.ToolCallID != "call-1" {
		t.Fatalf("third row = %+v, want projected tool row", snapshot.Rows[2])
	}
}

func TestTranscriptFactsPreserveNoticeContent(t *testing.T) {
	facts := TranscriptCommittedRowFactsFromEvent(Event{
		Kind: EventLocalEntryAdded,
		LocalEntry: &ChatEntry{
			Role:          "warning",
			Text:          "cleanup warning",
			CondensedText: "cleanup",
			NoticeID:      "notice-1",
		},
	})
	if len(facts) != 1 || facts[0].Notice == nil {
		t.Fatalf("facts = %+v, want notice fact", facts)
	}
	notice := facts[0].Notice
	if notice.DiagnosticDetail != "cleanup warning" || notice.CondensedText != "cleanup" || notice.NoticeID == nil || *notice.NoticeID != "notice-1" || notice.Severity != "warning" {
		t.Fatalf("notice fact = %+v, want preserved local-entry content", notice)
	}
}

func TestTranscriptFactsPreserveSpecialMessageContentAsNotice(t *testing.T) {
	facts := TranscriptCommittedRowFactsFromEvent(Event{
		Kind: EventConversationUpdated,
		Message: llm.Message{
			Role:           llm.RoleDeveloper,
			MessageType:    llm.MessageTypeCompactionSummary,
			Content:        "full compaction summary",
			CompactContent: "compact summary",
		},
	})
	if len(facts) != 1 || facts[0].Notice == nil {
		t.Fatalf("facts = %+v, want notice fact", facts)
	}
	notice := facts[0].Notice
	if notice.DiagnosticDetail != "full compaction summary" || notice.CondensedText != "compact summary" || notice.MessageType != llm.MessageTypeCompactionSummary {
		t.Fatalf("notice fact = %+v, want preserved developer message content", notice)
	}
}

func TestTranscriptFactsIncludeInFlightClearFailures(t *testing.T) {
	facts := TranscriptCommittedRowFactsFromEvent(Event{Kind: EventInFlightClearFailed, Error: "cleanup failed"})
	if len(facts) != 1 || facts[0].Notice == nil || facts[0].Notice.DiagnosticDetail != "cleanup failed" || facts[0].Notice.Severity != "error" {
		t.Fatalf("facts = %+v, want in-flight clear failure notice", facts)
	}
}

func TestTranscriptFactsPreserveCacheWarningVisibility(t *testing.T) {
	facts := TranscriptCommittedRowFactsFromEvent(Event{
		Kind:                   EventCacheWarning,
		CacheWarning:           &transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonNonPostfix, LostInputTokens: 42},
		CacheWarningVisibility: transcript.EntryVisibilityVerbose,
	})
	if len(facts) != 1 || facts[0].Notice == nil || facts[0].Notice.CacheWarning == nil {
		t.Fatalf("facts = %+v, want cache warning notice", facts)
	}
	if facts[0].Notice.CacheWarning.Visibility != transcript.EntryVisibilityVerbose {
		t.Fatalf("visibility = %q, want verbose", facts[0].Notice.CacheWarning.Visibility)
	}
}

func TestTranscriptDeliverySnapshotIncludesPostCompactionLocalEntries(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "one"})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "two", Phase: llm.MessagePhaseFinal})
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "three"})
	activeSegmentStart := 3
	s.replaceHistoryAtCommittedEntryStart(nil, &activeSegmentStart)

	s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "warning", Text: "post-compaction warning"})
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "after compaction"})
	s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "system", Text: "after message"})

	snapshot := s.deliverySnapshot()
	if len(snapshot.Rows) != 3 {
		t.Fatalf("delivery rows = %+v, want warning notice, user row, system notice", snapshot.Rows)
	}
	if snapshot.Rows[0].Notice == nil || snapshot.Rows[0].Notice.DiagnosticDetail != "post-compaction warning" {
		t.Fatalf("first row = %+v, want post-compaction warning notice", snapshot.Rows[0])
	}
	if snapshot.Rows[1].User == nil || snapshot.Rows[1].User.Text != "after compaction" {
		t.Fatalf("second row = %+v, want user row", snapshot.Rows[1])
	}
	if snapshot.Rows[2].Notice == nil || snapshot.Rows[2].Notice.DiagnosticDetail != "after message" {
		t.Fatalf("third row = %+v, want system notice after user row", snapshot.Rows[2])
	}
}

func TestTranscriptDeliverySnapshotHydratesLocalEntriesAsLiveTypedFacts(t *testing.T) {
	s := newChatStore()
	entry := ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "reviewer_status", Text: "Supervisor ran: 1 suggestion.", CondensedText: "1 suggestion", NoticeID: "notice-7"}
	s.appendLocalEntryRecord(entry)

	snapshot := s.deliverySnapshot()
	live := TranscriptCommittedRowFactsFromEvent(Event{Kind: EventLocalEntryAdded, LocalEntry: &entry})
	if !reflect.DeepEqual(snapshot.Rows, live) {
		t.Fatalf("hydrated rows = %+v, live facts = %+v, want identical typed facts", snapshot.Rows, live)
	}
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].Notice == nil || snapshot.Rows[0].Notice.Reason != "runtime_diagnostic" {
		t.Fatalf("hydrated rows = %+v, want typed runtime diagnostic notice", snapshot.Rows)
	}
}

func TestTranscriptDeliverySnapshotFossilizesOnlyUntypedLocalEntries(t *testing.T) {
	s := newChatStore()
	s.appendLocalEntryRecord(ChatEntry{Text: "ancient untyped row"})

	snapshot := s.deliverySnapshot()
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].Notice == nil {
		t.Fatalf("delivery rows = %+v, want one notice", snapshot.Rows)
	}
	notice := snapshot.Rows[0].Notice
	if notice.Reason != "legacy_untyped_notice" || notice.LegacyText == nil || *notice.LegacyText != "ancient untyped row" {
		t.Fatalf("notice = %+v, want legacy fossil with text", notice)
	}
}

func TestTranscriptDeliveryLiveAndHydrationAgreeOnProjectedCompactionEntries(t *testing.T) {
	s := newChatStore()
	activeSegmentStart := 7
	items := llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleUser, Content: "user text"},
		{Role: llm.RoleAssistant, Content: "assistant text", Phase: llm.MessagePhaseFinal, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "shell"}}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Name: "shell", Content: `{"output":"done"}`},
	})
	s.replaceHistoryAtCommittedEntryStart(items, &activeSegmentStart)

	hydrated := s.deliverySnapshot().Rows
	live := make([]TranscriptCommittedRowFact, 0, len(hydrated))
	for _, entry := range transcriptEntriesFromHistoryReplacement(llm.PrepareOpenAIInputItems(items)) {
		copyEntry := entry
		live = append(live, TranscriptCommittedRowFactsFromEvent(Event{Kind: EventLocalEntryAdded, LocalEntry: &copyEntry})...)
	}
	if !reflect.DeepEqual(hydrated, live) {
		t.Fatalf("hydrated rows = %+v, live facts = %+v, want identical projections", hydrated, live)
	}
	if len(hydrated) != 3 || hydrated[0].User == nil || hydrated[1].Assistant == nil || hydrated[2].Tool == nil {
		t.Fatalf("rows = %+v, want typed user, assistant, tool rows", hydrated)
	}
}
