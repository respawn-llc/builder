package app

import (
	"strings"

	"core/shared/clientui"
	"core/shared/clientuicopy"
	"core/shared/transcript"
)

const (
	uiDetailTranscriptMinResidentSegments = 2
)

type residentSegmentMeta struct {
	startLocal   int
	olderCursor  *int64
	hasMoreAbove bool
	newerCursor  *int64
	hasMoreBelow bool
}

type uiDetailTranscriptWindow struct {
	sessionID    string
	entries      []clientui.ChatEntry
	loaded       bool
	olderCursor  *int64
	hasMoreAbove bool
	newerCursor  *int64
	hasMoreBelow bool
	segments     []residentSegmentMeta
	lastRequest  clientui.TranscriptPageRequest
}

type uiDetailTranscriptMergeResult struct {
	addedEntries        int
	trimmedFrontEntries []clientui.ChatEntry
}

func (w uiDetailTranscriptWindow) page() clientui.TranscriptPage {
	return clientui.TranscriptPage{
		SessionID:    w.sessionID,
		OlderCursor:  w.olderCursor,
		HasMoreAbove: w.hasMoreAbove,
		NewerCursor:  w.newerCursor,
		HasMoreBelow: w.hasMoreBelow,
		Entries:      cloneDetailChatEntries(w.entries),
	}
}

func (w *uiDetailTranscriptWindow) reset() {
	if w == nil {
		return
	}
	*w = uiDetailTranscriptWindow{}
}

func (w *uiDetailTranscriptWindow) refreshBounds() {
	if w == nil || len(w.segments) == 0 {
		return
	}
	top := w.segments[0]
	bottom := w.segments[len(w.segments)-1]
	w.olderCursor = top.olderCursor
	w.hasMoreAbove = top.hasMoreAbove
	w.newerCursor = bottom.newerCursor
	w.hasMoreBelow = bottom.hasMoreBelow
}

func (w *uiDetailTranscriptWindow) refreshEdgeCursors(page clientui.TranscriptPage) {
	if w == nil || len(w.segments) == 0 {
		return
	}
	top := &w.segments[0]
	top.olderCursor = cloneInt64Pointer(page.OlderCursor)
	top.hasMoreAbove = page.HasMoreAbove
	bottom := &w.segments[len(w.segments)-1]
	bottom.newerCursor = cloneInt64Pointer(page.NewerCursor)
	bottom.hasMoreBelow = page.HasMoreBelow
	w.refreshBounds()
}

func segmentMetaFromPage(startLocal int, page clientui.TranscriptPage) residentSegmentMeta {
	return residentSegmentMeta{
		startLocal:   startLocal,
		olderCursor:  cloneInt64Pointer(page.OlderCursor),
		hasMoreAbove: page.HasMoreAbove,
		newerCursor:  cloneInt64Pointer(page.NewerCursor),
		hasMoreBelow: page.HasMoreBelow,
	}
}

func (w *uiDetailTranscriptWindow) apply(page clientui.TranscriptPage) {
	if w == nil {
		return
	}
	if w.loaded && transcriptPageSessionChanged(w.sessionID, page.SessionID) {
		w.replace(page)
		return
	}
	if !w.loaded {
		w.replace(page)
		return
	}
	w.merge(page)
}

func (w uiDetailTranscriptWindow) matchesPage(page clientui.TranscriptPage) bool {
	if !w.loaded {
		return false
	}
	if transcriptPageSessionChanged(w.sessionID, page.SessionID) {
		return false
	}
	if len(w.entries) != len(page.Entries) {
		return false
	}
	for i := range page.Entries {
		if !clientChatEntryEqual(w.entries[i], page.Entries[i]) {
			return false
		}
	}
	return true
}

func (w *uiDetailTranscriptWindow) replace(page clientui.TranscriptPage) {
	if w == nil {
		return
	}
	w.sessionID = strings.TrimSpace(page.SessionID)
	w.entries = cloneDetailChatEntries(page.Entries)
	w.loaded = true
	w.segments = []residentSegmentMeta{segmentMetaFromPage(0, page)}
	w.refreshBounds()
	w.trimToSegments(len(w.entries))
}

func (w *uiDetailTranscriptWindow) prependCursorPage(page clientui.TranscriptPage) uiDetailTranscriptMergeResult {
	if w == nil {
		return uiDetailTranscriptMergeResult{}
	}
	if !w.loaded || transcriptPageSessionChanged(w.sessionID, page.SessionID) {
		w.replace(page)
		return uiDetailTranscriptMergeResult{}
	}
	if w.hasSegment(page) {
		return uiDetailTranscriptMergeResult{}
	}
	pageEntries := cloneDetailChatEntries(page.Entries)
	if len(pageEntries) == 0 {
		if len(w.segments) == 0 {
			w.segments = []residentSegmentMeta{segmentMetaFromPage(0, page)}
		} else {
			top := &w.segments[0]
			top.olderCursor = cloneInt64Pointer(page.OlderCursor)
			top.hasMoreAbove = page.HasMoreAbove
		}
		w.refreshBounds()
		w.loaded = true
		return uiDetailTranscriptMergeResult{}
	}
	merged := make([]clientui.ChatEntry, 0, len(pageEntries)+len(w.entries))
	merged = append(merged, pageEntries...)
	merged = append(merged, w.entries...)
	w.entries = merged
	for i := range w.segments {
		w.segments[i].startLocal += len(pageEntries)
	}
	w.segments = append([]residentSegmentMeta{segmentMetaFromPage(0, page)}, w.segments...)
	w.refreshBounds()
	trimmedFrontEntries := w.trimToSegments(0)
	w.loaded = true
	return uiDetailTranscriptMergeResult{
		addedEntries:        len(pageEntries),
		trimmedFrontEntries: trimmedFrontEntries,
	}
}

