package runtime

import (
	"core/server/session"
	"core/shared/transcript"
)

func reviewerFeedbackChatEntryFromSessionRecord(
	record session.ReviewerFeedbackRecord,
	stepID string,
) ChatEntry {
	return ChatEntry{
		StepID:     stepID,
		Visibility: runtimeEntryVisibilityFromSession(record.Visibility),
		ReviewerFeedback: &ReviewerFeedbackChatEntry{
			ID:          record.ID,
			Suggestions: append([]string(nil), record.Suggestions...),
		},
	}
}

func reviewerErrorChatEntryFromSessionRecord(
	record session.ReviewerErrorRecord,
	stepID string,
) ChatEntry {
	return ChatEntry{
		StepID:     stepID,
		Visibility: transcript.EntryVisibilityOngoing,
		ReviewerError: &ReviewerErrorChatEntry{
			ID:     record.ID,
			Detail: record.Detail,
		},
	}
}
