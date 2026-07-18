package app

import (
	"strings"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/rollbacktarget"
	"core/shared/textutil"
	"core/shared/transcript"
	"core/shared/transcript/patchformat"
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
	sessionID               string
	entries                 []clientui.TranscriptCommittedRow
	loaded                  bool
	olderCursor             *int64
	hasMoreAbove            bool
	newerCursor             *int64
	hasMoreBelow            bool
	latestRollbackCandidate *rollbacktarget.CandidateLocator
	segments                []residentSegmentMeta
	lastRequest             clientui.TranscriptPageRequest
}

type uiDetailTranscriptMergeResult struct {
	addedEntries        int
	trimmedFrontEntries []clientui.TranscriptCommittedRow
}

func (w uiDetailTranscriptWindow) page() clientui.TranscriptPage {
	return clientui.TranscriptPage{
		SessionID:               w.sessionID,
		OlderCursor:             w.olderCursor,
		HasMoreAbove:            w.hasMoreAbove,
		NewerCursor:             w.newerCursor,
		HasMoreBelow:            w.hasMoreBelow,
		LatestRollbackCandidate: textutil.Pointer(w.latestRollbackCandidate),
		Entries:                 cloneDetailTranscriptRows(w.entries),
	}
}

func (w uiDetailTranscriptWindow) clone() uiDetailTranscriptWindow {
	cloned := w
	cloned.entries = cloneDetailTranscriptRows(w.entries)
	cloned.olderCursor = textutil.Pointer(w.olderCursor)
	cloned.newerCursor = textutil.Pointer(w.newerCursor)
	cloned.latestRollbackCandidate = textutil.Pointer(w.latestRollbackCandidate)
	cloned.lastRequest = cloneTranscriptPageRequest(w.lastRequest)
	if len(w.segments) > 0 {
		cloned.segments = make([]residentSegmentMeta, len(w.segments))
		for index, segment := range w.segments {
			cloned.segments[index] = segment
			cloned.segments[index].olderCursor = textutil.Pointer(segment.olderCursor)
			cloned.segments[index].newerCursor = textutil.Pointer(segment.newerCursor)
		}
	}
	return cloned
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
	top.olderCursor = textutil.Pointer(page.OlderCursor)
	top.hasMoreAbove = page.HasMoreAbove
	bottom := &w.segments[len(w.segments)-1]
	bottom.newerCursor = textutil.Pointer(page.NewerCursor)
	bottom.hasMoreBelow = page.HasMoreBelow
	w.latestRollbackCandidate = textutil.Pointer(page.LatestRollbackCandidate)
	w.refreshBounds()
}

