package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func appendSegmentTestMessage(t *testing.T, store *session.Store, role llm.Role, content string) {
	t.Helper()
	if _, _, err := store.AppendEvent("step", "message", llm.Message{Role: role, Content: content}); err != nil {
		t.Fatalf("append message %q: %v", content, err)
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
	if _, _, err := store.AppendEvent("step", "history_replaced", historyReplacementPayload{
		Engine: "compaction",
		Items:  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}}),
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
	if _, _, err := store.AppendEvent("step", "history_replaced", historyReplacementPayload{
		Engine: "compaction",
		Items:  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}}),
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

func TestEngineTranscriptSegmentPageRestoresToolCompletionDiagnosticAfterResult(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	diagnostic := transcript.NewDeletionFactMismatchDeveloperDiagnostic(
		"call-1",
		patchformat.WholeFileDeletionFactMismatchError{
			Kind: patchformat.WholeFileDeletionFactMismatchMissing,
			ID:   patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0},
		},
	)
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	events := []struct {
		kind    string
		payload any
	}{
		{
			kind: "message",
			payload: llm.Message{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{{
					ID:    "call-1",
					Name:  string(toolspec.ToolPatch),
					Input: json.RawMessage(`{"patch":"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n"}`),
				}},
			},
		},
		{
			kind: "tool_completed",
			payload: storedToolCompletion{
				CallID: "call-1",
				Name:   string(toolspec.ToolPatch),
				Output: json.RawMessage(`{"ok":true}`),
				Presentation: &transcript.ToolCallMeta{
					ToolName:    string(toolspec.ToolPatch),
					PatchRender: &rendered,
				},
				Diagnostic: &diagnostic,
			},
		},
		{
			kind: "message",
			payload: llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: "call-1",
				Name:       string(toolspec.ToolPatch),
				Content:    `{"ok":true}`,
			},
		},
	}
	for _, event := range events {
		if _, _, err := store.AppendEvent("step", event.kind, event.payload); err != nil {
			t.Fatalf("append %s: %v", event.kind, err)
		}
	}

	entries := mustEngineNewestSegmentPage(t, eng).Snapshot.Entries
	if len(entries) != 3 {
		t.Fatalf("entry count = %d, want tool call, result, diagnostic", len(entries))
	}
	if entries[1].Role != "tool_result_ok" ||
		entries[2].Role != string(transcript.EntryRoleDeveloperErrorFeedback) ||
		entries[1].StepID != entries[2].StepID ||
		entries[2].StepID != "step" ||
		!transcript.DeveloperDiagnosticEqual(entries[2].DeveloperDiagnostic, &diagnostic) {
		t.Fatalf("segment completion/diagnostic adjacency = %+v", entries)
	}
}
