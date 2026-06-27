package app

import (
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestRuntimeEventCanDeferCommittedConversationFence(t *testing.T) {
	update := clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         7,
	}
	if !runtimeEventCanDeferCommittedConversationFence(update) {
		t.Fatal("expected empty committed conversation update to be deferrable fence")
	}
	update.TranscriptEntries = []clientui.ChatEntry{{Role: "assistant", Text: "done"}}
	if runtimeEventCanDeferCommittedConversationFence(update) {
		t.Fatal("expected conversation update with entries to be non-deferrable")
	}
	update.TranscriptEntries = nil
	update.RecoveryCause = clientui.TranscriptRecoveryCauseStreamGap
	if runtimeEventCanDeferCommittedConversationFence(update) {
		t.Fatal("expected recovery conversation update to be non-deferrable")
	}
}

func TestWaitRuntimeEventDoesNotDeferCommittedGoalFeedback(t *testing.T) {
	ch := make(chan clientui.Event, 2)
	ch <- clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         7,
		CommittedEntryStart:        0,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role:          string(transcript.EntryRoleGoalFeedback),
			Text:          "goal detail",
			CondensedText: `Goal set: "ship feature"`,
			Visibility:    clientui.EntryVisibilityAll,
		}},
	}
	ch <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "later"}

	raw := waitRuntimeEvent(ch)()
	msg, ok := raw.(runtimeEventBatchMsg)
	if !ok {
		t.Fatalf("expected runtimeEventBatchMsg, got %T", raw)
	}
	if len(msg.events) != 1 {
		t.Fatalf("first batch len = %d, want only goal feedback event", len(msg.events))
	}
	if msg.events[0].Kind != clientui.EventConversationUpdated || len(msg.events[0].TranscriptEntries) != 1 {
		t.Fatalf("first event = %+v, want committed goal feedback", msg.events[0])
	}
	if msg.carry != nil {
		t.Fatalf("did not expect later event carried behind goal feedback, got %+v", *msg.carry)
	}
}

func TestSplitRuntimeBatchAtAssistantDeltaKeepsBatchTogether(t *testing.T) {
	events := []clientui.Event{
		{Kind: clientui.EventAssistantDelta, AssistantDelta: "first"},
		{Kind: clientui.EventAssistantDelta, AssistantDelta: "second"},
		{Kind: clientui.EventAssistantMessage},
	}

	head, tail, split := splitRuntimeBatchAtAssistantDelta(events)
	if split {
		t.Fatal("assistant deltas should stay in their runtime batch")
	}
	if len(tail) != 0 {
		t.Fatalf("tail = %+v, want empty", tail)
	}
	if len(head) != len(events) || head[0].AssistantDelta != "first" || head[1].AssistantDelta != "second" || head[2].Kind != clientui.EventAssistantMessage {
		t.Fatalf("head = %+v, want original batch order", head)
	}
}

func TestRuntimeEventCoversDeferredCommittedConversationUpdate(t *testing.T) {
	update := clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         7,
		CommittedEntryCount:        2,
	}
	next := clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     " step-1 ",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         7,
		CommittedEntryCount:        2,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "done"}},
	}
	if !runtimeEventCoversDeferredCommittedConversationUpdate(update, next) {
		t.Fatal("expected matching committed entry event to cover deferred update")
	}
	next.CommittedEntryCount = 3
	if runtimeEventCoversDeferredCommittedConversationUpdate(update, next) {
		t.Fatal("expected different committed count not to cover deferred update")
	}
	next.CommittedEntryCount = 2
	next.StepID = "other"
	if runtimeEventCoversDeferredCommittedConversationUpdate(update, next) {
		t.Fatal("expected different step not to cover deferred update")
	}
}

func TestRuntimeEventShouldBatchAfterCommittedConversationFence(t *testing.T) {
	update := clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         7,
		CommittedEntryCount:        2,
	}
	next := clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         8,
		CommittedEntryCount:        3,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "done"}},
	}
	if !runtimeEventShouldBatchAfterCommittedConversationFence(update, next) {
		t.Fatal("expected same-step committed entry advance to batch after deferred update")
	}
	next.CommittedEntryCount = 2
	if runtimeEventShouldBatchAfterCommittedConversationFence(update, next) {
		t.Fatal("expected covering event not to be batched with deferred update")
	}
	next.CommittedEntryCount = 3
	next.TranscriptRevision = 6
	if runtimeEventShouldBatchAfterCommittedConversationFence(update, next) {
		t.Fatal("expected older revision not to batch after deferred update")
	}
}
