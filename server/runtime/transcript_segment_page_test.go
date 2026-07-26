package runtime

import (
	"fmt"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/transcript"
)

func appendSegmentTestMessage(t *testing.T, store *session.Store, role llm.Role, content string) {
	t.Helper()
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{Role: role, Content: textutil.Value(content)}); err != nil {
		t.Fatalf("append message %q: %v", content, err)
	}
}

func appendSegmentTestMessages(t *testing.T, store *session.Store, role llm.Role, contents []string) {
	t.Helper()
	payloads := make([]session.EventRecordPayload, 0, len(contents))
	for _, content := range contents {
		message, err := sessionMessageRecordFromLLM(llm.Message{Role: role, Content: textutil.Value(content)})
		if err != nil {
			t.Fatalf("adapt message %q: %v", content, err)
		}
		payloads = append(payloads, message)
	}
	stepID := "step"
	events, receipt, err := mustMaterializeTestEventLog(t, store).AppendRecordsAtomic(&stepID, payloads)
	if err != nil {
		t.Fatalf("append messages: %v", err)
	}
	if !receipt.Committed || len(events) != len(contents) {
		t.Fatalf("append messages receipt=%+v events=%d, want committed %d events", receipt, len(events), len(contents))
	}
}

func mustEngineSegmentPage(t *testing.T, eng *Engine, cursor int64) TranscriptSegmentPage {
	t.Helper()
	page, err := eng.TranscriptSegmentPage(cursor)
	if err != nil {
		t.Fatalf("transcript segment page (cursor=%d): %v", cursor, err)
	}
	return page
}

func mustEngineNewestSegmentPage(t *testing.T, eng *Engine) TranscriptSegmentPage {
	t.Helper()
	page, err := eng.TranscriptNewestSegmentPage()
	if err != nil {
		t.Fatalf("newest transcript segment page: %v", err)
	}
	return page
}

func mustEngineSegmentPageForward(t *testing.T, eng *Engine, startOffset int64) TranscriptSegmentPage {
	t.Helper()
	page, err := eng.TranscriptSegmentPageForward(startOffset)
	if err != nil {
		t.Fatalf("transcript segment page forward (offset=%d): %v", startOffset, err)
	}
	return page
}

func segmentEntryTexts(page TranscriptSegmentPage) []string {
	texts := make([]string, 0, len(page.Snapshot.Entries))
	for _, entry := range page.Snapshot.Entries {
		if text := strings.TrimSpace(entry.Text); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

func containsText(texts []string, want string) bool {
	for _, text := range texts {
		if text == want {
			return true
		}
	}
	return false
}

func TestEngineTranscriptSegmentPagePaginatesAcrossCompaction(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})

	appendSegmentTestMessage(t, store, llm.RoleUser, "u1")
	appendSegmentTestMessage(t, store, llm.RoleAssistant, "a1")
	if _, _, err := appendTestEvent(t, store, "step", historyReplacementPayload{
		Engine: "compaction",
		Items:  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")}}),
	}); err != nil {
		t.Fatalf("append history_replaced: %v", err)
	}
	appendSegmentTestMessage(t, store, llm.RoleUser, "u2")
	appendSegmentTestMessage(t, store, llm.RoleAssistant, "a2")

	newest := mustEngineNewestSegmentPage(t, eng)
	newestTexts := segmentEntryTexts(newest)
	if !containsText(newestTexts, "u2") || !containsText(newestTexts, "a2") {
		t.Fatalf("newest segment must contain post-compaction turns, got %v", newestTexts)
	}
	if containsText(newestTexts, "u1") {
		t.Fatalf("newest segment must not contain pre-compaction turns, got %v", newestTexts)
	}
	if !newest.HasMoreAbove {
		t.Fatalf("newest segment after a compaction must report more above")
	}
	if newest.OlderCursor <= 0 {
		t.Fatalf("newest segment older cursor must point above, got %d", newest.OlderCursor)
	}

	older := mustEngineSegmentPage(t, eng, newest.OlderCursor)
	olderTexts := segmentEntryTexts(older)
	if !containsText(olderTexts, "u1") || !containsText(olderTexts, "a1") {
		t.Fatalf("older segment must contain pre-compaction turns, got %v", olderTexts)
	}
	if older.HasMoreAbove {
		t.Fatalf("oldest segment must not report more above, got cursor=%d", older.OlderCursor)
	}
}

