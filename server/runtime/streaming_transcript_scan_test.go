package runtime

import (
	"encoding/json"
	"reflect"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func streamScanTestEvent(t *testing.T, kind string, payload any) session.Event {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s payload: %v", kind, err)
	}
	return session.Event{Kind: kind, Payload: raw}
}

func streamScanRepresentativeEvents(t *testing.T) []session.Event {
	t.Helper()
	return []session.Event{
		streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "task one"}),
		streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "answer one"}),
		streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "task two"}),
		streamScanTestEvent(t, "message", llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   llm.MessagePhaseCommentary,
			Content: "running one tool",
			ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"ls"}`)},
			},
		}),
		streamScanTestEvent(t, "tool_completed", storedToolCompletion{CallID: "call-1", Name: string(toolspec.ToolExecCommand), Output: json.RawMessage(`{"output":"files"}`)}),
		streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: "call-1", Name: string(toolspec.ToolExecCommand), Content: `{"output":"files"}`}),
		streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "done two"}),
		streamScanTestEvent(t, "local_entry", storedLocalEntry{Visibility: transcript.EntryVisibilityAuto, Role: "system", Text: "a local note"}),
		streamScanTestEvent(t, sessionEventCacheWarning, transcript.CacheWarning{}),
		streamScanTestEvent(t, "history_replaced", historyReplacementPayload{
			Engine: "compaction",
			Items: llm.ItemsFromMessages([]llm.Message{
				{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary so far"},
			}),
		}),
		streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "task three"}),
		streamScanTestEvent(t, "message", llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   llm.MessagePhaseCommentary,
			Content: "running two tools",
			ToolCalls: []llm.ToolCall{
				{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"a"}`)},
				{ID: "call-3", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"b"}`)},
			},
		}),
		streamScanTestEvent(t, "tool_completed", storedToolCompletion{CallID: "call-2", Name: string(toolspec.ToolExecCommand), Output: json.RawMessage(`{"output":"a-out"}`)}),
		streamScanTestEvent(t, "tool_completed", storedToolCompletion{CallID: "call-3", Name: string(toolspec.ToolExecCommand), Output: json.RawMessage(`{"output":"b-out"}`)}),
		streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: "call-2", Name: string(toolspec.ToolExecCommand), Content: `{"output":"a-out"}`}),
		streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleTool, ToolCallID: "call-3", Name: string(toolspec.ToolExecCommand), Content: `{"output":"b-out"}`}),
		streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "final answer"}),
	}
}

func TestStreamingTranscriptScanSeedsLastFinalAnswerFromCompactionBoundary(t *testing.T) {
	events := []session.Event{
		streamScanTestEvent(t, "history_replaced", historyReplacementPayload{
			Engine:                            "compaction",
			LastCommittedAssistantFinalAnswer: "retained final answer",
			Items: llm.ItemsFromMessages([]llm.Message{
				{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary so far"},
			}),
		}),
	}
	scan := newStreamingTranscriptScan(inMemoryTranscriptScanRequest{Offset: 0, Limit: 0}, config.CacheWarningModeDefault)
	applyEventsToStreaming(t, scan, events)
	if got, want := scan.LastCommittedAssistantFinalAnswer(), "retained final answer"; got != want {
		t.Fatalf("scan last final answer = %q, want boundary-seeded %q", got, want)
	}
}

func TestStreamingTranscriptScanBoundarySeedOverriddenByLaterFinalAnswer(t *testing.T) {
	events := []session.Event{
		streamScanTestEvent(t, "history_replaced", historyReplacementPayload{
			Engine:                            "compaction",
			LastCommittedAssistantFinalAnswer: "stale boundary answer",
			Items: llm.ItemsFromMessages([]llm.Message{
				{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary so far"},
			}),
		}),
		streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "newer final answer"}),
	}
	scan := newStreamingTranscriptScan(inMemoryTranscriptScanRequest{Offset: 0, Limit: 0}, config.CacheWarningModeDefault)
	applyEventsToStreaming(t, scan, events)
	if got, want := scan.LastCommittedAssistantFinalAnswer(), "newer final answer"; got != want {
		t.Fatalf("scan last final answer = %q, want later final %q", got, want)
	}
}

func TestStreamingTranscriptScanKeepsToolAttachedLocalEntryAfterMaterializedOutput(t *testing.T) {
	callID := "call-fallback"
	presentation := transcript.ToolCallMeta{
		ToolName: string(toolspec.ToolPatch),
	}
	expectedPresentation := transcript.NormalizeToolCallMeta(presentation)
	events := []session.Event{
		streamScanTestEvent(t, "message", llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID:   callID,
				Name: string(toolspec.ToolPatch),
			}},
		}),
		streamScanTestEvent(t, "tool_completed", storedToolCompletion{
			CallID:       callID,
			Name:         string(toolspec.ToolPatch),
			Output:       json.RawMessage(`{"ok":true}`),
			Presentation: &presentation,
		}),
		streamScanTestEvent(t, "local_entry", storedLocalEntry{
			Role:            string(transcript.EntryRoleDeveloperErrorFeedback),
			Text:            "presentation fallback",
			AfterToolCallID: &callID,
		}),
		streamScanTestEvent(t, "message", llm.Message{
			Role:        llm.RoleTool,
			ToolCallID:  callID,
			Name:        string(toolspec.ToolPatch),
			Content:     `{"ok":true}`,
			MessageType: llm.ToolOutputMessageType(true),
		}),
		streamScanTestEvent(t, "message", llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   llm.MessagePhaseFinal,
			Content: "done",
		}),
	}

	snapshot := fullStreamingProjection(t, events)
	if got := len(snapshot.Entries); got != 4 {
		t.Fatalf("entry count = %d, want one tool call, one tool result, one operator row, and one assistant row: %+v", got, snapshot.Entries)
	}
	if got := snapshot.Entries[0]; got.Role != "tool_call" || got.ToolCallID != callID {
		t.Fatalf("entry[0] = %+v, want tool call row", got)
	}
	if got := snapshot.Entries[1]; got.Role != "tool_result_ok" || got.ToolCallID != callID || got.ToolCall == nil || !reflect.DeepEqual(*got.ToolCall, expectedPresentation) {
		t.Fatalf("entry[1] = %+v, want one finalized tool result row", got)
	}
	if got := snapshot.Entries[2]; got.Role != string(transcript.EntryRoleDeveloperErrorFeedback) {
		t.Fatalf("entry[2] = %+v, want operator feedback after tool output", got)
	}
	if got := snapshot.Entries[3]; got.Role != "assistant" {
		t.Fatalf("entry[3] = %+v, want following assistant row", got)
	}
}

func applyEventsToStreaming(t *testing.T, scan *streamingTranscriptScan, events []session.Event) {
	t.Helper()
	for _, evt := range events {
		if err := scan.ApplyPersistedEvent(evt); err != nil {
			t.Fatalf("streaming apply %s: %v", evt.Kind, err)
		}
	}
}

func fullStreamingProjection(t *testing.T, events []session.Event) ChatSnapshot {
	t.Helper()
	scan := newStreamingTranscriptScan(inMemoryTranscriptScanRequest{Offset: 0, Limit: 0}, config.CacheWarningModeDefault)
	applyEventsToStreaming(t, scan, events)
	return scan.PageSnapshot().Snapshot
}

func TestStreamingTranscriptScanPagesAreWindowsOfFullProjection(t *testing.T) {
	events := streamScanRepresentativeEvents(t)
	full := fullStreamingProjection(t, events).Entries
	total := len(full)

	pageRequests := []struct {
		offset int
		limit  int
	}{
		{0, 0},
		{0, 3},
		{2, 4},
		{5, 2},
		{100, 5},
	}
	for _, req := range pageRequests {
		scan := newStreamingTranscriptScan(inMemoryTranscriptScanRequest{Offset: req.offset, Limit: req.limit}, config.CacheWarningModeDefault)
		applyEventsToStreaming(t, scan, events)
		got := scan.PageSnapshot()

		wantOffset := req.offset
		if wantOffset > total {
			wantOffset = total
		}
		wantEnd := total
		if req.limit > 0 && wantOffset+req.limit < wantEnd {
			wantEnd = wantOffset + req.limit
		}
		want := full[wantOffset:wantEnd]

		if got.TotalEntries != total || got.Offset != wantOffset {
			t.Fatalf("page(%d,%d) totals: got {total=%d off=%d} want {total=%d off=%d}", req.offset, req.limit, got.TotalEntries, got.Offset, total, wantOffset)
		}
		if len(got.Snapshot.Entries) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got.Snapshot.Entries, want)) {
			t.Fatalf("page(%d,%d) entries diverged from full-projection window: got %d want %d entries", req.offset, req.limit, len(got.Snapshot.Entries), len(want))
		}
	}
}

func TestStreamingTranscriptScanRecentTailIsSuffixOfFullProjection(t *testing.T) {
	events := streamScanRepresentativeEvents(t)
	full := fullStreamingProjection(t, events).Entries
	total := len(full)

	for _, tailLimit := range []int{1, 3, 100} {
		scan := newStreamingTranscriptScan(inMemoryTranscriptScanRequest{TrackRecentTail: true, TailLimit: tailLimit}, config.CacheWarningModeDefault)
		applyEventsToStreaming(t, scan, events)
		got := scan.RecentTailSnapshot()

		if got.TotalEntries != total {
			t.Fatalf("tail(%d) total: got %d want %d", tailLimit, got.TotalEntries, total)
		}
		if got.Offset < 0 || got.Offset > total {
			t.Fatalf("tail(%d) offset %d out of range (total=%d)", tailLimit, got.Offset, total)
		}
		// The recent tail is always a contiguous suffix of the full projection.
		want := full[got.Offset:]
		if len(got.Snapshot.Entries) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got.Snapshot.Entries, want)) {
			t.Fatalf("tail(%d) entries are not the suffix at offset %d: got %d want %d entries", tailLimit, got.Offset, len(got.Snapshot.Entries), len(want))
		}
	}
}

func TestStreamingTranscriptScanMetadata(t *testing.T) {
	events := streamScanRepresentativeEvents(t)
	full := fullStreamingProjection(t, events).Entries

	scan := newStreamingTranscriptScan(inMemoryTranscriptScanRequest{Offset: 0, Limit: 0}, config.CacheWarningModeDefault)
	applyEventsToStreaming(t, scan, events)

	if got := scan.TotalEntries(); got != len(full) {
		t.Fatalf("total entries: got %d want %d", got, len(full))
	}
	if got, want := scan.LastCommittedAssistantFinalAnswer(), "final answer"; got != want {
		t.Fatalf("last committed final answer: got %q want %q", got, want)
	}
}

func TestStreamingTranscriptScanRetainsOnlyWindow(t *testing.T) {
	const (
		messages  = 5000
		tailLimit = 12
		pageLimit = 8
	)
	events := make([]session.Event, 0, messages*2)
	for i := 0; i < messages; i++ {
		events = append(events, streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleUser, Content: "u"}))
		events = append(events, streamScanTestEvent(t, "message", llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "a"}))
	}

	tail := newStreamingTranscriptScan(inMemoryTranscriptScanRequest{TrackRecentTail: true, TailLimit: tailLimit}, config.CacheWarningModeDefault)
	applyEventsToStreaming(t, tail, events)
	snap := tail.RecentTailSnapshot()
	if snap.TotalEntries != messages*2 {
		t.Fatalf("tail total entries = %d, want %d", snap.TotalEntries, messages*2)
	}
	if len(tail.scan.tailEntries) > tailLimit {
		t.Fatalf("tail retained %d entries, exceeds window %d", len(tail.scan.tailEntries), tailLimit)
	}

	page := newStreamingTranscriptScan(inMemoryTranscriptScanRequest{Offset: 10, Limit: pageLimit}, config.CacheWarningModeDefault)
	applyEventsToStreaming(t, page, events)
	if got := page.PageSnapshot(); got.TotalEntries != messages*2 {
		t.Fatalf("page total entries = %d, want %d", got.TotalEntries, messages*2)
	}
	if len(page.scan.pageEntries) > pageLimit {
		t.Fatalf("page retained %d entries, exceeds window %d", len(page.scan.pageEntries), pageLimit)
	}
}
