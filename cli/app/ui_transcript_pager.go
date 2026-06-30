package app

import (
	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/transcript"
	"strings"
)

const (
	uiDetailTranscriptPageSize            = 250
	uiDetailTranscriptMaxEntries          = 1000
	uiDetailTranscriptEdgeLineMargin      = 24
	uiDetailTranscriptMinResidentSegments = 2
)

type residentSegmentMeta struct {
	startLocal   int
	olderCursor  int64
	hasMoreAbove bool
	newerCursor  int64
	hasMoreBelow bool
}

type uiDetailTranscriptWindow struct {
	sessionID         string
	offset            int
	totalEntries      int
	entries           []tui.TranscriptEntry
	ongoing           string
	ongoingError      string
	loaded            bool
	olderCursor       int64
	hasMoreAbove      bool
	newerCursor       int64
	hasMoreBelow      bool
	segments          []residentSegmentMeta
	lastRequest       clientui.TranscriptPageRequest
	knownPageStart    int
	knownPageStartSet bool
}

func (w uiDetailTranscriptWindow) page() clientui.TranscriptPage {
	entries := make([]clientui.ChatEntry, 0, len(w.entries))
	for _, entry := range w.entries {
		entries = append(entries, clientui.ChatEntry{
			Visibility:        entry.Visibility,
			RollbackTargetID:  entry.RollbackTargetID,
			Role:              string(entry.Role),
			Text:              entry.Text,
			CondensedText:     entry.CondensedText,
			Phase:             string(entry.Phase),
			MessageType:       string(entry.MessageType),
			SourcePath:        entry.SourcePath,
			CompactLabel:      entry.CompactLabel,
			ToolResultSummary: entry.ToolResultSummary,
			ToolCallID:        entry.ToolCallID,
			ToolCall:          transcriptToolCallMetaClient(entry.ToolCall),
		})
	}
	return clientui.TranscriptPage{
		SessionID:      w.sessionID,
		OlderCursor:    w.olderCursor,
		HasMoreAbove:   w.hasMoreAbove,
		NewerCursor:    w.newerCursor,
		HasMoreBelow:   w.hasMoreBelow,
		Entries:        entries,
		Streaming:      w.ongoing,
		StreamingError: w.ongoingError,
	}
}

func (w *uiDetailTranscriptWindow) setKnownBounds(offset, totalEntries int) {
	if w == nil {
		return
	}
	if offset < 0 {
		offset = 0
	}
	w.knownPageStart = offset
	w.knownPageStartSet = true
	if totalEntries > w.totalEntries {
		w.totalEntries = totalEntries
	}
	if !w.loaded {
		w.offset = offset
	}
}

func (w uiDetailTranscriptWindow) inferredPageStart(page clientui.TranscriptPage) int {
	if w.knownPageStartSet {
		return w.knownPageStart
	}
	if !page.HasMoreAbove {
		return 0
	}
	totalEntries := max(w.totalEntries, len(page.Entries))
	start := totalEntries - len(page.Entries)
	if start < 0 {
		return 0
	}
	return start
}

func (w uiDetailTranscriptWindow) inferredPageTotal(page clientui.TranscriptPage, start int) int {
	if !page.HasMoreAbove && !page.HasMoreBelow {
		return start + len(page.Entries)
	}
	return max(w.totalEntries, start+len(page.Entries))
}

func (w *uiDetailTranscriptWindow) clearKnownPageBounds() {
	if w == nil {
		return
	}
	w.knownPageStart = 0
	w.knownPageStartSet = false
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
	top.olderCursor = page.OlderCursor
	top.hasMoreAbove = page.HasMoreAbove
	bottom := &w.segments[len(w.segments)-1]
	bottom.newerCursor = page.NewerCursor
	bottom.hasMoreBelow = page.HasMoreBelow
	w.refreshBounds()
}

func segmentMetaFromPage(startLocal int, page clientui.TranscriptPage) residentSegmentMeta {
	return residentSegmentMeta{
		startLocal:   startLocal,
		olderCursor:  page.OlderCursor,
		hasMoreAbove: page.HasMoreAbove,
		newerCursor:  page.NewerCursor,
		hasMoreBelow: page.HasMoreBelow,
	}
}

func (w *uiDetailTranscriptWindow) reset() {
	if w == nil {
		return
	}
	*w = uiDetailTranscriptWindow{}
}

