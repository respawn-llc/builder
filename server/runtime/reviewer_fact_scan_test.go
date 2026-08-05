package runtime

import (
	"testing"

	"core/server/session"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestPersistedTranscriptScanReconstructsTypedReviewerFactsInBoundedWindows(t *testing.T) {
	feedback := session.ReviewerFeedbackRecord{
		ID:          runtimeids.NewReviewerFeedbackID(),
		Suggestions: []string{"  **first**  ", "second\nline"},
		Visibility:  session.EntryVisibilityOngoingCollapsed,
	}
	reviewerError := session.ReviewerErrorRecord{
		ID:     runtimeids.NewReviewerErrorID(),
		Detail: "raw failure detail",
	}
	feedbackEvent, err := session.NewEventRecord(1, stringPointer("step-feedback"), feedback)
	if err != nil {
		t.Fatalf("create feedback event: %v", err)
	}
	errorEvent, err := session.NewEventRecord(2, stringPointer("step-error"), reviewerError)
	if err != nil {
		t.Fatalf("create error event: %v", err)
	}

	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{Offset: 1, Limit: 1, TrackRecentTail: true, TailLimit: 1})
	for _, event := range []session.EventRecord{feedbackEvent, errorEvent} {
		if err := scan.ApplyPersistedEvent(event); err != nil {
			t.Fatalf("apply typed Reviewer event: %v", err)
		}
	}

	if got := scan.TotalEntries(); got != 2 {
		t.Fatalf("total entries = %d, want 2", got)
	}
	page := scan.CollectedPageSnapshot()
	if len(page.Entries) != 1 || page.Entries[0].ReviewerError == nil {
		t.Fatalf("bounded page = %+v, want only Reviewer error", page.Entries)
	}
	if page.Entries[0].ReviewerError.ID != reviewerError.ID ||
		page.Entries[0].ReviewerError.Detail != reviewerError.Detail ||
		page.Entries[0].StepID != "step-error" ||
		page.Entries[0].Visibility != transcript.EntryVisibilityOngoing {
		t.Fatalf("projected Reviewer error = %+v", page.Entries[0])
	}
	tail := scan.RecentTailSnapshot()
	if len(tail.Snapshot.Entries) != 1 || tail.Snapshot.Entries[0].ReviewerError == nil {
		t.Fatalf("bounded tail = %+v, want only Reviewer error", tail.Snapshot.Entries)
	}
	facts := TranscriptCommittedRowFactsFromSnapshot(page)
	if len(facts) != 1 || facts[0].Kind != TranscriptCommittedRowFactReviewerError ||
		facts[0].ReviewerError == nil || facts[0].ReviewerError.ID != reviewerError.ID {
		t.Fatalf("snapshot Reviewer facts = %+v", facts)
	}
}

func TestPersistedTranscriptScanKeepsHistoricalReviewerLocalEntriesGeneric(t *testing.T) {
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	events := []session.EventRecord{
		mustPersistedScanEvent(t, "local_entry", storedLocalEntry{
			Visibility: transcript.EntryVisibilityOngoing,
			Role:       "reviewer_suggestions",
			Text:       "legacy Markdown\n\n- item",
		}),
		mustPersistedScanEvent(t, "local_entry", storedLocalEntry{
			Visibility: transcript.EntryVisibilityOngoing,
			Role:       "reviewer_error",
			Text:       "legacy error detail",
		}),
	}
	for _, event := range events {
		if err := scan.ApplyPersistedEvent(event); err != nil {
			t.Fatalf("apply historical Reviewer local entry: %v", err)
		}
	}
	entries := scan.CollectedPageSnapshot().Entries
	if len(entries) != 2 || entries[0].Role != "reviewer_suggestions" || entries[0].Text != "legacy Markdown\n\n- item" ||
		entries[1].Role != "reviewer_error" || entries[1].Text != "legacy error detail" {
		t.Fatalf("historical Reviewer local entry = %+v", entries)
	}
}

func stringPointer(value string) *string {
	return &value
}
