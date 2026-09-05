package runtimeview

import (
	"reflect"
	"testing"

	"core/server/runtime"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestReviewerFactsMatchAcrossLiveHydrationAndPageProjection(t *testing.T) {
	stepID := "11111111-1111-4111-8111-111111111111"
	feedbackID := runtimeids.NewReviewerFeedbackID()
	errorID := runtimeids.NewReviewerErrorID()
	entries := []runtime.ChatEntry{
		{
			StepID: runtimeStepIDPointer(stepID), Visibility: transcript.EntryVisibilityOngoingCollapsed,
			CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 1},
			ReviewerFeedback:    &runtime.ReviewerFeedbackChatEntry{ID: feedbackID, Suggestions: []string{"  **one**  ", "two\nline"}},
		},
		{
			StepID: runtimeStepIDPointer(stepID), Visibility: transcript.EntryVisibilityOngoing,
			CommittedProvenance: &runtime.TranscriptCommittedRowProvenance{EventSequence: 2},
			ReviewerError:       &runtime.ReviewerErrorChatEntry{ID: errorID, Detail: " raw failure "},
		},
	}
	snapshot := runtime.ChatSnapshot{Entries: entries}
	liveFacts := runtime.TranscriptCommittedRowFactsFromSnapshot(snapshot)
	hydration := mustTranscriptHydration(t, runtime.TranscriptHydrationSnapshot{CommittedRows: liveFacts})
	page, err := TranscriptPageFromSegment(
		"58e121b5-30f7-4d0f-a1fa-fb3e6695e39c",
		"name",
		clientui.ConversationFreshnessEstablished,
		runtime.TranscriptSegmentPage{Snapshot: snapshot},
	)
	if err != nil {
		t.Fatalf("project page: %v", err)
	}
	if !reflect.DeepEqual(hydration.TailSegment.Entries, page.Entries) {
		t.Fatalf("hydration/page Reviewer rows differ: hydration=%+v page=%+v", hydration.TailSegment.Entries, page.Entries)
	}
	for index := range hydration.TailSegment.Entries {
		if err := hydration.TailSegment.Entries[index].Validate(); err != nil {
			t.Fatalf("hydrated Reviewer row %d failed validation: %v", index, err)
		}
		if err := page.Entries[index].Validate(); err != nil {
			t.Fatalf("paged Reviewer row %d failed validation: %v", index, err)
		}
	}
	for index := range entries {
		event := runtime.Event{
			Kind: runtime.EventLocalEntryAdded, StepID: runtimeStepIDPointer(stepID),
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
		if len(liveRows) != 1 || !reflect.DeepEqual(liveRows[0], hydration.TailSegment.Entries[index]) {
			t.Fatalf("live Reviewer row %d differs: live=%+v hydration=%+v", index, liveRows, hydration.TailSegment.Entries[index])
		}
	}
	if len(liveFacts) != 2 || liveFacts[0].ReviewerFeedback == nil || liveFacts[1].ReviewerError == nil {
		t.Fatalf("typed Reviewer facts = %+v", liveFacts)
	}
}

func TestQuestionAnswerFactsMatchAcrossLiveHydrationAndPageProjection(t *testing.T) {
	const (
		sessionID = "58e121b5-30f7-4d0f-a1fa-fb3e6695e39c"
		stepID    = "11111111-1111-4111-8111-111111111111"
		callID    = "22222222-2222-4222-8222-222222222222"
	)
	selected := 2
	freeform := "keep the split"
	provenance := &runtime.TranscriptCommittedRowProvenance{EventSequence: 7}
	presentation := transcript.NormalizeToolCallMeta(transcript.ToolCallMeta{
		ToolName:               string(toolspec.ToolAskQuestion),
		Presentation:           transcript.ToolPresentationAskQuestion,
		RenderBehavior:         transcript.ToolCallRenderBehaviorAskQuestion,
		Question:               "Which option?",
		Suggestions:            []string{"first", "second"},
		RecommendedOptionIndex: 2,
	})
	answer := &tools.AskQuestionAnswer{
		SelectedOptionNumber: &selected,
		Freeform:             &freeform,
	}
	entry := runtime.ChatEntry{
		StepID:              runtimeStepIDPointer(stepID),
		Visibility:          transcript.EntryVisibilityOngoingCollapsed,
		Role:                "tool_result_ok",
		Text:                "User selected option 2. User also said: keep the split",
		ToolCallID:          callID,
		ToolCall:            &presentation,
		QuestionAnswer:      answer,
		CommittedProvenance: provenance,
	}
	snapshot := runtime.ChatSnapshot{Entries: []runtime.ChatEntry{entry}}
	facts := runtime.TranscriptCommittedRowFactsFromSnapshot(snapshot)
	hydration := mustTranscriptHydration(t, runtime.TranscriptHydrationSnapshot{CommittedRows: facts})
	page, err := TranscriptPageFromSegment(
		sessionID,
		"name",
		clientui.ConversationFreshnessEstablished,
		runtime.TranscriptSegmentPage{Snapshot: snapshot},
	)
	if err != nil {
		t.Fatalf("project page: %v", err)
	}
	liveMessages := TranscriptMessagesFromRuntimeEvent(runtime.Event{
		Kind:   runtime.EventToolCallCompleted,
		StepID: runtimeStepIDPointer(stepID),
		ToolResult: &tools.Result{
			CallID:         callID,
			Name:           toolspec.ToolAskQuestion,
			Output:         []byte(`{"summary":"User selected option 2. User also said: keep the split"}`),
			Presentation:   &presentation,
			QuestionAnswer: answer,
		},
		CommittedProvenance: provenance,
	})
	if len(facts) != 1 || len(hydration.TailSegment.Entries) != 1 || len(page.Entries) != 1 || len(liveMessages) != 1 {
		t.Fatalf(
			"projected Question rows: facts=%d hydration=%d page=%d live=%d, want one each",
			len(facts),
			len(hydration.TailSegment.Entries),
			len(page.Entries),
			len(liveMessages),
		)
	}
	liveRow := transcriptPayload[clientui.TranscriptCommittedRow](t, liveMessages[0])
	if !reflect.DeepEqual(hydration.TailSegment.Entries[0], page.Entries[0]) ||
		!reflect.DeepEqual(hydration.TailSegment.Entries[0], liveRow) {
		t.Fatalf(
			"Question rows differ: hydration=%+v page=%+v live=%+v",
			hydration.TailSegment.Entries[0],
			page.Entries[0],
			liveRow,
		)
	}
	got := hydration.TailSegment.Entries[0]
	if got.Tool == nil || got.Tool.QuestionAnswer == nil ||
		got.Tool.QuestionAnswer.SelectedOptionNumber == nil ||
		*got.Tool.QuestionAnswer.SelectedOptionNumber != selected ||
		got.Tool.QuestionAnswer.Freeform == nil ||
		*got.Tool.QuestionAnswer.Freeform != freeform {
		t.Fatalf("Question answer facts = %+v, want selected=%d freeform=%q", got.Tool, selected, freeform)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("shared v1-compatible Question row failed validation: %v", err)
	}
	legacy := got
	legacyTool := *got.Tool
	legacyTool.QuestionAnswer = nil
	legacy.Tool = &legacyTool
	if err := legacy.Validate(); err != nil {
		t.Fatalf("shared contract rejected event-log v1 Question row without answer facts: %v", err)
	}

	selected = 1
	freeform = "mutated"
	if got.Tool.QuestionAnswer.SelectedOptionNumber == nil ||
		*got.Tool.QuestionAnswer.SelectedOptionNumber != 2 ||
		got.Tool.QuestionAnswer.Freeform == nil ||
		*got.Tool.QuestionAnswer.Freeform != "keep the split" {
		t.Fatalf("Question row aliases source answer facts: %+v", got.Tool.QuestionAnswer)
	}
}