func (w *uiDetailTranscriptWindow) syncTail(page clientui.TranscriptPage) {
	if w == nil {
		return
	}
	defer w.clearKnownPageBounds()
	if w.loaded && transcriptPageSessionChanged(w.sessionID, page.SessionID) {
		w.replace(page)
		return
	}
	if !w.loaded {
		w.replace(page)
		return
	}
	end := w.offset + len(w.entries)
	pageStart := w.inferredPageStart(page)
	pageEnd := pageStart + len(page.Entries)
	w.totalEntries = w.inferredPageTotal(page, pageStart)
	w.ongoing = page.Streaming
	w.ongoingError = page.StreamingError
	if pageStart >= end || pageEnd <= w.offset {
		if pageEnd >= w.totalEntries {
			w.replace(page)
		}
		return
	}
	w.merge(page)
}

func (w *uiDetailTranscriptWindow) apply(page clientui.TranscriptPage) {
	if w == nil {
		return
	}
	defer w.clearKnownPageBounds()
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
	pageStart := w.inferredPageStart(page)
	totalEntries := w.inferredPageTotal(page, pageStart)
	if w.offset != pageStart || w.totalEntries != totalEntries {
		return false
	}
	if w.ongoing != page.Streaming || w.ongoingError != page.StreamingError {
		return false
	}
	if len(w.entries) != len(page.Entries) {
		return false
	}
	for i := range page.Entries {
		if !transcript.EntryPayloadEqual(transcriptPayloadFromTUIEntry(w.entries[i]), transcriptPayloadFromClientEntry(page.Entries[i])) {
			return false
		}
	}
	return true
}

func (w *uiDetailTranscriptWindow) replace(page clientui.TranscriptPage) {
	if w == nil {
		return
	}
	defer w.clearKnownPageBounds()
	w.sessionID = strings.TrimSpace(page.SessionID)
	w.offset = w.inferredPageStart(page)
	w.totalEntries = w.inferredPageTotal(page, w.offset)
	w.entries = transcriptEntriesFromPage(page)
	w.ongoing = page.Streaming
	w.ongoingError = page.StreamingError
	w.loaded = true
	w.segments = []residentSegmentMeta{segmentMetaFromPage(0, page)}
	w.refreshBounds()
	w.trimToSegments(w.offset)
}

func (w *uiDetailTranscriptWindow) prependCursorPage(page clientui.TranscriptPage) {
	if w == nil {
		return
	}
	if !w.loaded || transcriptPageSessionChanged(w.sessionID, page.SessionID) {
		w.replace(page)
		return
	}
	pageEntries := transcriptEntriesFromPage(page)
	if len(pageEntries) == 0 {
		if len(w.segments) == 0 {
			w.segments = []residentSegmentMeta{segmentMetaFromPage(0, page)}
		} else {
			top := &w.segments[0]
			top.olderCursor = page.OlderCursor
			top.hasMoreAbove = page.HasMoreAbove
		}
		w.refreshBounds()
		w.loaded = true
		return
	}
	merged := make([]tui.TranscriptEntry, 0, len(pageEntries)+len(w.entries))
	merged = append(merged, pageEntries...)
	merged = append(merged, w.entries...)
	w.offset -= len(pageEntries)
	if w.offset < 0 {
		w.offset = 0
	}
	w.entries = merged
	w.totalEntries = max(w.totalEntries, w.offset+len(w.entries))
	for i := range w.segments {
		w.segments[i].startLocal += len(pageEntries)
	}
	w.segments = append([]residentSegmentMeta{segmentMetaFromPage(0, page)}, w.segments...)
	w.refreshBounds()
	w.trimToSegments(w.offset)
	w.loaded = true
}

func (w *uiDetailTranscriptWindow) appendCursorPage(page clientui.TranscriptPage) {
	if w == nil {
		return
	}
	if !w.loaded || transcriptPageSessionChanged(w.sessionID, page.SessionID) {
		w.replace(page)
		return
	}
	pageEntries := transcriptEntriesFromPage(page)
	if len(pageEntries) == 0 {
		if len(w.segments) == 0 {
			w.segments = []residentSegmentMeta{segmentMetaFromPage(len(w.entries), page)}
		} else {
			bottom := &w.segments[len(w.segments)-1]
			bottom.newerCursor = page.NewerCursor
			bottom.hasMoreBelow = page.HasMoreBelow
		}
		w.refreshBounds()
		w.ongoing = page.Streaming
		w.ongoingError = page.StreamingError
		w.loaded = true
		return
	}
	startLocal := len(w.entries)
	w.entries = append(w.entries, pageEntries...)
	w.totalEntries = max(w.totalEntries, w.offset+len(w.entries))
	w.segments = append(w.segments, segmentMetaFromPage(startLocal, page))
	w.refreshBounds()
	w.trimToSegments(w.offset + len(w.entries))
	w.ongoing = page.Streaming
	w.ongoingError = page.StreamingError
	w.loaded = true
}

