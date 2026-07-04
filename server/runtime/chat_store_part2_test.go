package runtime

import (
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"reflect"
	"testing"
)

func TestChatStoreSnapshotPreservesVisibleEntryOrdering(t *testing.T) {
	runChatEntryCases(t, []chatEntryCase{
		{
			name: "local entry ordering with developer error feedback",
			seed: func(s *chatStore) {
				s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "first"})
				s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "system", Text: "local-between"})
				s.appendMessage(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: "warn"})
				s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "done"})
			},
			want: []expectedChatEntry{
				{Role: "user", Text: "first"},
				{Role: "system", Text: "local-between"},
				{Role: string(transcript.EntryRoleDeveloperFeedback), Text: "warn"},
				{Role: "assistant", Text: "done"},
			},
		},
		{
			name: "local entries stay at insertion point",
			seed: func(s *chatStore) {
				s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "first"})
				s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "error", Text: "mid-error"})
				s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "second"})
			},
			want: []expectedChatEntry{
				{Role: "user", Text: "first"},
				{Role: "error", Text: "mid-error"},
				{Role: "assistant", Text: "second"},
			},
		},
	})
}

func TestChatStoreProviderHistoryStartsAtLastCompactionCheckpoint(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "before-1"})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "before-2"})

	replacement := []llm.ResponseItem{
		{Type: llm.ResponseItemTypeMessage, Role: llm.RoleDeveloper, Content: "ctx"},
		{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "compact-summary"},
	}
	s.replaceHistory(replacement)
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "after"})

	items := s.snapshotItems()
	if len(items) != 3 {
		t.Fatalf("expected 3 provider items, got %d (%+v)", len(items), items)
	}
	if items[0].Role != llm.RoleDeveloper || items[0].Content != "ctx" {
		t.Fatalf("unexpected replacement item[0]: %+v", items[0])
	}
	if items[1].Role != llm.RoleUser || items[1].Content != "compact-summary" {
		t.Fatalf("unexpected replacement item[1]: %+v", items[1])
	}
	if items[2].Role != llm.RoleUser || items[2].Content != "after" {
		t.Fatalf("expected post-compaction tail in provider history, got %+v", items[2])
	}
}

func TestPersistedTranscriptScanKeepsProjectedEntriesAcrossMultipleCompactions(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedScanEvents(t, scan, []session.Event{
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "before"}),
		historyReplacedEvent(t, []llm.Message{{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary-1"}}),
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "between"}),
		historyReplacedEvent(t, []llm.Message{{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary-2"}}),
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: "after"}),
	})

	assertChatEntries(t, scan.CollectedPageSnapshot().Entries, []expectedChatEntry{
		{Role: "user", Text: "before"},
		{Role: string(transcript.EntryRoleCompactionSummary), Text: "summary-1"},
		{Role: "user", Text: "between"},
		{Role: string(transcript.EntryRoleCompactionSummary), Text: "summary-2"},
		{Role: "assistant", Text: "after"},
	})
}

func TestChatStoreCompactionTrimsRetainedHistoryToWorkingSet(t *testing.T) {
	s := newChatStore()
	for i := 0; i < 600; i++ {
		s.appendMessage(llm.Message{
			Role:      llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{ID: callIDForIndex(i), Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
		})
		s.recordToolCompletionWithProviderItems(tools.Result{CallID: callIDForIndex(i), Name: toolspec.ToolExecCommand, Output: json.RawMessage(`{"output":"/tmp"}`)}, nil)
		s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "system", Text: "note"})
	}
	committedBeforeCompaction := s.committedEntryCount()

	s.replaceHistory([]llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}})
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "after"})

	if got := len(s.items); got > 4 {
		t.Fatalf("retained %d provider items after compaction, want bounded working set", got)
	}
	if got := len(s.local); got > 4 {
		t.Fatalf("retained %d local entries after compaction, want bounded working set", got)
	}
	if got := len(s.toolCompletions); got != 0 {
		t.Fatalf("retained %d tool completions after compaction, want pruned working set", got)
	}
	if got := s.committedEntryCount(); got <= committedBeforeCompaction {
		t.Fatalf("committed entry counter = %d, want cumulative total preserved across trim (was %d)", got, committedBeforeCompaction)
	}
}

func callIDForIndex(i int) string {
	return "call-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
}

func TestChatStoreProviderHistoryUsesMostRecentCompactionCheckpoint(t *testing.T) {
	s := newChatStore()
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "before"})

	s.replaceHistory([]llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "summary-1"}})
	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "between"})

	s.replaceHistory([]llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "summary-2"}})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "after"})

	items := s.snapshotItems()
	if len(items) != 2 {
		t.Fatalf("expected 2 provider items, got %d (%+v)", len(items), items)
	}
	if items[0].Content != "summary-2" || items[1].Content != "after" {
		t.Fatalf("expected latest replacement + tail, got %+v", items)
	}
}

