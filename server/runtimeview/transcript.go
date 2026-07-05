package runtimeview

import (
	"strings"

	"core/server/runtime"
	"core/shared/clientui"
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
		engine.TranscriptRevision(),
		segment,
	), nil
}

func TranscriptPageFromSegment(sessionID, sessionName string, freshness clientui.ConversationFreshness, revision int64, page runtime.TranscriptSegmentPage) clientui.TranscriptPage {
	snapshot := ChatSnapshotFromRuntime(page.Snapshot)
	return clientui.TranscriptPage{
		SessionID:             sessionID,
		SessionName:           sessionName,
		ConversationFreshness: freshness,
		Revision:              revision,
		OlderCursor:           transcriptCursor(page.HasMoreAbove, page.OlderCursor),
		HasMoreAbove:          page.HasMoreAbove,
		NewerCursor:           transcriptCursor(page.HasMoreBelow, page.NewerCursor),
		HasMoreBelow:          page.HasMoreBelow,
		Entries:               cloneChatEntries(snapshot.Entries),
		Streaming:             snapshot.Streaming,
		StreamingMetadata:     snapshot.StreamingMetadata,
		StreamingError:        snapshot.StreamingError,
	}
}

func transcriptCursor(hasMore bool, cursor int64) *int64 {
	if !hasMore {
		return nil
	}
	return &cursor
}

func CommittedTranscriptSuffixFromRuntime(engine *runtime.Engine, _ clientui.CommittedTranscriptSuffixRequest) (clientui.CommittedTranscriptSuffix, error) {
	if engine == nil {
		return clientui.CommittedTranscriptSuffix{}, nil
	}
	segment, err := engine.TranscriptNewestSegmentPage()
	if err != nil {
		return clientui.CommittedTranscriptSuffix{}, err
	}
	return CommittedTranscriptSuffixFromSegment(
		engine.SessionID(),
		engine.SessionName(),
		ConversationFreshnessFromSession(engine.ConversationFreshness()),
		engine.TranscriptRevision(),
		segment,
	), nil
}

func CommittedTranscriptSuffixFromSegment(sessionID, sessionName string, freshness clientui.ConversationFreshness, revision int64, page runtime.TranscriptSegmentPage) clientui.CommittedTranscriptSuffix {
	snapshot := ChatSnapshotFromRuntime(page.Snapshot)
	entries := cloneChatEntries(snapshot.Entries)
	return clientui.CommittedTranscriptSuffix{
		SessionID:               sessionID,
		SessionName:             sessionName,
		ConversationFreshness:   freshness,
		Revision:                revision,
		CommittedEntryCount:     len(entries),
		StartEntryCount:         0,
		NextEntryCount:          len(entries),
		HasMoreCommittedEntries: page.HasMoreAbove,
		Entries:                 entries,
	}
}

func ChatSnapshotFromRuntime(snapshot runtime.ChatSnapshot) clientui.ChatSnapshot {
	entries := make([]clientui.ChatEntry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if isSuppressedNoopAssistantEntry(entry) {
			continue
		}
		entries = append(entries, clientui.ChatEntry{
			Visibility:        clientui.EntryVisibility(entry.Visibility),
			RollbackTargetID:  entry.RollbackTargetID,
			Role:              entry.Role,
			Text:              entry.Text,
			CondensedText:     entry.CondensedText,
			Phase:             clientui.MessagePhase(entry.Phase),
			MessageType:       clientui.MessageType(entry.MessageType),
			SourcePath:        entry.SourcePath,
			CompactLabel:      entry.CompactLabel,
			ToolResultSummary: entry.ToolResultSummary,
			ToolCallID:        entry.ToolCallID,
			NoticeID:          entry.NoticeID,
			ToolCall:          cloneToolCallMeta(entry.ToolCall),
		})
	}
	streaming := snapshot.Streaming
	if strings.TrimSpace(streaming) == runtimeNoopFinalToken {
		streaming = ""
	}
	return clientui.ChatSnapshot{
		Entries:           entries,
		Streaming:         streaming,
		StreamingMetadata: assistantStreamMetadataFromRuntime(snapshot.StreamingMetadata),
		StreamingError:    snapshot.StreamingError,
	}
}

func assistantStreamMetadataFromRuntime(metadata *runtime.AssistantStreamMetadata) *clientui.AssistantStreamMetadata {
	if metadata == nil {
		return nil
	}
	return &clientui.AssistantStreamMetadata{
		StepID:                  metadata.StepID,
		BaseRevision:            metadata.BaseRevision,
		BaseCommittedEntryCount: metadata.BaseCommittedEntryCount,
	}
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
		copyEntry.ToolCall = cloneClientToolCallMeta(entry.ToolCall)
		cloned = append(cloned, copyEntry)
	}
	return cloned
}

func cloneClientToolCallMeta(meta *clientui.ToolCallMeta) *clientui.ToolCallMeta {
	if meta == nil {
		return nil
	}
	copyMeta := *meta
	if len(meta.Suggestions) > 0 {
		copyMeta.Suggestions = append([]string(nil), meta.Suggestions...)
	}
	if meta.RenderHint != nil {
		renderHint := *meta.RenderHint
		copyMeta.RenderHint = &renderHint
	}
	if meta.PatchRender != nil {
		copyMeta.PatchRender = cloneRenderedPatch(meta.PatchRender)
	}
	return &copyMeta
}
