package runtime

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestPersistedTranscriptScanCollectsRequestedPageOnly(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 1, Limit: 2})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: textutil.Value("u1")}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("a1"), Phase: textutil.Value(llm.MessagePhaseFinal)}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: textutil.Value("u2")}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("a2"), Phase: textutil.Value(llm.MessagePhaseFinal)}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if scan.TotalEntries() != 4 {
		t.Fatalf("TotalEntries() = %d, want 4", scan.TotalEntries())
	}
	if len(page.Entries) != 2 {
		t.Fatalf("len(page.Entries) = %d, want 2", len(page.Entries))
	}
	if page.Entries[0].Text != "a1" || page.Entries[1].Text != "u2" {
		t.Fatalf("unexpected page entries: %+v", page.Entries)
	}
}

func TestPersistedTranscriptScanTracksDormantRecentTailWindow(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{TrackRecentTail: true, TailLimit: 3})
	for i := 0; i < 5; i++ {
		if err := scan.ApplyPersistedEvent(mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: textutil.Value("before-" + strconv.Itoa(i))})); err != nil {
			t.Fatalf("ApplyPersistedEvent before %d: %v", i, err)
		}
	}
	if err := scan.ApplyPersistedEvent(mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("summary")}})})); err != nil {
		t.Fatalf("ApplyPersistedEvent(history_replaced): %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := scan.ApplyPersistedEvent(mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("after-" + strconv.Itoa(i)), Phase: textutil.Value(llm.MessagePhaseFinal)})); err != nil {
			t.Fatalf("ApplyPersistedEvent after %d: %v", i, err)
		}
	}

	window := scan.RecentTailSnapshot()
	if window.TotalEntries != 8 {
		t.Fatalf("window.TotalEntries = %d, want 8", window.TotalEntries)
	}
	if window.Offset != 5 {
		t.Fatalf("window.Offset = %d, want 5", window.Offset)
	}
	if len(window.Snapshot.Entries) != 3 {
		t.Fatalf("len(window.Snapshot.Entries) = %d, want 3", len(window.Snapshot.Entries))
	}
	if window.Snapshot.Entries[0].Text != "summary" || window.Snapshot.Entries[1].Text != "after-0" || window.Snapshot.Entries[2].Text != "after-1" {
		t.Fatalf("unexpected tail entries: %+v", window.Snapshot.Entries)
	}
}

func TestPersistedTranscriptScanKeepsLatestCompactionSegmentInDormantRecentTail(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{TrackRecentTail: true, TailLimit: 2})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: textutil.Value("before")}),
		mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")}})}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("after-0"), Phase: textutil.Value(llm.MessagePhaseFinal)}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("after-1"), Phase: textutil.Value(llm.MessagePhaseFinal)}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("after-2"), Phase: textutil.Value(llm.MessagePhaseFinal)}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}

	window := scan.RecentTailSnapshot()
	if got := window.Offset; got != 1 {
		t.Fatalf("window.Offset = %d, want 1", got)
	}
	if got := len(window.Snapshot.Entries); got != 4 {
		t.Fatalf("len(window.Snapshot.Entries) = %d, want 4 (%+v)", got, window.Snapshot.Entries)
	}
	if window.Snapshot.Entries[0].Text != "summary" || window.Snapshot.Entries[1].Text != "after-0" || window.Snapshot.Entries[2].Text != "after-1" || window.Snapshot.Entries[3].Text != "after-2" {
		t.Fatalf("unexpected dormant tail entries: %+v", window.Snapshot.Entries)
	}
}

func TestPersistedTranscriptScanWithoutLimitCollectsEntireDormantTranscript(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: textutil.Value("u1")}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("a1"), Phase: textutil.Value(llm.MessagePhaseFinal)}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: textutil.Value("u2")}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if scan.TotalEntries() != 3 {
		t.Fatalf("TotalEntries() = %d, want 3", scan.TotalEntries())
	}
	if len(page.Entries) != 3 {
		t.Fatalf("len(page.Entries) = %d, want 3", len(page.Entries))
	}
	if page.Entries[0].Text != "u1" || page.Entries[1].Text != "a1" || page.Entries[2].Text != "u2" {
		t.Fatalf("unexpected unbounded page entries: %+v", page.Entries)
	}
}

