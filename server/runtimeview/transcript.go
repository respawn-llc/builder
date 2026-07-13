package runtimeview

import (
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/valuecopy"
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
		SessionID:               sessionID,
		SessionName:             sessionName,
		ConversationFreshness:   freshness,
		OlderCursor:             transcriptCursor(page.HasMoreAbove, page.OlderCursor),
		HasMoreAbove:            page.HasMoreAbove,
		NewerCursor:             transcriptCursor(page.HasMoreBelow, page.NewerCursor),
		HasMoreBelow:            page.HasMoreBelow,
		LatestRollbackCandidate: valuecopy.Pointer(page.LatestRollbackCandidate),
		Entries:                 transcriptRowsFromFacts(runtime.TranscriptCommittedRowFactsFromSnapshot(page.Snapshot)),
	}
}

func transcriptCursor(hasMore bool, cursor int64) *int64 {
	if !hasMore {
		return nil
	}
	return &cursor
}