func (w *uiDetailTranscriptWindow) appendCursorPage(page clientui.TranscriptPage) uiDetailTranscriptMergeResult {
	if w == nil {
		return uiDetailTranscriptMergeResult{}
	}
	if !w.loaded || transcriptPageSessionChanged(w.sessionID, page.SessionID) {
		w.replace(page)
		return uiDetailTranscriptMergeResult{}
	}
	if w.hasSegment(page) {
		return uiDetailTranscriptMergeResult{}
	}
	pageEntries := cloneDetailChatEntries(page.Entries)
	if len(pageEntries) == 0 {
		if len(w.segments) == 0 {
			w.segments = []residentSegmentMeta{segmentMetaFromPage(len(w.entries), page)}
		} else {
			bottom := &w.segments[len(w.segments)-1]
			bottom.newerCursor = cloneInt64Pointer(page.NewerCursor)
			bottom.hasMoreBelow = page.HasMoreBelow
		}
		w.refreshBounds()
		w.loaded = true
		return uiDetailTranscriptMergeResult{}
	}
	startLocal := len(w.entries)
	w.entries = append(w.entries, pageEntries...)
	w.segments = append(w.segments, segmentMetaFromPage(startLocal, page))
	w.refreshBounds()
	trimmedFrontEntries := w.trimToSegments(len(w.entries))
	w.loaded = true
	return uiDetailTranscriptMergeResult{
		addedEntries:        len(pageEntries),
		trimmedFrontEntries: trimmedFrontEntries,
	}
}

func (w *uiDetailTranscriptWindow) merge(page clientui.TranscriptPage) {
	if w == nil {
		return
	}
	if transcriptPageSessionChanged(w.sessionID, page.SessionID) {
		w.replace(page)
		return
	}
	if !w.loaded || len(w.segments) == 0 {
		w.replace(page)
	}
}