func (w *uiDetailTranscriptWindow) merge(page clientui.TranscriptPage) {
	if w == nil {
		return
	}
	defer w.clearKnownPageBounds()
	if transcriptPageSessionChanged(w.sessionID, page.SessionID) {
		w.replace(page)
		return
	}
	if len(page.Entries) == 0 {
		w.totalEntries = max(w.totalEntries, len(w.entries))
		w.ongoing = page.Streaming
		w.ongoingError = page.StreamingError
		return
	}
	pageEntries := transcriptEntriesFromPage(page)
	currentStart := w.offset
	currentEnd := w.offset + len(w.entries)
	pageStart := w.inferredPageStart(page)
	pageEnd := pageStart + len(pageEntries)
	if pageEnd < currentStart || pageStart > currentEnd {
		w.replace(page)
		return
	}
	mergedStart := min(currentStart, pageStart)
	mergedEnd := max(currentEnd, pageEnd)
	merged := make([]tui.TranscriptEntry, mergedEnd-mergedStart)
	copy(merged[currentStart-mergedStart:], w.entries)
	copy(merged[pageStart-mergedStart:], pageEntries)
	frontGrowth := currentStart - mergedStart
	w.offset = mergedStart
	w.entries = merged
	w.totalEntries = max(w.inferredPageTotal(page, pageStart), mergedEnd)
	w.ongoing = page.Streaming
	w.ongoingError = page.StreamingError
	w.loaded = true
	if frontGrowth > 0 {
		for i := range w.segments {
			w.segments[i].startLocal += frontGrowth
		}
	}
	if len(w.segments) > 0 {
		if pageStart <= currentStart {
			top := &w.segments[0]
			top.olderCursor = page.OlderCursor
			top.hasMoreAbove = page.HasMoreAbove
		}
		bottom := &w.segments[len(w.segments)-1]
		bottom.newerCursor = page.NewerCursor
		bottom.hasMoreBelow = page.HasMoreBelow
	} else {
		w.segments = []residentSegmentMeta{segmentMetaFromPage(0, page)}
	}
	w.refreshBounds()
	w.trimToSegments(pageStart)
}

func (w *uiDetailTranscriptWindow) trimToSegments(anchorOffset int) {
	if w == nil || len(w.segments) <= uiDetailTranscriptMinResidentSegments {
		return
	}
	anchorLocal := anchorOffset - w.offset
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
	for len(w.segments) > uiDetailTranscriptMinResidentSegments && len(w.entries) > uiDetailTranscriptMaxEntries {
		last := len(w.segments) - 1
		firstDist := anchorSeg
		lastDist := last - anchorSeg
		if lastDist >= firstDist && anchorSeg != last {
			cut := w.segments[last].startLocal
			w.entries = append([]tui.TranscriptEntry(nil), w.entries[:cut]...)
			w.segments = w.segments[:last]
		} else if anchorSeg != 0 {
			cut := w.segments[1].startLocal
			w.entries = append([]tui.TranscriptEntry(nil), w.entries[cut:]...)
			w.offset += cut
			w.segments = w.segments[1:]
			for i := range w.segments {
				w.segments[i].startLocal -= cut
			}
			anchorSeg--
		} else {
			break
		}
	}
	w.totalEntries = max(w.totalEntries, w.offset+len(w.entries))
	w.refreshBounds()
}

func (w uiDetailTranscriptWindow) requestedPageForDetailEntry() clientui.TranscriptPageRequest {
	return clientui.TranscriptPageRequest{}
}

func (w uiDetailTranscriptWindow) pageBefore() (clientui.TranscriptPageRequest, bool) {
	if !w.loaded || !w.hasMoreAbove || w.olderCursor <= 0 {
		return clientui.TranscriptPageRequest{}, false
	}
	return clientui.TranscriptPageRequest{Cursor: w.olderCursor}, true
}

func (w uiDetailTranscriptWindow) pageAfter() (clientui.TranscriptPageRequest, bool) {
	if !w.loaded || !w.hasMoreBelow || w.newerCursor <= 0 {
		return clientui.TranscriptPageRequest{}, false
	}
	return clientui.TranscriptPageRequest{NewerCursor: w.newerCursor}, true
}

func pageRequestEqual(a, b clientui.TranscriptPageRequest) bool {
	return a.Cursor == b.Cursor && a.NewerCursor == b.NewerCursor
}

func transcriptPageSessionChanged(currentSessionID, nextSessionID string) bool {
	trimmedCurrent := strings.TrimSpace(currentSessionID)
	trimmedNext := strings.TrimSpace(nextSessionID)
	if trimmedCurrent == "" || trimmedNext == "" {
		return false
	}
	return trimmedCurrent != trimmedNext
}
