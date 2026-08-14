package runtime

import (
	"core/server/session"
	"core/shared/transcript"
)

func reviewerFeedbackChatEntryFromSessionRecord(
	record session.ReviewerFeedbackRecord,
	stepID string,
	provenance *TranscriptCommittedRowProvenance,
) ChatEntry {
	return ChatEntry{
		StepID:              exactStepIDPointer(stepID),
		Visibility:          runtimeEntryVisibilityFromSession(record.Visibility),
		CommittedProvenance: cloneTranscriptCommittedRowProvenance(provenance),
		ReviewerFeedback: &ReviewerFeedbackChatEntry{
			ID:          record.ID,
			Suggestions: append([]string(nil), record.Suggestions...),
		},
	}
}

func reviewerErrorChatEntryFromSessionRecord(
	record session.ReviewerErrorRecord,
	stepID string,
	provenance *TranscriptCommittedRowProvenance,
) ChatEntry {
	return ChatEntry{
		StepID:              exactStepIDPointer(stepID),
		Visibility:          transcript.EntryVisibilityOngoing,
		CommittedProvenance: cloneTranscriptCommittedRowProvenance(provenance),
		ReviewerError: &ReviewerErrorChatEntry{
			ID:     record.ID,
			Detail: record.Detail,
		},
	}
}