func segmentMetaFromPage(startLocal int, page clientui.TranscriptPage) residentSegmentMeta {
	return residentSegmentMeta{
		startLocal:   startLocal,
		olderCursor:  textutil.Pointer(page.OlderCursor),
		hasMoreAbove: page.HasMoreAbove,
		newerCursor:  textutil.Pointer(page.NewerCursor),
		hasMoreBelow: page.HasMoreBelow,
	}
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
		if !tui.TranscriptCommittedRowEqual(w.entries[i], page.Entries[i]) {
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
	w.entries = cloneDetailTranscriptRows(page.Entries)
	w.loaded = true
	w.latestRollbackCandidate = textutil.Pointer(page.LatestRollbackCandidate)
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
	w.latestRollbackCandidate = textutil.Pointer(page.LatestRollbackCandidate)
	if w.hasSegment(page) {
		return uiDetailTranscriptMergeResult{}
	}
	pageEntries := cloneDetailTranscriptRows(page.Entries)
	if len(pageEntries) == 0 {
		if len(w.segments) == 0 {
			w.segments = []residentSegmentMeta{segmentMetaFromPage(0, page)}
		} else {
			top := &w.segments[0]
			top.olderCursor = textutil.Pointer(page.OlderCursor)
			top.hasMoreAbove = page.HasMoreAbove
		}
		w.refreshBounds()
		w.loaded = true
		return uiDetailTranscriptMergeResult{}
	}
	merged := make([]clientui.TranscriptCommittedRow, 0, len(pageEntries)+len(w.entries))
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
	w.latestRollbackCandidate = textutil.Pointer(page.LatestRollbackCandidate)
	if w.hasSegment(page) {
		return uiDetailTranscriptMergeResult{}
	}
	pageEntries := cloneDetailTranscriptRows(page.Entries)
	if len(pageEntries) == 0 {
		if len(w.segments) == 0 {
			w.segments = []residentSegmentMeta{segmentMetaFromPage(len(w.entries), page)}
		} else {
			bottom := &w.segments[len(w.segments)-1]
			bottom.newerCursor = textutil.Pointer(page.NewerCursor)
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
			if !tui.TranscriptCommittedRowEqual(w.entries[seg.startLocal+entryIndex], entry) {
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
		textutil.EqualOptional(seg.olderCursor, page.OlderCursor) &&
		textutil.EqualOptional(seg.newerCursor, page.NewerCursor)
}

func (w *uiDetailTranscriptWindow) trimToSegments(anchorLocal int) []clientui.TranscriptCommittedRow {
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
	trimmedFrontEntries := []clientui.TranscriptCommittedRow(nil)
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
			w.entries = append([]clientui.TranscriptCommittedRow(nil), w.entries[:cut]...)
			w.segments = w.segments[:last]
		} else if anchorSeg != 0 {
			cut := w.segments[1].startLocal
			trimmedFrontEntries = append(trimmedFrontEntries, cloneDetailTranscriptRows(w.entries[:cut])...)
			w.entries = append([]clientui.TranscriptCommittedRow(nil), w.entries[cut:]...)
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
	return clientui.TranscriptPageRequest{Cursor: textutil.Pointer(w.olderCursor)}, true
}

func (w uiDetailTranscriptWindow) pageAfter() (clientui.TranscriptPageRequest, bool) {
	if !w.loaded || !w.hasMoreBelow || w.newerCursor == nil {
		return clientui.TranscriptPageRequest{}, false
	}
	return clientui.TranscriptPageRequest{NewerCursor: textutil.Pointer(w.newerCursor)}, true
}

func pageRequestEqual(a, b clientui.TranscriptPageRequest) bool {
	return textutil.EqualOptional(a.Cursor, b.Cursor) && textutil.EqualOptional(a.NewerCursor, b.NewerCursor)
}

func cloneTranscriptPageRequest(request clientui.TranscriptPageRequest) clientui.TranscriptPageRequest {
	return clientui.TranscriptPageRequest{
		Cursor:      textutil.Pointer(request.Cursor),
		NewerCursor: textutil.Pointer(request.NewerCursor),
	}
}

func transcriptPageSessionChanged(currentSessionID, nextSessionID string) bool {
	trimmedCurrent := strings.TrimSpace(currentSessionID)
	trimmedNext := strings.TrimSpace(nextSessionID)
	if trimmedCurrent == "" || trimmedNext == "" {
		return false
	}
	return trimmedCurrent != trimmedNext
}

func cloneDetailTranscriptRows(entries []clientui.TranscriptCommittedRow) []clientui.TranscriptCommittedRow {
	if len(entries) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptCommittedRow, 0, len(entries))
	for _, row := range entries {
		copyRow := row
		copyRow.User = textutil.Pointer(row.User)
		if copyRow.User != nil {
			copyRow.User.CondensedText = textutil.Pointer(row.User.CondensedText)
			copyRow.User.RollbackTargetID = textutil.Pointer(row.User.RollbackTargetID)
		}
		copyRow.Assistant = textutil.Pointer(row.Assistant)
		if copyRow.Assistant != nil {
			copyRow.Assistant.StreamID = textutil.Pointer(row.Assistant.StreamID)
			copyRow.Assistant.CondensedText = textutil.Pointer(row.Assistant.CondensedText)
		}
		copyRow.Tool = textutil.Pointer(row.Tool)
		if copyRow.Tool != nil {
			copyRow.Tool.ResultSummary = textutil.Pointer(row.Tool.ResultSummary)
			copyRow.Tool.CondensedText = textutil.Pointer(row.Tool.CondensedText)
			if row.Tool.Presentation != nil {
				presentation := transcript.NormalizeToolCallMeta(*row.Tool.Presentation)
				presentation.PatchRender = patchformat.Clone(presentation.PatchRender)
				copyRow.Tool.Presentation = &presentation
			}
		}
		copyRow.Notice = cloneDetailTranscriptNotice(row.Notice)
		out = append(out, copyRow)
	}
	return out
}

func cloneDetailTranscriptNotice(notice *clientui.TranscriptNoticeRow) *clientui.TranscriptNoticeRow {
	if notice == nil {
		return nil
	}
	copyNotice := *notice
	copyNotice.StepID = textutil.Pointer(notice.StepID)
	copyNotice.MessageType = textutil.Pointer(notice.MessageType)
	copyNotice.LegacyText = textutil.Pointer(notice.LegacyText)
	copyNotice.NoticeID = textutil.Pointer(notice.NoticeID)
	copyNotice.SourcePath = textutil.Pointer(notice.SourcePath)
	if notice.Worktree != nil {
		worktree := *notice.Worktree
		worktree.Branch = textutil.Pointer(notice.Worktree.Branch)
		copyNotice.Worktree = &worktree
	}
	copyNotice.CacheWarning = textutil.Pointer(notice.CacheWarning)
	copyNotice.Diagnostic = textutil.Pointer(notice.Diagnostic)
	if notice.Background != nil {
		background := *notice.Background
		background.ExitCode = textutil.Pointer(notice.Background.ExitCode)
		copyNotice.Background = &background
	}
	copyNotice.CondensedText = textutil.Pointer(notice.CondensedText)
	copyNotice.CompactLabel = textutil.Pointer(notice.CompactLabel)
	return &copyNotice
}
