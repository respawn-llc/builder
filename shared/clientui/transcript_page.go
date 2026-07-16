package clientui

import "core/shared/rollbacktarget"

type TranscriptPageRequest struct {
	Cursor      *int64
	NewerCursor *int64
}

type TranscriptPage struct {
	SessionID               string
	SessionName             string
	ConversationFreshness   ConversationFreshness
	OlderCursor             *int64
	HasMoreAbove            bool
	NewerCursor             *int64
	HasMoreBelow            bool
	LatestRollbackCandidate *rollbacktarget.CandidateLocator
	Entries                 []TranscriptCommittedRow
}
