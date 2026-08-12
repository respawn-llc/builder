package runtimeview

import (
	"reflect"
	"testing"

	"core/server/runtime"
	"core/server/session"
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
			CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 1},
			ReviewerFeedback:    &runtime.ReviewerFeedbackChatEntry{ID: feedbackID, Suggestions: []string{"  **one**  ", "two\nline"}},
		},
		{
			StepID: stepID, Visibility: transcript.EntryVisibilityOngoing,
			CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 2},
			ReviewerError:       &runtime.ReviewerErrorChatEntry{ID: errorID, Detail: " raw failure "},
		},
	}
	snapshot := runtime.ChatSnapshot{Entries: entries}
	liveFacts := runtime.TranscriptCommittedRowFactsFromSnapshot(snapshot)
	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{
		CommittedRows:    liveFacts,
		GoalAvailability: session.GoalAvailable,
	})
	page, err := TranscriptPageFromSegment(
		"58e121b5-30f7-4d0f-a1fa-fb3e6695e39c",
		"name",
		clientui.ConversationFreshnessEstablished,
		runtime.TranscriptSegmentPage{Snapshot: snapshot},
	)
	if err != nil {
		t.Fatalf("project page: %v", err)
	}
	if !reflect.DeepEqual(hydration.CommittedRows, page.Entries) {
		t.Fatalf("hydration/page Reviewer rows differ: hydration=%+v page=%+v", hydration.CommittedRows, page.Entries)
	}
	for index := range hydration.CommittedRows {
		if err := hydration.CommittedRows[index].Validate(); err != nil {
			t.Fatalf("hydrated Reviewer row %d failed validation: %v", index, err)
		}
		if err := page.Entries[index].Validate(); err != nil {
			t.Fatalf("paged Reviewer row %d failed validation: %v", index, err)
		}
	}
	for index := range entries {
		event := runtime.Event{
			Kind: runtime.EventLocalEntryAdded, StepID: stepID,
			LocalEntry: &entries[index], LocalEntryProjected: true,
			CommittedProvenance: entries[index].CommittedProvenance,
		}
		liveMessages := TranscriptMessagesFromRuntimeEvent(event)
		if len(liveMessages) != 1 {
			t.Fatalf("live Reviewer subscription messages %d, want one", len(liveMessages))
		}
		if err := liveMessages[0].Validate(); err != nil {
			t.Fatalf("live Reviewer subscription event %d failed validation: %v", index, err)
		}
		liveRows := []clientui.TranscriptCommittedRow{
			transcriptPayload[clientui.TranscriptCommittedRow](t, liveMessages[0]),
		}
		if len(liveRows) != 1 || !reflect.DeepEqual(liveRows[0], hydration.CommittedRows[index]) {
			t.Fatalf("live Reviewer row %d differs: live=%+v hydration=%+v", index, liveRows, hydration.CommittedRows[index])
		}
	}
	if len(liveFacts) != 2 || liveFacts[0].ReviewerFeedback == nil || liveFacts[1].ReviewerError == nil {
		t.Fatalf("typed Reviewer facts = %+v", liveFacts)
	}
}
