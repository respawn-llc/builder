package runtimeview

import (
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/textutil"
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
	freshness, err := engine.ConversationFreshness()
	if err != nil {
		return clientui.TranscriptPage{}, err
	}
	return TranscriptPageFromSegment(
		engine.SessionID(),
		engine.SessionName(),
		ConversationFreshnessFromSession(freshness),
		segment,
	)
}

func TranscriptPageFromSegment(sessionID, sessionName string, freshness clientui.ConversationFreshness, page runtime.TranscriptSegmentPage) (clientui.TranscriptPage, error) {
	segment, err := TranscriptTailSegmentFromSegment(page)
	if err != nil {
		return clientui.TranscriptPage{}, err
	}
	return clientui.TranscriptPage{
		SessionID:               sessionID,
		SessionName:             sessionName,
		ConversationFreshness:   freshness,
		OlderCursor:             segment.OlderCursor,
		HasMoreAbove:            segment.HasMoreAbove,
		NewerCursor:             transcriptCursor(page.HasMoreBelow, page.NewerCursor),
		HasMoreBelow:            page.HasMoreBelow,
		LatestRollbackCandidate: textutil.Pointer(page.LatestRollbackCandidate),
		Entries:                 segment.Entries,
	}, nil
}

func TranscriptTailSegmentFromSegment(page runtime.TranscriptSegmentPage) (clientui.TranscriptTailSegment, error) {
	return transcriptTailSegmentFromFactsChecked(
		runtime.TranscriptCommittedRowFactsFromSnapshot(page.Snapshot),
		transcriptCursor(page.HasMoreAbove, page.OlderCursor),
		page.HasMoreAbove,
	)
}

func transcriptTailSegmentFromFactsChecked(
	facts []runtime.TranscriptCommittedRowFact,
	olderCursor *int64,
	hasMoreAbove bool,
) (clientui.TranscriptTailSegment, error) {
	entries, err := transcriptRowsFromFactsChecked(facts)
	if err != nil {
		return clientui.TranscriptTailSegment{}, err
	}
	segment := clientui.TranscriptTailSegment{
		OlderCursor:  olderCursor,
		HasMoreAbove: hasMoreAbove,
		Entries:      entries,
	}
	if err := segment.Validate(); err != nil {
		return clientui.TranscriptTailSegment{}, err
	}
	return segment, nil
}

func transcriptCursor(hasMore bool, cursor int64) *int64 {
	if !hasMore {
		return nil
	}
	return &cursor
}
