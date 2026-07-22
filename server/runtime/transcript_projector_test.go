package runtime

import (
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func appendPersistedTranscriptRecord(t *testing.T, store *session.Store, payload any) session.EventRecord {
	t.Helper()
	event, _, err := appendTestEvent(t, store, "step", payload)
	if err != nil {
		t.Fatalf("append persisted transcript event %T: %v", payload, err)
	}
	return event
}

func applyPersistedTranscriptRecords(t *testing.T, scan *PersistedTranscriptScan, records []session.EventRecord) {
	t.Helper()
	for _, record := range records {
		if err := scan.ApplyPersistedEvent(record); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(record), err)
		}
	}
}

func TestPersistedTranscriptScanReconstructsPersistedTranscript(t *testing.T) {
	store := mustCreateTestSession(t)
	toolOutput, err := json.Marshal(map[string]any{"ok": true})
	if err != nil {
		t.Fatalf("marshal tool output: %v", err)
	}
	records := []session.EventRecord{
		appendPersistedTranscriptRecord(t, store, llm.Message{Role: llm.RoleUser, Content: textutil.Value("hello")}),
		appendPersistedTranscriptRecord(t, store, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}}),
		appendPersistedTranscriptRecord(t, store, storedToolCompletion{
			CallID: "call-1",
			Name:   string(toolspec.ToolExecCommand),
			Output: toolOutput,
			ProviderItems: []llm.ResponseItem{{
				Type:   llm.ResponseItemTypeFunctionCallOutput,
				CallID: textutil.Value("call-1"),
				Name:   textutil.Value(string(toolspec.ToolExecCommand)),
				Output: toolOutput,
			}},
		}),
		appendPersistedTranscriptRecord(t, store, storedLocalEntry{Role: "system", Text: "persisted note"}),
		appendPersistedTranscriptRecord(t, store, llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("final answer"), Phase: textutil.Value(llm.MessagePhaseFinal)}),
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, records)

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
	store := mustCreateTestSession(t)
	records := []session.EventRecord{
		appendPersistedTranscriptRecord(t, store, llm.Message{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("user summary")}),
		appendPersistedTranscriptRecord(t, store, llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("developer handoff")}),
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, records)

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
	store := mustCreateTestSession(t)
	record := appendPersistedTranscriptRecord(t, store, storedLocalEntry{Role: "error", Text: "Exact token counting failed"})
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, []session.EventRecord{record})

	snapshot := scan.CollectedPageSnapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(snapshot.Entries))
	}
	if got := snapshot.Entries[0]; got.Role != "error" || got.Text != "Exact token counting failed" {
		t.Fatalf("entry[0] = %+v, want persisted error entry", got)
	}
}

func TestPersistedTranscriptScanPreservesPersistedLocalEntryNoticeID(t *testing.T) {
	store := mustCreateTestSession(t)
	record := appendPersistedTranscriptRecord(t, store, storedLocalEntry{Role: "system", Text: "Mirrored notice", NoticeID: textutil.Value("notice-1")})
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	applyPersistedTranscriptRecords(t, scan, []session.EventRecord{record})

	snapshot := scan.CollectedPageSnapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(snapshot.Entries))
	}
	if got := snapshot.Entries[0].NoticeID; got != "notice-1" {
		t.Fatalf("notice id = %q, want notice-1", got)
	}
}