func TestPersistedTranscriptScanEnrichesToolResultFromCompletion(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 0, Limit: 10})
	toolOutput, err := json.Marshal(map[string]any{"ok": true})
	if err != nil {
		t.Fatalf("marshal tool output: %v", err)
	}
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "tool_completed", storedToolCompletion{CallID: "call-1", Name: string(toolspec.ToolExecCommand), Output: json.RawMessage(toolOutput)}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: textutil.Value("call-1"), Name: textutil.Value(string(toolspec.ToolExecCommand))}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if len(page.Entries) != 2 {
		t.Fatalf("len(page.Entries) = %d, want 2", len(page.Entries))
	}
	if page.Entries[1].Role != "tool_result_ok" {
		t.Fatalf("page.Entries[1].Role = %q, want tool_result_ok", page.Entries[1].Role)
	}
	if page.Entries[1].Text == "" {
		t.Fatalf("expected enriched tool result text, got empty entry: %+v", page.Entries[1])
	}
}

func TestPersistedTranscriptScanProjectsDeveloperAndToolSummaryMetadata(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 0, Limit: 10})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value("Internal developer note")}),
		mustPersistedScanEvent(t, "tool_completed", storedToolCompletion{CallID: "call-1", Name: string(toolspec.ToolExecCommand), IsError: true, Summary: textutil.Value("permission denied"), CondensedText: textutil.Value("permission denied compact"), Output: json.RawMessage(`{"error":"permission denied"}`)}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"cat secret"}`)}}}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if got := len(page.Entries); got != 3 {
		t.Fatalf("len(page.Entries) = %d, want 3 (%+v)", got, page.Entries)
	}
	developer := page.Entries[0]
	if developer.Role != string(transcript.EntryRoleDeveloperContext) || developer.Visibility != transcript.EntryVisibilityDetail || developer.MessageType != llm.MessageTypeEnvironment || developer.CompactLabel != "Environment info" {
		t.Fatalf("unexpected developer projection: %+v", developer)
	}
	result := page.Entries[2]
	if result.Role != "tool_result_error" || result.ToolResultSummary != "permission denied" || result.CondensedText != "permission denied compact" {
		t.Fatalf("unexpected tool result summary projection: %+v", result)
	}
}

func TestPersistedTranscriptScanSynthesizesCompletedToolResultWithoutToolMessage(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 0, Limit: 10})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}}),
		mustPersistedScanEvent(t, "tool_completed", storedToolCompletion{CallID: "call-1", Name: string(toolspec.ToolExecCommand), Output: json.RawMessage(`{"output":"/tmp","exit_code":0,"truncated":false}`)}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if len(page.Entries) != 3 {
		t.Fatalf("len(page.Entries) = %d, want 3", len(page.Entries))
	}
	if page.Entries[2].Role != "tool_result_ok" || page.Entries[2].ToolCallID != "call-1" {
		t.Fatalf("expected synthesized tool result, got %+v", page.Entries[2])
	}
	if page.Entries[2].Text == "" {
		t.Fatalf("expected synthesized tool result text, got empty entry: %+v", page.Entries[2])
	}
}

func TestFormatPersistedToolCallBuildsFallbackMetadata(t *testing.T) {
	t.Parallel()
	entry := formatPersistedToolCall(llm.ToolCall{
		ID:    "call-1",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	})
	if entry.Role != "tool_call" {
		t.Fatalf("entry role = %q, want tool_call", entry.Role)
	}
	if entry.Text != "pwd" {
		t.Fatalf("entry text = %q, want pwd", entry.Text)
	}
	if entry.ToolCall == nil || !entry.ToolCall.IsShell {
		t.Fatalf("expected shell tool metadata, got %+v", entry.ToolCall)
	}
	if entry.ToolCall.Command != "pwd" {
		t.Fatalf("tool command = %q, want pwd", entry.ToolCall.Command)
	}
}

func TestPersistedTranscriptScanRendersPatchToolCallsWithoutEditedLabel(t *testing.T) {
	t.Parallel()
	singlePatch := "*** Begin Patch\n*** Update File: cli/app/ui_status.go\n@@\n type uiStatusAuthInfo struct {\n-\tSummary string\n+\tSummary string\n+\tReady bool\n }\n*** End Patch\n"
	multiPatch := "*** Begin Patch\n*** Update File: a.go\n+new\n*** Update File: b.go\n-old\n*** End Patch\n"
	rawPatch := "not a structured patch payload"
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 0, Limit: 10})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:          "call-patch-single",
			Name:        string(toolspec.ToolPatch),
			Custom:      true,
			CustomInput: textutil.Value(singlePatch),
		}, {
			ID:          "call-patch-multi",
			Name:        string(toolspec.ToolPatch),
			Custom:      true,
			CustomInput: textutil.Value(multiPatch),
		}, {
			ID:          "call-patch-raw",
			Name:        string(toolspec.ToolPatch),
			Custom:      true,
			CustomInput: textutil.Value(rawPatch),
		}}}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: textutil.Value("call-patch-single"), Name: textutil.Value(string(toolspec.ToolPatch)), MessageType: textutil.Value(llm.MessageTypeCustomToolCallOutput), Content: textutil.Value(`{}`)}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: textutil.Value("call-patch-multi"), Name: textutil.Value(string(toolspec.ToolPatch)), MessageType: textutil.Value(llm.MessageTypeCustomToolCallOutput), Content: textutil.Value(`{}`)}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: textutil.Value("call-patch-raw"), Name: textutil.Value(string(toolspec.ToolPatch)), MessageType: textutil.Value(llm.MessageTypeCustomToolCallOutput), Content: textutil.Value(`{}`)}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if len(page.Entries) != 6 {
		t.Fatalf("len(page.Entries) = %d, want 6 (%+v)", len(page.Entries), page.Entries)
	}
	wantSummaries := map[string]string{
		"call-patch-single": "./cli/app/ui_status.go +2 -1",
		"call-patch-multi":  "./a.go +1\n./b.go -1",
		"call-patch-raw":    rawPatch,
	}
	for _, entry := range page.Entries {
		if entry.Role != "tool_call" {
			continue
		}
		want, ok := wantSummaries[entry.ToolCallID]
		if !ok {
			t.Fatalf("unexpected patch call id %q", entry.ToolCallID)
		}
		if entry.ToolCall == nil {
			t.Fatalf("expected persisted patch metadata for %s", entry.ToolCallID)
		}
		if entry.ToolCallID != "call-patch-raw" && entry.ToolCall.PatchRender == nil {
			t.Fatalf("expected persisted patch render metadata for %s, got %+v", entry.ToolCallID, entry.ToolCall)
		}
		if entry.ToolCall.PatchSummary != want {
			t.Fatalf("unexpected persisted patch summary for %s: got %q want %q", entry.ToolCallID, entry.ToolCall.PatchSummary, want)
		}
		if strings.Contains(entry.ToolCall.PatchSummary, "Edited:") || strings.Contains(entry.ToolCall.PatchDetail, "Edited:") || strings.Contains(entry.ToolCall.PatchSummary, "*** Begin Patch") {
			t.Fatalf("expected persisted patch metadata without Edited/raw payload for %s, got %+v", entry.ToolCallID, entry.ToolCall)
		}
		delete(wantSummaries, entry.ToolCallID)
	}
	if len(wantSummaries) != 0 {
		t.Fatalf("missing persisted patch calls: %+v", wantSummaries)
	}
}

func TestPersistedTranscriptScanProjectsCarryoverFromPersistedMessage(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: textutil.Value("before compaction")}),
		mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("condensed provider summary"), MessageType: textutil.Value(llm.MessageTypeCompactionSummary)}})}),
		mustPersistedScanEvent(t, "local_entry", storedLocalEntry{Role: "compaction_summary", Text: "condensed summary"}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeManualCompactionCarryover), Content: textutil.Value("Last user message before handoff\n\ncarry this forward")}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if len(page.Entries) != 4 {
		t.Fatalf("len(page.Entries) = %d, want 4 (%+v)", len(page.Entries), page.Entries)
	}
	if page.Entries[0].Role != "user" || page.Entries[0].Text != "before compaction" {
		t.Fatalf("expected preserved pre-compaction user entry, got %+v", page.Entries[0])
	}
	if page.Entries[1].Role != "compaction_summary" || page.Entries[1].Text != "condensed provider summary" {
		t.Fatalf("expected projected provider compaction summary entry, got %+v", page.Entries[1])
	}
	if page.Entries[2].Role != "compaction_summary" || page.Entries[2].Text != "condensed summary" {
		t.Fatalf("expected persisted compaction summary entry, got %+v", page.Entries[2])
	}
	if page.Entries[3].Role != "manual_compaction_carryover" || page.Entries[3].Text != "Last user message before handoff\n\ncarry this forward" {
		t.Fatalf("expected manual compaction carryover entry, got %+v", page.Entries[3])
	}
	if page.Entries[3].Visibility != transcript.EntryVisibilityDetail {
		t.Fatalf("expected carryover entry to stay detail-only, got %+v", page.Entries[3])
	}
}

func TestPersistedTranscriptScanProjectsCarryoverFromHistoryReplacement(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: textutil.Value("before compaction")}),
		mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Items: llm.ItemsFromMessages([]llm.Message{
			{Role: llm.RoleUser, Content: textutil.Value("condensed provider summary"), MessageType: textutil.Value(llm.MessageTypeCompactionSummary)},
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeManualCompactionCarryover), Content: textutil.Value("Last user message before handoff\n\ncarry this forward")},
		})}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if len(page.Entries) != 3 {
		t.Fatalf("len(page.Entries) = %d, want 3 (%+v)", len(page.Entries), page.Entries)
	}
	if page.Entries[1].Role != "compaction_summary" || page.Entries[1].Text != "condensed provider summary" {
		t.Fatalf("expected projected provider compaction summary entry, got %+v", page.Entries[1])
	}
	if page.Entries[2].Role != "manual_compaction_carryover" || page.Entries[2].Visibility != transcript.EntryVisibilityDetail {
		t.Fatalf("expected detail-only manual compaction carryover entry, got %+v", page.Entries[2])
	}
}

func TestPersistedTranscriptScanMaterializesCompactedDeveloperContextInDetailPage(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: textutil.Value("before compaction")}),
		mustPersistedScanEvent(t, "local_entry", storedLocalEntry{Role: "error", Text: "before replace"}),
		mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Items: llm.ItemsFromMessages([]llm.Message{
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value("environment info")},
			{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("condensed summary")},
		})}),
		mustPersistedScanEvent(t, "local_entry", storedLocalEntry{Role: "compaction_notice", Text: "after replace notice"}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", mustSessionEventKind(evt), err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if got := len(page.Entries); got != 5 {
		t.Fatalf("len(page.Entries) = %d, want 5 (%+v)", got, page.Entries)
	}
	if got := page.Entries[0]; got.Role != "user" || got.Text != "before compaction" {
		t.Fatalf("entry[0] = %+v, want preserved pre-compaction user entry", got)
	}
	if got := page.Entries[1]; got.Role != "error" || got.Text != "before replace" {
		t.Fatalf("entry[1] = %+v, want preserved pre-compaction local entry", got)
	}
	if got := page.Entries[2]; got.Role != "developer_context" || got.Text != "environment info" {
		t.Fatalf("entry[2] = %+v, want compacted developer context", got)
	}
	if got := page.Entries[3]; got.Role != "compaction_summary" || got.Text != "condensed summary" || got.CompactLabel != "Context compacted" || got.CondensedText != "Context compacted" {
		t.Fatalf("entry[3] = %+v, want compacted summary", got)
	}
	if got := page.Entries[4]; got.Role != "compaction_notice" || got.Text != "after replace notice" {
		t.Fatalf("entry[4] = %+v, want legacy local entry preserved without special handling", got)
	}
}

func TestPersistedTranscriptScanReplaysCacheWarnings(t *testing.T) {
	t.Parallel()
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	if err := scan.ApplyPersistedEvent(mustPersistedScanEvent(t, sessionEventCacheWarning, transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonNonPostfix})); err != nil {
		t.Fatalf("ApplyPersistedEvent(cache_warning): %v", err)
	}

	page := scan.CollectedPageSnapshot()
	if len(page.Entries) != 1 {
		t.Fatalf("len(page.Entries) = %d, want 1", len(page.Entries))
	}
	if page.Entries[0].Role != cacheWarningTranscriptRole {
		t.Fatalf("entry role = %q, want %q", page.Entries[0].Role, cacheWarningTranscriptRole)
	}
	if page.Entries[0].Text != transcript.CacheWarningText(transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonNonPostfix}) {
		t.Fatalf("unexpected cache warning text: %+v", page.Entries[0])
	}
}

func TestPersistedTranscriptScanUsesCacheWarningModeVisibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode config.CacheWarningMode
		want transcript.EntryVisibility
	}{
		{name: "default", mode: config.CacheWarningModeDefault, want: transcript.EntryVisibilityDetail},
		{name: "verbose", mode: config.CacheWarningModeVerbose, want: transcript.EntryVisibilityOngoing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{CacheWarningMode: tt.mode})
			if err := scan.ApplyPersistedEvent(mustPersistedScanEvent(t, sessionEventCacheWarning, transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonNonPostfix})); err != nil {
				t.Fatalf("ApplyPersistedEvent(cache_warning): %v", err)
			}

			page := scan.CollectedPageSnapshot()
			if len(page.Entries) != 1 {
				t.Fatalf("len(page.Entries) = %d, want 1", len(page.Entries))
			}
			if got := page.Entries[0].Visibility; got != tt.want {
				t.Fatalf("cache warning visibility = %q, want %q", got, tt.want)
			}
		})
	}
}

func mustPersistedScanEvent(t *testing.T, kind string, payload any) session.EventRecord {
	t.Helper()
	return streamScanTestEvent(t, kind, payload)
}
