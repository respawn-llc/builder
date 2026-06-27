package runtimeview

import (
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
	if req.NewerCursor > 0 {
		segment, err = engine.TranscriptSegmentPageForward(req.NewerCursor)
	} else {
		segment, err = engine.TranscriptSegmentPage(req.Cursor)
	}
	if err != nil {
		return clientui.TranscriptPage{}, err
	}
	page := TranscriptPageFromSegment(
		engine.SessionID(),
		engine.SessionName(),
		ConversationFreshnessFromSession(engine.ConversationFreshness()),
		engine.TranscriptRevision(),
		segment,
	)
	return page, nil
}

func TranscriptPageFromSegment(sessionID, sessionName string, freshness clientui.ConversationFreshness, revision int64, page runtime.TranscriptSegmentPage) clientui.TranscriptPage {
	snapshot := ChatSnapshotFromRuntime(page.Snapshot)
	return clientui.TranscriptPage{
		SessionID:             sessionID,
		SessionName:           sessionName,
		ConversationFreshness: freshness,
		Revision:              revision,
		OlderCursor:           page.OlderCursor,
		HasMoreAbove:          page.HasMoreAbove,
		NewerCursor:           page.NewerCursor,
		HasMoreBelow:          page.HasMoreBelow,
		Entries:               cloneChatEntries(snapshot.Entries),
		Streaming:             snapshot.Streaming,
		StreamingError:        snapshot.StreamingError,
	}
}

func CommittedTranscriptSuffixFromRuntime(engine *runtime.Engine, _ clientui.CommittedTranscriptSuffixRequest) (clientui.CommittedTranscriptSuffix, error) {
	if engine == nil {
		return clientui.CommittedTranscriptSuffix{}, nil
	}
	segment, err := engine.TranscriptSegmentPage(0)
	if err != nil {
		return clientui.CommittedTranscriptSuffix{}, err
	}
	suffix := CommittedTranscriptSuffixFromSegment(
		engine.SessionID(),
		engine.SessionName(),
		ConversationFreshnessFromSession(engine.ConversationFreshness()),
		engine.TranscriptRevision(),
		segment,
	)
	if segment.HasMoreAbove {
		total := engine.CommittedTranscriptEntryCount()
		suffix.CommittedEntryCount = total
		suffix.NextEntryCount = total
		if start := total - len(suffix.Entries); start >= 0 {
			suffix.StartEntryCount = start
		}
	}
	return suffix, nil
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
