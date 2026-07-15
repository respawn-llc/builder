package runtimefeed

import (
	"testing"

	"core/shared/clientui"
)

func TestTranscriptUserFlushCarriesTypedOperationIdentity(t *testing.T) {
	queueItemID := runtimefeedTestQueueItemID(t)
	flushed := TranscriptUserMessageFlushed{
		StepID: runtimefeedTestStepID(t),
		Operations: []RuntimeOperationRef{{
			Kind:            clientui.RuntimeOperationKindQueuedMessage,
			ClientRequestID: runtimefeedTestClientRequestID(t),
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
		ClientRequestID: runtimefeedTestClientRequestID(t),
		QueueItemID:     runtimefeedTestQueueItemID(t),
		Status:          clientui.QueuedUserMessageAccepted,
		Text:            &text,
	}
	if err := accepted.Validate(); err != nil {
		t.Fatalf("validate accepted queued-message state: %v", err)
	}

	reason := clientui.QueuedUserMessageFailureStopped
	failed := accepted
	failed.Status = clientui.QueuedUserMessageFailed
	failed.FailureReason = &reason
	if err := failed.Validate(); err != nil {
		t.Fatalf("validate failed queued-message state: %v", err)
	}
}

func TestTranscriptInputFactsRejectMissingOrDuplicateOperationIdentity(t *testing.T) {
	queueItemID := runtimefeedTestQueueItemID(t)
	operation := RuntimeOperationRef{
		Kind:            clientui.RuntimeOperationKindQueuedMessage,
		ClientRequestID: runtimefeedTestClientRequestID(t),
		QueueItemID:     &queueItemID,
	}
	tests := []TranscriptUserMessageFlushed{
		{StepID: runtimefeedTestStepID(t)},
		{
			StepID: runtimefeedTestStepID(t),
			Operations: []RuntimeOperationRef{
				operation,
				operation,
			},
		},
		{
			StepID: runtimefeedTestStepID(t),
			Operations: []RuntimeOperationRef{{
				Kind:            clientui.RuntimeOperationKindQueuedMessage,
				ClientRequestID: runtimefeedTestClientRequestID(t),
			}},
		},
	}
	for _, flushed := range tests {
		if err := flushed.Validate(); err == nil {
			t.Fatalf("accepted invalid user-message flush: %+v", flushed)
		}
	}

	submittedWithText := TranscriptQueuedMessageState{
		ClientRequestID: runtimefeedTestClientRequestID(t),
		QueueItemID:     runtimefeedTestQueueItemID(t),
		Status:          clientui.QueuedUserMessageSubmitted,
		Text:            func() *string { text := "stale"; return &text }(),
	}
	if err := submittedWithText.Validate(); err == nil {
		t.Fatal("accepted submitted queued-message state with stale text")
	}
}
