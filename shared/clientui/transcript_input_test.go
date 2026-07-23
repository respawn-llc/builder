package clientui

import (
	"testing"
)

func TestTranscriptUserFlushCarriesTypedOperationIdentity(t *testing.T) {
	queueItemID := transcriptTestQueueItemID(t)
	flushed := TranscriptUserMessageFlushed{
		StepID: transcriptTestStepID(t),
		Operations: []RuntimeOperationRef{{
			Kind:            RuntimeOperationKindQueuedMessage,
			ClientRequestID: transcriptTestClientRequestID(t),
			QueueItemID:     &queueItemID,
		}},
	}
	if err := flushed.Validate(); err != nil {
		t.Fatalf("validate user-message flush: %v", err)
	}
}

func TestTranscriptQueuedMessageStateUsesTypedTerminalFields(t *testing.T) {
	text := "queued input"
	accepted := TranscriptQueuedMessageState{
		ClientRequestID: transcriptTestClientRequestID(t),
		QueueItemID:     transcriptTestQueueItemID(t),
		Status:          QueuedUserMessageAccepted,
		Text:            &text,
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

func TestTranscriptInputFactsRejectMissingOrDuplicateOperationIdentity(t *testing.T) {
	queueItemID := transcriptTestQueueItemID(t)
	operation := RuntimeOperationRef{
		Kind:            RuntimeOperationKindQueuedMessage,
		ClientRequestID: transcriptTestClientRequestID(t),
		QueueItemID:     &queueItemID,
	}
	tests := []TranscriptUserMessageFlushed{
		{StepID: transcriptTestStepID(t)},
		{
			StepID: transcriptTestStepID(t),
			Operations: []RuntimeOperationRef{
				operation,
				operation,
			},
		},
		{
			StepID: transcriptTestStepID(t),
			Operations: []RuntimeOperationRef{{
				Kind:            RuntimeOperationKindQueuedMessage,
				ClientRequestID: transcriptTestClientRequestID(t),
			}},
		},
	}
	for _, flushed := range tests {
		if err := flushed.Validate(); err == nil {
			t.Fatalf("accepted invalid user-message flush: %+v", flushed)
		}
	}

	submittedWithText := TranscriptQueuedMessageState{
		ClientRequestID: transcriptTestClientRequestID(t),
		QueueItemID:     transcriptTestQueueItemID(t),
		Status:          QueuedUserMessageSubmitted,
		Text:            func() *string { text := "stale"; return &text }(),
	}
	if err := submittedWithText.Validate(); err == nil {
		t.Fatal("accepted submitted queued-message state with stale text")
	}
}
