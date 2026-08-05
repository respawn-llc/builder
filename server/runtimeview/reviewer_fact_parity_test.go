package runtimeview

import (
	"reflect"
	"testing"

	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestReviewerFactsMatchAcrossLiveHydrationAndPageProjection(t *testing.T) {
	stepID := "11111111-1111-4111-8111-111111111111"
	feedbackID := runtimeids.NewReviewerFeedbackID()
	errorID := runtimeids.NewReviewerErrorID()
	entries := []runtime.ChatEntry{
		{
			StepID: stepID, Visibility: transcript.EntryVisibilityOngoingCollapsed,
			ReviewerFeedback: &runtime.ReviewerFeedbackChatEntry{ID: feedbackID, Suggestions: []string{"  **one**  ", "two\nline"}},
		},
		{
			StepID: stepID, Visibility: transcript.EntryVisibilityOngoing,
			ReviewerError: &runtime.ReviewerErrorChatEntry{ID: errorID, Detail: " raw failure "},
		},
	}
	snapshot := runtime.ChatSnapshot{Entries: entries}
	liveFacts := runtime.TranscriptCommittedRowFactsFromSnapshot(snapshot)
	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{CommittedRows: liveFacts})
	page := TranscriptPageFromSegment(
		"58e121b5-30f7-4d0f-a1fa-fb3e6695e39c",
		"name",
		clientui.ConversationFreshnessEstablished,
		runtime.TranscriptSegmentPage{Snapshot: snapshot},
	)
	if !reflect.DeepEqual(hydration.CommittedRows, page.Entries) {
		t.Fatalf("hydration/page Reviewer rows differ: hydration=%+v page=%+v", hydration.CommittedRows, page.Entries)
	}
	for index := range entries {
		event := runtime.Event{
			Kind: runtime.EventLocalEntryAdded, StepID: stepID,
			LocalEntry: &entries[index], LocalEntryProjected: true,
		}
		liveRows := transcriptRowsFromFacts(runtime.TranscriptCommittedRowFactsFromEvent(event))
		if len(liveRows) != 1 || !reflect.DeepEqual(liveRows[0], hydration.CommittedRows[index]) {
			t.Fatalf("live Reviewer row %d differs: live=%+v hydration=%+v", index, liveRows, hydration.CommittedRows[index])
		}
	}
	if len(liveFacts) != 2 || liveFacts[0].ReviewerFeedback == nil || liveFacts[1].ReviewerError == nil {
		t.Fatalf("typed Reviewer facts = %+v", liveFacts)
	}
}