func TestEngineTranscriptSegmentPageForwardMatchesBackwardSegments(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})

	appendSegmentTestMessage(t, store, llm.RoleUser, "u1")
	appendSegmentTestMessage(t, store, llm.RoleAssistant, "a1")
	if _, _, err := appendTestEvent(t, store, "step", historyReplacementPayload{
		Engine: "compaction",
		Items:  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")}}),
	}); err != nil {
		t.Fatalf("append history_replaced: %v", err)
	}
	appendSegmentTestMessage(t, store, llm.RoleUser, "u2")
	appendSegmentTestMessage(t, store, llm.RoleAssistant, "a2")

	newest := mustEngineNewestSegmentPage(t, eng)
	older := mustEngineSegmentPage(t, eng, newest.OlderCursor)
	if !older.HasMoreBelow || older.NewerCursor <= 0 {
		t.Fatalf("older segment must report more below with a forward cursor, got below=%t cursor=%d", older.HasMoreBelow, older.NewerCursor)
	}

	forward := mustEngineSegmentPageForward(t, eng, older.NewerCursor)
	forwardTexts := segmentEntryTexts(forward)
	if !containsText(forwardTexts, "u2") || !containsText(forwardTexts, "a2") {
		t.Fatalf("forward segment must contain post-compaction turns, got %v", forwardTexts)
	}
	if containsText(forwardTexts, "u1") {
		t.Fatalf("forward segment must not contain pre-compaction turns, got %v", forwardTexts)
	}
	if forward.HasMoreBelow {
		t.Fatalf("forward read into the newest segment must report no more below")
	}
	if !forward.HasMoreAbove {
		t.Fatalf("forward segment after a compaction must report more above")
	}
	if got, want := segmentEntryTexts(newest), forwardTexts; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("forward segment %v must match newest segment %v", want, got)
	}
}

func TestEngineTranscriptSegmentPageSingleSegment(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})

	appendSegmentTestMessage(t, store, llm.RoleUser, "only")
	appendSegmentTestMessage(t, store, llm.RoleAssistant, "answer")

	page := mustEngineNewestSegmentPage(t, eng)
	if page.HasMoreAbove {
		t.Fatalf("never-compacted session must not report more above")
	}
	texts := segmentEntryTexts(page)
	if !containsText(texts, "only") || !containsText(texts, "answer") {
		t.Fatalf("single segment must contain all turns, got %v", texts)
	}
}

func TestEngineTranscriptNewestSegmentPageIncludesCompleteActiveSegment(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})

	appendSegmentTestMessage(t, store, llm.RoleUser, "before compaction")
	if _, _, err := appendTestEvent(t, store, "step", historyReplacementPayload{
		Engine: "compaction",
	}); err != nil {
		t.Fatalf("append history replacement: %v", err)
	}

	const activeEntryCount = 650
	activeEntries := make([]string, activeEntryCount)
	for index := 0; index < activeEntryCount; index++ {
		activeEntries[index] = fmt.Sprintf("active-%03d", index)
	}
	appendSegmentTestMessages(t, store, llm.RoleUser, activeEntries)

	page := mustEngineNewestSegmentPage(t, eng)
	if !page.HasMoreAbove {
		t.Fatal("active segment after compaction must report retained history above")
	}
	if got := len(page.Snapshot.Entries); got != activeEntryCount {
		t.Fatalf("active segment entry count = %d, want %d", got, activeEntryCount)
	}
	for index, entry := range page.Snapshot.Entries {
		if entry.Role != "user" || entry.Text != fmt.Sprintf("active-%03d", index) {
			t.Fatalf("active segment entry[%d] = %+v", index, entry)
		}
	}
}

func TestEngineTranscriptNewestSegmentPageProjectsHistoryReplacementRows(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})

	appendSegmentTestMessage(t, store, llm.RoleUser, "before compaction")
	if _, _, err := appendTestEvent(t, store, "step", historyReplacementPayload{
		Engine: "compaction",
		Items: llm.ItemsFromMessages([]llm.Message{
			{
				Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment),
				Content: textutil.Value("environment state"),
			},
			{
				Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content: textutil.Value("condensed summary"),
			},
			{
				Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeManualCompactionCarryover),
				Content: textutil.Value("carry this forward"),
			},
		}),
	}); err != nil {
		t.Fatalf("append history replacement: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, "step", storedLocalEntry{
		Role: "compaction_summary", Text: "persisted summary",
	}); err != nil {
		t.Fatalf("append persisted compaction summary: %v", err)
	}

	page := mustEngineNewestSegmentPage(t, eng)
	var environment, summary, carryover, persistedSummary bool
	for _, entry := range page.Snapshot.Entries {
		if entry.Text == "before compaction" {
			t.Fatalf("newest segment retained pre-compaction entry: %+v", entry)
		}
		switch {
		case entry.Role == "developer_context" && entry.Text == "environment state" &&
			entry.Visibility == transcript.EntryVisibilityDetail:
			environment = true
		case entry.Role == "compaction_summary" && entry.Text == "condensed summary":
			summary = true
		case entry.Role == "manual_compaction_carryover" && entry.Text == "carry this forward" &&
			entry.Visibility == transcript.EntryVisibilityDetail:
			carryover = true
		case entry.Role == "compaction_summary" && entry.Text == "persisted summary":
			persistedSummary = true
		}
	}
	if !environment || !summary || !carryover || !persistedSummary {
		t.Fatalf(
			"newest segment rows: environment=%t summary=%t carryover=%t persisted_summary=%t entries=%+v",
			environment, summary, carryover, persistedSummary, page.Snapshot.Entries,
		)
	}
}
