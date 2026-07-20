package runtime

import (
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func applyPersistedScanEvents(t *testing.T, scan *PersistedTranscriptScan, events []session.EventRecord) {
	t.Helper()
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}
}

func TestPersistedTranscriptScanReconstructsPersistedTranscript(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	toolOutput, err := json.Marshal(map[string]any{"ok": true})
	if err != nil {
		t.Fatalf("marshal tool output: %v", err)
	}
	applyPersistedScanEvents(t, scan, []session.EventRecord{
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: textutil.Value("hello")}),
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}}),
		mustPersistedEvent(t, "tool_completed", storedToolCompletion{CallID: "call-1", Name: string(toolspec.ToolExecCommand), Output: toolOutput}),
		mustPersistedEvent(t, "local_entry", storedLocalEntry{Role: "system", Text: "persisted note"}),
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("final answer"), Phase: textutil.Value(llm.MessagePhaseFinal)}),
	})

	snapshot := scan.CollectedPageSnapshot()
	if len(snapshot.Entries) != 5 {
		t.Fatalf("entry count = %d, want 5", len(snapshot.Entries))
	}
	if snapshot.Entries[1].Role != "tool_call" {
		t.Fatalf("entry[1].Role = %q, want tool_call", snapshot.Entries[1].Role)
	}
	if snapshot.Entries[2].Role != "tool_result_ok" {
		t.Fatalf("entry[2].Role = %q, want tool_result_ok", snapshot.Entries[2].Role)
	}
	if snapshot.Entries[3].Role != "system" || snapshot.Entries[3].Text != "persisted note" {
		t.Fatalf("unexpected local entry: %+v", snapshot.Entries[3])
	}
	if got := scan.LastCommittedAssistantFinalAnswer(); got != "final answer" {
		t.Fatalf("LastCommittedAssistantFinalAnswer() = %q, want final answer", got)
	}
}

func TestPersistedTranscriptScanSurfacesPersistedCompactionSummaries(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedScanEvents(t, scan, []session.EventRecord{
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("user summary")}),
		mustPersistedEvent(t, "message", llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("developer handoff")}),
	})

	snapshot := scan.CollectedPageSnapshot()
	if len(snapshot.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(snapshot.Entries))
	}
	if got := snapshot.Entries[0]; got.Role != "compaction_summary" || got.Text != "user summary" {
		t.Fatalf("entry[0] = %+v, want user compaction summary", got)
	}
	if got := snapshot.Entries[1]; got.Role != "compaction_summary" || got.Text != "developer handoff" {
		t.Fatalf("entry[1] = %+v, want developer compaction summary", got)
	}
}

func TestPersistedTranscriptScanPreservesErrorLocalEntries(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedScanEvents(t, scan, []session.EventRecord{
		mustPersistedEvent(t, "local_entry", storedLocalEntry{Role: "error", Text: "Exact token counting failed"}),
	})

	snapshot := scan.CollectedPageSnapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(snapshot.Entries))
	}
	if got := snapshot.Entries[0]; got.Role != "error" || got.Text != "Exact token counting failed" {
		t.Fatalf("entry[0] = %+v, want persisted error entry", got)
	}
}

func TestPersistedTranscriptScanPreservesPersistedLocalEntryNoticeID(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedScanEvents(t, scan, []session.EventRecord{
		mustPersistedEvent(t, "local_entry", storedLocalEntry{Role: "system", Text: "Mirrored notice", NoticeID: textutil.Value("notice-1")}),
	})

	snapshot := scan.CollectedPageSnapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(snapshot.Entries))
	}
	if got := snapshot.Entries[0].NoticeID; got != "notice-1" {
		t.Fatalf("notice id = %q, want notice-1", got)
	}
}

func mustPersistedEvent(t *testing.T, kind string, payload any) session.EventRecord {
	t.Helper()
	return streamScanTestEvent(t, kind, payload)
}
