package runtimeview

import (
	"strings"

	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/clientuicopy"
	"core/shared/transcript"
)

const RecentTailEntryLimit = 500

func TranscriptPageFromRuntime(engine *runtime.Engine, req clientui.TranscriptPageRequest) (clientui.TranscriptPage, error) {
	if engine == nil {
		return clientui.TranscriptPage{}, nil
	}
	var segment runtime.TranscriptSegmentPage
	var err error
	if req.NewerCursor != nil {
		segment, err = engine.TranscriptSegmentPageForward(*req.NewerCursor)
	} else if req.Cursor != nil {
		segment, err = engine.TranscriptSegmentPage(*req.Cursor)
	} else {
		segment, err = engine.TranscriptNewestSegmentPage()
	}
	if err != nil {
		return clientui.TranscriptPage{}, err
	}
	return TranscriptPageFromSegment(
		engine.SessionID(),
		engine.SessionName(),
		ConversationFreshnessFromSession(engine.ConversationFreshness()),
		segment,
	), nil
}

func TranscriptPageFromSegment(sessionID, sessionName string, freshness clientui.ConversationFreshness, page runtime.TranscriptSegmentPage) clientui.TranscriptPage {
	return clientui.TranscriptPage{
		SessionID:             sessionID,
		SessionName:           sessionName,
		ConversationFreshness: freshness,
		OlderCursor:           transcriptCursor(page.HasMoreAbove, page.OlderCursor),
		HasMoreAbove:          page.HasMoreAbove,
		NewerCursor:           transcriptCursor(page.HasMoreBelow, page.NewerCursor),
		HasMoreBelow:          page.HasMoreBelow,
		Entries:               chatEntriesFromRuntimeSnapshot(page.Snapshot),
	}
}

func transcriptCursor(hasMore bool, cursor int64) *int64 {
	if !hasMore {
		return nil
	}
	return &cursor
}

func cloneOptionalInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func chatEntriesFromRuntimeSnapshot(snapshot runtime.ChatSnapshot) []clientui.ChatEntry {
	entries := make([]clientui.ChatEntry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if isSuppressedNoopAssistantEntry(entry) {
			continue
		}
		messageType := clientui.MessageType(entry.MessageType)
		if transcript.IsReviewerEntryRole(strings.TrimSpace(entry.Role)) {
			messageType = clientui.MessageTypeReviewerFeedback
		}
		entries = append(entries, clientui.ChatEntry{
			Visibility:         clientui.EntryVisibility(entry.Visibility),
			RollbackTargetID:   entry.RollbackTargetID,
			Role:               entry.Role,
			Text:               entry.Text,
			CondensedText:      entry.CondensedText,
			Phase:              clientui.MessagePhase(entry.Phase),
			MessageType:        messageType,
			SourcePath:         entry.SourcePath,
			CompactLabel:       entry.CompactLabel,
			ToolResultSummary:  entry.ToolResultSummary,
			ToolCallID:         entry.ToolCallID,
			NoticeID:           entry.NoticeID,
			BackgroundExitCode: cloneOptionalInt(entry.BackgroundExitCode),
			ToolCall:           cloneToolCallMeta(entry.ToolCall),
		})
	}
	return entries
}

func isSuppressedNoopAssistantEntry(entry runtime.ChatEntry) bool {
	return strings.TrimSpace(entry.Role) == "assistant" && strings.TrimSpace(entry.Text) == runtimeNoopFinalToken
}

func cloneChatEntries(entries []clientui.ChatEntry) []clientui.ChatEntry {
	if len(entries) == 0 {
		return nil
	}
	cloned := make([]clientui.ChatEntry, 0, len(entries))
	for _, entry := range entries {
		copyEntry := entry
		copyEntry.ToolCall = clientuicopy.ToolCallMeta(entry.ToolCall)
		cloned = append(cloned, copyEntry)
	}
	return cloned
}