func TestChatStoreSnapshotItemsPreservesMultiToolOutputOrdering(t *testing.T) {
	s := newChatStore()
	call1 := toolCallWithPresentation(t, s, llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)})
	call2 := toolCallWithPresentation(t, s, llm.ToolCall{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"ls"}`)})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call1, call2}})
	s.recordToolCompletionWithProviderItems(tools.Result{CallID: "call-1", Name: toolspec.ToolExecCommand, Output: json.RawMessage(`{"output":"/tmp"}`)}, nil)
	s.recordToolCompletionWithProviderItems(tools.Result{CallID: "call-2", Name: toolspec.ToolExecCommand, Output: json.RawMessage(`{"output":"a.txt"}`)}, nil)

	items := s.snapshotItems()
	if len(items) != 4 {
		t.Fatalf("expected 4 provider items, got %d (%+v)", len(items), items)
	}
	if items[0].Type != llm.ResponseItemTypeFunctionCall || items[0].CallID != "call-1" {
		t.Fatalf("unexpected first item: %+v", items[0])
	}
	if items[1].Type != llm.ResponseItemTypeFunctionCall || items[1].CallID != "call-2" {
		t.Fatalf("unexpected second item: %+v", items[1])
	}
	if items[2].Type != llm.ResponseItemTypeFunctionCallOutput || items[2].CallID != "call-1" {
		t.Fatalf("unexpected third item: %+v", items[2])
	}
	if items[3].Type != llm.ResponseItemTypeFunctionCallOutput || items[3].CallID != "call-2" {
		t.Fatalf("unexpected fourth item: %+v", items[3])
	}
}

func TestChatStoreSnapshotItemsPreservesMixedMaterializedAndPendingToolOutputs(t *testing.T) {
	s := newChatStore()
	call1 := toolCallWithPresentation(t, s, llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)})
	call2 := toolCallWithPresentation(t, s, llm.ToolCall{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"ls"}`)})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call1, call2}})
	s.recordToolCompletionWithProviderItems(tools.Result{CallID: "call-1", Name: toolspec.ToolExecCommand, Output: json.RawMessage(`{"output":"/tmp"}`)}, nil)
	s.recordToolCompletionWithProviderItems(tools.Result{CallID: "call-2", Name: toolspec.ToolExecCommand, Output: json.RawMessage(`{"output":"a.txt"}`)}, nil)
	s.appendMessage(llm.Message{Role: llm.RoleTool, ToolCallID: "call-1", Name: string(toolspec.ToolExecCommand), Content: `{"output":"/tmp"}`})

	items := s.snapshotItems()
	if len(items) != 4 {
		t.Fatalf("expected 4 provider items, got %d (%+v)", len(items), items)
	}
	if items[0].Type != llm.ResponseItemTypeFunctionCall || items[0].CallID != "call-1" {
		t.Fatalf("unexpected item[0]: %+v", items[0])
	}
	if items[1].Type != llm.ResponseItemTypeFunctionCall || items[1].CallID != "call-2" {
		t.Fatalf("unexpected item[1]: %+v", items[1])
	}
	if items[2].Type != llm.ResponseItemTypeFunctionCallOutput || items[2].CallID != "call-1" {
		t.Fatalf("unexpected item[2]: %+v", items[2])
	}
	if items[3].Type != llm.ResponseItemTypeFunctionCallOutput || items[3].CallID != "call-2" {
		t.Fatalf("unexpected item[3]: %+v", items[3])
	}
}

func TestChatStoreSnapshotItemsMatchesItemsFromMessagesWhenFullyMaterialized(t *testing.T) {
	s := newChatStore()
	call1 := toolCallWithPresentation(t, s, llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)})
	call2 := toolCallWithPresentation(t, s, llm.ToolCall{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"ls"}`)})
	messages := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call1, call2}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Name: string(toolspec.ToolExecCommand), Content: `{"output":"/tmp"}`},
		{Role: llm.RoleTool, ToolCallID: "call-2", Name: string(toolspec.ToolExecCommand), Content: `{"output":"a.txt"}`},
	}
	for _, msg := range messages {
		s.appendMessage(msg)
	}
	want := llm.ItemsFromMessages(messages)
	if got := s.snapshotItems(); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshotItems mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestChatStoreCommittedEntryCountTracksVisibleTranscript(t *testing.T) {
	s := newChatStore()
	if got := s.committedEntryCount(); got != 0 {
		t.Fatalf("initial committed entry count = %d, want 0", got)
	}

	s.appendMessage(llm.Message{Role: llm.RoleUser, Content: "hello"})
	if got := s.committedEntryCount(); got != 1 {
		t.Fatalf("after user message committed entry count = %d, want 1", got)
	}

	call := toolCallWithPresentation(t, s, llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)})
	s.appendMessage(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}})
	if got := s.committedEntryCount(); got != 2 {
		t.Fatalf("after assistant tool call committed entry count = %d, want 2", got)
	}

	s.recordToolCompletionWithProviderItems(tools.Result{CallID: "call-1", Name: toolspec.ToolExecCommand, Output: json.RawMessage(`{"output":"/tmp"}`)}, nil)
	if got := s.committedEntryCount(); got != 3 {
		t.Fatalf("after synthesized tool result committed entry count = %d, want 3", got)
	}

	s.appendMessage(llm.Message{Role: llm.RoleTool, ToolCallID: "call-1", Name: string(toolspec.ToolExecCommand), Content: `{"output":"/tmp"}`})
	if got := s.committedEntryCount(); got != 3 {
		t.Fatalf("materialized tool result should not double count, got %d want 3", got)
	}

	s.appendLocalEntryRecord(ChatEntry{Visibility: transcript.EntryVisibilityAuto, Role: "system", Text: "note"})
	if got := s.committedEntryCount(); got != 4 {
		t.Fatalf("after local entry committed entry count = %d, want 4", got)
	}

	if got := len(s.snapshotWithMetadata().Snapshot.Entries); got != s.committedEntryCount() {
		t.Fatalf("snapshot entry count = %d, committed entry count = %d", got, s.committedEntryCount())
	}
}
