package runtimeview

import (
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/shared/clientui"
	"core/shared/transcript"
)

func TestCommittedRowLocatorIsStableAcrossPageHydrationAndLiveProjection(t *testing.T) {
	const (
		sessionID = "12345678-1234-4234-8234-123456789012"
		stepID    = "22222222-2222-4222-8222-222222222222"
	)
	provenance := &runtime.TranscriptCommittedRowProvenance{EventSequence: 17}
	snapshot := runtime.ChatSnapshot{Entries: []runtime.ChatEntry{{
		StepID:              runtimeStepIDPointer(stepID),
		Visibility:          transcript.EntryVisibilityOngoing,
		Role:                "user",
		Text:                "hello",
		CommittedProvenance: provenance,
	}}}

	page, err := TranscriptPageFromSegment(
		sessionID,
		"session",
		clientui.ConversationFreshness(0),
		runtime.TranscriptSegmentPage{Snapshot: snapshot},
	)
	if err != nil {
		t.Fatalf("project page: %v", err)
	}
	hydration := TranscriptHydrationFromSnapshot(runtime.TranscriptHydrationSnapshot{
		CommittedRows: runtime.TranscriptCommittedRowFactsFromSnapshot(snapshot),
	})
	live := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:                runtime.EventUserMessageFlushed,
		StepID:              runtimeStepIDPointer(stepID),
		UserMessage:         "hello",
		CommittedProvenance: provenance,
	})

	if len(page.Entries) != 1 || len(hydration.CommittedRows) != 1 || len(live) != 1 {
		t.Fatalf("projected rows: page=%d hydration=%d live=%d, want one each", len(page.Entries), len(hydration.CommittedRows), len(live))
	}
	liveRow := transcriptPayload[clientui.TranscriptCommittedRow](t, live[0])
	if page.Entries[0].Locator != hydration.CommittedRows[0].Locator ||
		page.Entries[0].Locator != liveRow.Locator {
		t.Fatalf(
			"locators disagree: page=%+v hydration=%+v live=%+v",
			page.Entries[0].Locator,
			hydration.CommittedRows[0].Locator,
			liveRow.Locator,
		)
	}
	if err := page.Entries[0].Locator.Validate(); err != nil {
		t.Fatalf("projected locator is invalid: %v", err)
	}
}

func TestCommittedRowLocatorNumbersProjectedRowsAfterFiltering(t *testing.T) {
	const stepID = "22222222-2222-4222-8222-222222222222"
	provenance := &runtime.TranscriptCommittedRowProvenance{EventSequence: 23}
	facts := runtime.TranscriptCommittedRowFactsFromSnapshot(runtime.ChatSnapshot{
		Entries: []runtime.ChatEntry{
			{
				StepID:              runtimeStepIDPointer(stepID),
				Visibility:          transcript.EntryVisibilityHidden,
				Role:                "assistant",
				Text:                "internal",
				CommittedProvenance: provenance,
			},
			{
				StepID:              runtimeStepIDPointer(stepID),
				Visibility:          transcript.EntryVisibilityOngoing,
				Role:                "user",
				Text:                "visible one",
				CommittedProvenance: provenance,
			},
			{
				StepID:              runtimeStepIDPointer(stepID),
				Visibility:          transcript.EntryVisibilityDetail,
				Role:                "assistant",
				Text:                "visible two",
				Phase:               llm.MessagePhaseCommentary,
				CommittedProvenance: provenance,
			},
		},
	})
	if len(facts) != 2 {
		t.Fatalf("projected facts = %d, want two visible rows", len(facts))
	}
	if got, want := facts[0].Locator.RowOrdinal, int64(1); got != want {
		t.Fatalf("first projected ordinal = %d, want %d", got, want)
	}
	if got, want := facts[1].Locator.RowOrdinal, int64(2); got != want {
		t.Fatalf("second projected ordinal = %d, want %d", got, want)
	}
	for index, fact := range facts {
		if fact.Locator.EventSequence != 23 {
			t.Fatalf("fact %d event sequence = %d, want 23", index, fact.Locator.EventSequence)
		}
	}
}

func TestCheckedTranscriptProjectionReturnsMalformedLocatorErrors(t *testing.T) {
	const sessionID = "12345678-1234-4234-8234-123456789012"
	const stepID = "22222222-2222-4222-8222-222222222222"
	malformed := &runtime.TranscriptCommittedRowProvenance{}
	snapshot := runtime.TranscriptHydrationSnapshot{
		CommittedRows: []runtime.TranscriptCommittedRowFact{{
			StepID:     runtimeStepIDPointer(stepID),
			Kind:       runtime.TranscriptCommittedRowFactUser,
			Locator:    transcript.CommittedRowLocator{},
			Provenance: malformed,
			User:       &runtime.TranscriptUserRowFact{Text: "malformed"},
		}},
	}
	if _, err := TranscriptHydrationFromSnapshotChecked(snapshot); err == nil {
		t.Fatal("checked hydration projection accepted malformed locator")
	}

	_, err := TranscriptPageFromSegment(
		sessionID,
		"session",
		clientui.ConversationFreshness(0),
		runtime.TranscriptSegmentPage{Snapshot: runtime.ChatSnapshot{Entries: []runtime.ChatEntry{{
			StepID:              runtimeStepIDPointer(stepID),
			Visibility:          transcript.EntryVisibilityOngoing,
			Role:                "user",
			Text:                "malformed",
			CommittedProvenance: malformed,
		}}}},
	)
	if err == nil {
		t.Fatal("checked page projection accepted malformed locator")
	}

	_, err = TranscriptMessagesFromRuntimeEventChecked(runtime.Event{
		Kind:                runtime.EventUserMessageFlushed,
		StepID:              runtimeStepIDPointer(stepID),
		UserMessage:         "malformed",
		CommittedProvenance: malformed,
	})
	if err == nil {
		t.Fatal("checked live projection accepted malformed locator")
	}
}
