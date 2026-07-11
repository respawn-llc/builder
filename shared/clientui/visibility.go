package clientui

import "core/shared/transcript"

type EntryVisibility = transcript.EntryVisibility

const (
	EntryVisibilityAuto             = transcript.EntryVisibilityAuto
	EntryVisibilityOngoing          = transcript.EntryVisibilityOngoing
	EntryVisibilityOngoingCollapsed = transcript.EntryVisibilityOngoingCollapsed
	EntryVisibilityDetail           = transcript.EntryVisibilityDetail
	EntryVisibilityHidden           = transcript.EntryVisibilityHidden
)
