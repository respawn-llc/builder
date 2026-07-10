package app

import (
	"strings"

	"core/shared/clientui"
	"core/shared/transcript"
	"core/shared/valuecopy"
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
	entries      []clientui.TranscriptCommittedRow
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
	trimmedFrontEntries []clientui.TranscriptCommittedRow
}

func (w uiDetailTranscriptWindow) page() clientui.TranscriptPage {
	return clientui.TranscriptPage{
		SessionID:    w.sessionID,
		OlderCursor:  w.olderCursor,
		HasMoreAbove: w.hasMoreAbove,
		NewerCursor:  w.newerCursor,
		HasMoreBelow: w.hasMoreBelow,
		Entries:      cloneDetailTranscriptRows(w.entries),
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
	top.olderCursor = valuecopy.Pointer(page.OlderCursor)
	top.hasMoreAbove = page.HasMoreAbove
	bottom := &w.segments[len(w.segments)-1]
	bottom.newerCursor = valuecopy.Pointer(page.NewerCursor)
	bottom.hasMoreBelow = page.HasMoreBelow
	w.refreshBounds()
}

func segmentMetaFromPage(startLocal int, page clientui.TranscriptPage) residentSegmentMeta {
	return residentSegmentMeta{
		startLocal:   startLocal,
		olderCursor:  valuecopy.Pointer(page.OlderCursor),
		hasMoreAbove: page.HasMoreAbove,
		newerCursor:  valuecopy.Pointer(page.NewerCursor),
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
		if !clientTranscriptRowEqual(w.entries[i], page.Entries[i]) {
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
	pageEntries := cloneDetailTranscriptRows(page.Entries)
	if len(pageEntries) == 0 {
		if len(w.segments) == 0 {
			w.segments = []residentSegmentMeta{segmentMetaFromPage(0, page)}
		} else {
			top := &w.segments[0]
			top.olderCursor = valuecopy.Pointer(page.OlderCursor)
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
	if w.hasSegment(page) {
		return uiDetailTranscriptMergeResult{}
	}
	pageEntries := cloneDetailTranscriptRows(page.Entries)
	if len(pageEntries) == 0 {
		if len(w.segments) == 0 {
			w.segments = []residentSegmentMeta{segmentMetaFromPage(len(w.entries), page)}
		} else {
			bottom := &w.segments[len(w.segments)-1]
			bottom.newerCursor = valuecopy.Pointer(page.NewerCursor)
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
			if !clientTranscriptRowEqual(w.entries[seg.startLocal+entryIndex], entry) {
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
	return clientui.TranscriptPageRequest{Cursor: valuecopy.Pointer(w.olderCursor)}, true
}

func (w uiDetailTranscriptWindow) pageAfter() (clientui.TranscriptPageRequest, bool) {
	if !w.loaded || !w.hasMoreBelow || w.newerCursor == nil {
		return clientui.TranscriptPageRequest{}, false
	}
	return clientui.TranscriptPageRequest{NewerCursor: valuecopy.Pointer(w.newerCursor)}, true
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

func cloneDetailTranscriptRows(entries []clientui.TranscriptCommittedRow) []clientui.TranscriptCommittedRow {
	if len(entries) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptCommittedRow, 0, len(entries))
	for _, row := range entries {
		copyRow := row
		copyRow.User = valuecopy.Pointer(row.User)
		copyRow.Assistant = valuecopy.Pointer(row.Assistant)
		if copyRow.Assistant != nil {
			copyRow.Assistant.StreamID = valuecopy.Pointer(row.Assistant.StreamID)
		}
		copyRow.Tool = valuecopy.Pointer(row.Tool)
		if copyRow.Tool != nil {
			copyRow.Tool.ToolPresentation = valuecopy.ToolCallMeta(row.Tool.ToolPresentation)
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
	copyNotice.Diagnostic = valuecopy.Pointer(notice.Diagnostic)
	copyNotice.Data.LegacyText = valuecopy.Pointer(notice.Data.LegacyText)
	copyNotice.Data.NoticeID = valuecopy.Pointer(notice.Data.NoticeID)
	copyNotice.Data.CacheWarning = valuecopy.Pointer(notice.Data.CacheWarning)
	copyNotice.Data.RuntimeDiagnostic = valuecopy.Pointer(notice.Data.RuntimeDiagnostic)
	copyNotice.Data.BackgroundExitCode = valuecopy.Pointer(notice.Data.BackgroundExitCode)
	return &copyNotice
}

func int64PointerEqual(left, right *int64) bool {
	return ptrEqual(left, right)
}

func clientTranscriptRowEqual(left, right clientui.TranscriptCommittedRow) bool {
	if left.Visibility != right.Visibility ||
		left.Integrity != right.Integrity ||
		left.Kind != right.Kind {
		return false
	}
	return clientTranscriptUserRowEqual(left.User, right.User) &&
		clientTranscriptAssistantRowEqual(left.Assistant, right.Assistant) &&
		clientTranscriptToolRowEqual(left.Tool, right.Tool) &&
		clientTranscriptNoticeRowEqual(left.Notice, right.Notice)
}

func clientTranscriptUserRowEqual(left, right *clientui.TranscriptUserRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func clientTranscriptAssistantRowEqual(left, right *clientui.TranscriptAssistantRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Text == right.Text &&
		left.CondensedText == right.CondensedText &&
		left.Phase == right.Phase &&
		ptrEqual(left.StreamID, right.StreamID)
}

func clientTranscriptToolRowEqual(left, right *clientui.TranscriptToolRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.ToolCallID == right.ToolCallID &&
		left.ToolName == right.ToolName &&
		left.Text == right.Text &&
		left.IsError == right.IsError &&
		left.ResultSummary == right.ResultSummary &&
		left.CondensedText == right.CondensedText &&
		transcript.ToolCallMetaEqual(
			transcriptToolCallMeta(left.ToolPresentation),
			transcriptToolCallMeta(right.ToolPresentation),
		)
}

func clientTranscriptNoticeRowEqual(left, right *clientui.TranscriptNoticeRow) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Reason == right.Reason &&
		left.Severity == right.Severity &&
		ptrEqual(left.Data.LegacyText, right.Data.LegacyText) &&
		ptrEqual(left.Data.NoticeID, right.Data.NoticeID) &&
		ptrEqual(left.Data.CacheWarning, right.Data.CacheWarning) &&
		ptrEqual(left.Data.RuntimeDiagnostic, right.Data.RuntimeDiagnostic) &&
		left.Data.MessageType == right.Data.MessageType &&
		left.Data.SourcePath == right.Data.SourcePath &&
		left.Data.CondensedText == right.Data.CondensedText &&
		left.Data.CompactLabel == right.Data.CompactLabel &&
		ptrEqual(left.Data.BackgroundExitCode, right.Data.BackgroundExitCode) &&
		ptrEqual(left.Diagnostic, right.Diagnostic)
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