func (w uiDetailTranscriptWindow) hasSegment(page clientui.TranscriptPage) bool {
	if len(w.segments) == 0 || len(page.Entries) == 0 {
		return false
	}
	for idx, seg := range w.segments {
		if !segmentBoundaryEqual(seg, page) {
			continue
		}
		end := len(w.entries)
		if idx+1 < len(w.segments) {
			end = w.segments[idx+1].startLocal
		}
		if end-seg.startLocal != len(page.Entries) {
			continue
		}
		matches := true
		for entryIndex, entry := range page.Entries {
			if !clientChatEntryEqual(w.entries[seg.startLocal+entryIndex], entry) {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func segmentBoundaryEqual(seg residentSegmentMeta, page clientui.TranscriptPage) bool {
	return seg.hasMoreAbove == page.HasMoreAbove &&
		seg.hasMoreBelow == page.HasMoreBelow &&
		int64PointerEqual(seg.olderCursor, page.OlderCursor) &&
		int64PointerEqual(seg.newerCursor, page.NewerCursor)
}

func (w *uiDetailTranscriptWindow) trimToSegments(anchorLocal int) []clientui.ChatEntry {
	if w == nil || len(w.segments) <= uiDetailTranscriptMinResidentSegments {
		return nil
	}
	if anchorLocal < 0 {
		anchorLocal = 0
	}
	if anchorLocal > len(w.entries) {
		anchorLocal = len(w.entries)
	}
	anchorSeg := 0
	for i, seg := range w.segments {
		if seg.startLocal <= anchorLocal {
			anchorSeg = i
		} else {
			break
		}
	}
	trimmedFrontEntries := []clientui.ChatEntry(nil)
	// The resident window is bounded to two pages max (current + one adjacent):
	// the spec model for detail pagination. Eviction is driven by segment count
	// alone — there is no entry-count ceiling. The far segment from the anchor is
	// unloaded whenever a third resident segment appears, regardless of how many
	// entries those segments hold.
	for len(w.segments) > uiDetailTranscriptMinResidentSegments {
		last := len(w.segments) - 1
		firstDist := anchorSeg
		lastDist := last - anchorSeg
		if lastDist >= firstDist && anchorSeg != last {
			cut := w.segments[last].startLocal
			w.entries = append([]clientui.ChatEntry(nil), w.entries[:cut]...)
			w.segments = w.segments[:last]
		} else if anchorSeg != 0 {
			cut := w.segments[1].startLocal
			trimmedFrontEntries = append(trimmedFrontEntries, cloneDetailChatEntries(w.entries[:cut])...)
			w.entries = append([]clientui.ChatEntry(nil), w.entries[cut:]...)
			w.segments = w.segments[1:]
			for i := range w.segments {
				w.segments[i].startLocal -= cut
			}
			anchorSeg--
		} else {
			break
		}
	}
	w.refreshBounds()
	return trimmedFrontEntries
}

func (w uiDetailTranscriptWindow) requestedPageForDetailEntry() clientui.TranscriptPageRequest {
	return clientui.TranscriptPageRequest{}
}

func (w uiDetailTranscriptWindow) pageBefore() (clientui.TranscriptPageRequest, bool) {
	if !w.loaded || !w.hasMoreAbove || w.olderCursor == nil {
		return clientui.TranscriptPageRequest{}, false
	}
	return clientui.TranscriptPageRequest{Cursor: cloneInt64Pointer(w.olderCursor)}, true
}

func (w uiDetailTranscriptWindow) pageAfter() (clientui.TranscriptPageRequest, bool) {
	if !w.loaded || !w.hasMoreBelow || w.newerCursor == nil {
		return clientui.TranscriptPageRequest{}, false
	}
	return clientui.TranscriptPageRequest{NewerCursor: cloneInt64Pointer(w.newerCursor)}, true
}

func pageRequestEqual(a, b clientui.TranscriptPageRequest) bool {
	return int64PointerEqual(a.Cursor, b.Cursor) && int64PointerEqual(a.NewerCursor, b.NewerCursor)
}

func transcriptPageSessionChanged(currentSessionID, nextSessionID string) bool {
	trimmedCurrent := strings.TrimSpace(currentSessionID)
	trimmedNext := strings.TrimSpace(nextSessionID)
	if trimmedCurrent == "" || trimmedNext == "" {
		return false
	}
	return trimmedCurrent != trimmedNext
}

func cloneDetailChatEntries(entries []clientui.ChatEntry) []clientui.ChatEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]clientui.ChatEntry, 0, len(entries))
	for _, entry := range entries {
		copyEntry := entry
		copyEntry.ToolCall = clientuicopy.ToolCallMeta(entry.ToolCall)
		out = append(out, copyEntry)
	}
	return out
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func int64PointerEqual(left, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func clientChatEntryEqual(left, right clientui.ChatEntry) bool {
	return transcript.EntryPayloadEqual(clientEntryPayload(left), clientEntryPayload(right))
}

func clientEntryPayload(entry clientui.ChatEntry) transcript.EntryPayload {
	return transcript.EntryPayload{
		Visibility:        transcript.EntryVisibility(entry.Visibility),
		RollbackTargetID:  entry.RollbackTargetID,
		Role:              entry.Role,
		Text:              entry.Text,
		CondensedText:     entry.CondensedText,
		Phase:             string(entry.Phase),
		MessageType:       string(entry.MessageType),
		SourcePath:        entry.SourcePath,
		CompactLabel:      entry.CompactLabel,
		ToolResultSummary: entry.ToolResultSummary,
		ToolCallID:        entry.ToolCallID,
		NoticeID:          entry.NoticeID,
		ToolCall:          transcriptToolCallMeta(entry.ToolCall),
	}
}

func transcriptToolCallMeta(meta *clientui.ToolCallMeta) *transcript.ToolCallMeta {
	if meta == nil {
		return nil
	}
	out := transcript.ToolCallMeta{
		ToolName:               meta.ToolName,
		Presentation:           transcript.ToolPresentationKind(meta.Presentation),
		RenderBehavior:         transcript.ToolCallRenderBehavior(meta.RenderBehavior),
		IsShell:                meta.IsShell,
		UserInitiated:          meta.UserInitiated,
		Command:                meta.Command,
		CompactText:            meta.CompactText,
		InlineMeta:             meta.InlineMeta,
		TimeoutLabel:           meta.TimeoutLabel,
		PatchSummary:           meta.PatchSummary,
		PatchDetail:            meta.PatchDetail,
		PatchRender:            meta.PatchRender,
		Question:               meta.Question,
		Suggestions:            append([]string(nil), meta.Suggestions...),
		RecommendedOptionIndex: meta.RecommendedOptionIndex,
		OmitSuccessfulResult:   meta.OmitSuccessfulResult,
		RawOutputRequested:     meta.RawOutputRequested,
		OutputTruncated:        meta.OutputTruncated,
	}
	if meta.RenderHint != nil {
		out.RenderHint = &transcript.ToolRenderHint{
			Kind:         transcript.ToolRenderKind(meta.RenderHint.Kind),
			Path:         meta.RenderHint.Path,
			ResultOnly:   meta.RenderHint.ResultOnly,
			ShellDialect: transcript.ToolShellDialect(meta.RenderHint.ShellDialect),
		}
	}
	return &out
}
