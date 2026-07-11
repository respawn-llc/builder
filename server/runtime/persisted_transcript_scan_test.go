package runtime

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestPersistedTranscriptScanCollectsRequestedPageOnly(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 1, Limit: 2})
	events := []session.Event{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "u1"}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: "a1", Phase: llm.MessagePhaseFinal}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "u2"}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: "a2", Phase: llm.MessagePhaseFinal}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", evt.Kind, err)
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
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{TrackRecentTail: true, TailLimit: 3})
	for i := 0; i < 5; i++ {
		if err := scan.ApplyPersistedEvent(mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "before-" + strconv.Itoa(i)})); err != nil {
			t.Fatalf("ApplyPersistedEvent before %d: %v", i, err)
		}
	}
	if err := scan.ApplyPersistedEvent(mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: "summary"}})})); err != nil {
		t.Fatalf("ApplyPersistedEvent(history_replaced): %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := scan.ApplyPersistedEvent(mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: "after-" + strconv.Itoa(i), Phase: llm.MessagePhaseFinal})); err != nil {
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
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{TrackRecentTail: true, TailLimit: 2})
	events := []session.Event{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "before"}),
		mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}})}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: "after-0", Phase: llm.MessagePhaseFinal}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: "after-1", Phase: llm.MessagePhaseFinal}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: "after-2", Phase: llm.MessagePhaseFinal}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", evt.Kind, err)
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

func TestPersistedTranscriptScanIgnoresLegacyReviewerRollbackHistoryReplacement(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.Event{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "before"}),
		mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}})}),
		mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Engine: "reviewer_rollback", Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: "rolled back"}})}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", evt.Kind, err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if got := len(page.Entries); got != 2 {
		t.Fatalf("len(page.Entries) = %d, want 2 (%+v)", got, page.Entries)
	}
	if got := page.Entries[0].Text; got != "before" {
		t.Fatalf("entry[0] = %q, want before", got)
	}
	if got := page.Entries[1].Text; got != "summary" {
		t.Fatalf("entry[1] = %q, want summary", got)
	}
}

func TestPersistedTranscriptScanWithoutLimitCollectsEntireDormantTranscript(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.Event{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "u1"}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: "a1", Phase: llm.MessagePhaseFinal}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "u2"}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", evt.Kind, err)
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
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 0, Limit: 10})
	toolOutput, err := json.Marshal(map[string]any{"ok": true})
	if err != nil {
		t.Fatalf("marshal tool output: %v", err)
	}
	events := []session.Event{
		mustPersistedScanEvent(t, "tool_completed", map[string]any{"call_id": "call-1", "name": string(toolspec.ToolExecCommand), "is_error": false, "output": json.RawMessage(toolOutput)}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: "call-1", Name: string(toolspec.ToolExecCommand)}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", evt.Kind, err)
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

func TestPersistedTranscriptScanProjectsUnknownDeveloperAndToolSummaryMetadata(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 0, Limit: 10})
	events := []session.Event{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageType("custom_internal"), Content: "Internal developer note"}),
		mustPersistedScanEvent(t, "tool_completed", map[string]any{"call_id": "call-1", "name": string(toolspec.ToolExecCommand), "is_error": true, "summary": "permission denied", "condensed_text": "permission denied compact", "output": json.RawMessage(`{"error":"permission denied"}`)}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"cat secret"}`)}}}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", evt.Kind, err)
		}
	}

	page := scan.CollectedPageSnapshot()
	if got := len(page.Entries); got != 3 {
		t.Fatalf("len(page.Entries) = %d, want 3 (%+v)", got, page.Entries)
	}
	developer := page.Entries[0]
	if developer.Role != string(transcript.EntryRoleDeveloperContext) || developer.Visibility != transcript.EntryVisibilityOngoing || developer.MessageType != llm.MessageType("custom_internal") || developer.CompactLabel != "Developer context: custom_internal" {
		t.Fatalf("unexpected unknown developer projection: %+v", developer)
	}
	result := page.Entries[2]
	if result.Role != "tool_result_error" || result.ToolResultSummary != "permission denied" || result.CondensedText != "permission denied compact" {
		t.Fatalf("unexpected tool result summary projection: %+v", result)
	}
}

func TestPersistedTranscriptScanSynthesizesCompletedToolResultWithoutToolMessage(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 0, Limit: 10})
	events := []session.Event{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Content: "working", ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}}),
		mustPersistedScanEvent(t, "tool_completed", map[string]any{"call_id": "call-1", "name": string(toolspec.ToolExecCommand), "is_error": false, "output": json.RawMessage(`{"output":"/tmp","exit_code":0,"truncated":false}`)}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", evt.Kind, err)
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
	singlePatch := "*** Begin Patch\n*** Update File: cli/app/ui_status.go\n@@\n type uiStatusAuthInfo struct {\n-\tSummary string\n+\tSummary string\n+\tReady bool\n }\n*** End Patch\n"
	multiPatch := "*** Begin Patch\n*** Update File: a.go\n+new\n*** Update File: b.go\n-old\n*** End Patch\n"
	rawPatch := "not a structured patch payload"
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 0, Limit: 10})
	events := []session.Event{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:          "call-patch-single",
			Name:        string(toolspec.ToolPatch),
			Custom:      true,
			CustomInput: singlePatch,
		}, {
			ID:          "call-patch-multi",
			Name:        string(toolspec.ToolPatch),
			Custom:      true,
			CustomInput: multiPatch,
		}, {
			ID:          "call-patch-raw",
			Name:        string(toolspec.ToolPatch),
			Custom:      true,
			CustomInput: rawPatch,
		}}}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: "call-patch-single", Name: string(toolspec.ToolPatch), MessageType: llm.MessageTypeCustomToolCallOutput, Content: `{}`}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: "call-patch-multi", Name: string(toolspec.ToolPatch), MessageType: llm.MessageTypeCustomToolCallOutput, Content: `{}`}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: "call-patch-raw", Name: string(toolspec.ToolPatch), MessageType: llm.MessageTypeCustomToolCallOutput, Content: `{}`}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", evt.Kind, err)
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
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.Event{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "before compaction"}),
		mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: "condensed provider summary", MessageType: llm.MessageTypeCompactionSummary}})}),
		mustPersistedScanEvent(t, "local_entry", storedLocalEntry{Role: "compaction_summary", Text: "condensed summary"}),
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeManualCompactionCarryover, Content: "Last user message before handoff\n\ncarry this forward"}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", evt.Kind, err)
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

func TestPersistedTranscriptScanMaterializesCompactedDeveloperContextInDetailPage(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.Event{
		mustPersistedScanEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "before compaction"}),
		mustPersistedScanEvent(t, "local_entry", storedLocalEntry{Role: "error", Text: "before replace"}),
		mustPersistedScanEvent(t, "history_replaced", historyReplacementPayload{Items: llm.ItemsFromMessages([]llm.Message{
			{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeEnvironment, Content: "environment info"},
			{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "condensed summary"},
		})}),
		mustPersistedScanEvent(t, "local_entry", storedLocalEntry{Role: "compaction_notice", Text: "after replace notice"}),
	}
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("ApplyPersistedEvent(%q): %v", evt.Kind, err)
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

func mustPersistedScanEvent(t *testing.T, kind string, payload any) session.Event {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %q payload: %v", kind, err)
	}
	return session.Event{Kind: kind, Payload: body}
}
