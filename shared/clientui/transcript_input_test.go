package clientui

import (
	"testing"

	"core/shared/runtimeids"
)

func TestTranscriptUserFlushCarriesStepIdentity(t *testing.T) {
	flushed := TranscriptUserMessageFlushed{
		StepID: transcriptTestStepID(t),
	}
	if err := flushed.Validate(); err != nil {
		t.Fatalf("validate user-message flush: %v", err)
	}
}

func TestTranscriptQueuedMessageStateUsesTypedTerminalFields(t *testing.T) {
	text := "queued input"
	accepted := TranscriptQueuedMessageState{
		QueueItemID: transcriptTestQueueItemID(t),
		Status:      QueuedUserMessageAccepted,
		Text:        &text,
	}
	if err := accepted.Validate(); err != nil {
		t.Fatalf("validate accepted queued-message state: %v", err)
	}

	reason := QueuedUserMessageFailureRuntimeUnavailable
	failed := accepted
	failed.Status = QueuedUserMessageFailed
	failed.FailureReason = &reason
	if err := failed.Validate(); err != nil {
		t.Fatalf("validate failed queued-message state: %v", err)
	}
}

func TestTranscriptHumanInputInterruptedPreservesServerOrderAndVerbatimText(t *testing.T) {
	first := transcriptTestQueueItemID(t)
	second, err := runtimeids.ParseQueueItemID("66666666-6666-4666-8666-666666666666")
	if err != nil {
		t.Fatalf("ParseQueueItemID: %v", err)
	}
	event := TranscriptHumanInputInterrupted{Items: []TranscriptInterruptedHumanInputItem{
		{QueueItemID: first, Text: "  first\nline\t "},
		{QueueItemID: second, Text: "\tsecond  "},
	}}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if event.Items[0].QueueItemID != first || event.Items[1].QueueItemID != second {
		t.Fatalf("items reordered: %+v", event.Items)
	}
	if event.Items[0].Text != "  first\nline\t " || event.Items[1].Text != "\tsecond  " {
		t.Fatalf("text changed: %+v", event.Items)
	}
}

func TestTranscriptInputFactsRejectMissingIdentityAndStaleText(t *testing.T) {
	if err := (TranscriptUserMessageFlushed{}).Validate(); err == nil {
		t.Fatal("accepted user-message flush without step identity")
	}

	submittedWithText := TranscriptQueuedMessageState{
		QueueItemID: transcriptTestQueueItemID(t),
		Status:      QueuedUserMessageSubmitted,
		Text:        func() *string { text := "stale"; return &text }(),
	}
	if err := submittedWithText.Validate(); err == nil {
		t.Fatal("accepted submitted queued-message state with stale text")
	}
}
