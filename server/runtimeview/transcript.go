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
	entries, err := transcriptRowsFromFactsChecked(runtime.TranscriptCommittedRowFactsFromSnapshot(page.Snapshot))
	if err != nil {
		return clientui.TranscriptPage{}, err
	}
	return clientui.TranscriptPage{
		SessionID:               sessionID,
		SessionName:             sessionName,
		ConversationFreshness:   freshness,
		OlderCursor:             transcriptCursor(page.HasMoreAbove, page.OlderCursor),
		HasMoreAbove:            page.HasMoreAbove,
		NewerCursor:             transcriptCursor(page.HasMoreBelow, page.NewerCursor),
		HasMoreBelow:            page.HasMoreBelow,
		LatestRollbackCandidate: textutil.Pointer(page.LatestRollbackCandidate),
		Entries:                 entries,
	}, nil
}

func transcriptCursor(hasMore bool, cursor int64) *int64 {
	if !hasMore {
		return nil
	}
	return &cursor
}
