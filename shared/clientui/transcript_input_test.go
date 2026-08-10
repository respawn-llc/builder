package clientui

import (
	"testing"
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

	reason := QueuedUserMessageFailureStopped
	failed := accepted
	failed.Status = QueuedUserMessageFailed
	failed.FailureReason = &reason
	if err := failed.Validate(); err != nil {
		t.Fatalf("validate failed queued-message state: %v", err)
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
