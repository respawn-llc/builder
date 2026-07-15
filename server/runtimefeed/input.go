package runtimefeed

import (
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

type TranscriptUserMessageFlushed struct {
	StepID     runtimeids.StepID
	Operations []RuntimeOperationRef
}

type TranscriptQueuedMessageState struct {
	ClientRequestID runtimeids.RuntimeClientRequestID
	QueueItemID     runtimeids.QueueItemID
	Status          clientui.QueuedUserMessageStatus
	FailureReason   *clientui.QueuedUserMessageFailureReason
	Text            *string
}

func (f TranscriptUserMessageFlushed) Validate() error {
	if f.StepID.IsZero() {
		return fmt.Errorf("user-message flush step id is required")
	}
	if len(f.Operations) == 0 {
		return fmt.Errorf("user-message flush requires operation identities")
	}
	seen := make(map[runtimeids.RuntimeClientRequestID]struct{}, len(f.Operations))
	for index, operation := range f.Operations {
		if err := operation.Validate(); err != nil {
			return fmt.Errorf("validate user-message flush operation %d: %w", index, err)
		}
		if operation.Kind == clientui.RuntimeOperationKindQueuedMessage && operation.QueueItemID == nil {
			return fmt.Errorf("user-message flush queued operation %d requires queue item id", index)
		}
		if _, exists := seen[operation.ClientRequestID]; exists {
			return fmt.Errorf("user-message flush repeats client request id %q", operation.ClientRequestID.String())
		}
		seen[operation.ClientRequestID] = struct{}{}
	}
	return nil
}

func (s TranscriptQueuedMessageState) Validate() error {
	if s.ClientRequestID.IsZero() {
		return fmt.Errorf("queued-message state requires client request id")
	}
	if s.QueueItemID.IsZero() {
		return fmt.Errorf("queued-message state requires queue item id")
	}
	switch s.Status {
	case clientui.QueuedUserMessageAccepted:
		if s.FailureReason != nil {
			return fmt.Errorf("accepted queued-message state cannot carry failure reason")
		}
		return validateRequiredOptionalText("accepted queued-message state", s.Text)
	case clientui.QueuedUserMessageSubmitted, clientui.QueuedUserMessageDiscarded:
		if s.FailureReason != nil {
			return fmt.Errorf("%s queued-message state cannot carry failure reason", s.Status)
		}
		if s.Text != nil {
			return fmt.Errorf("%s queued-message state cannot carry restore text", s.Status)
		}
		return nil
	case clientui.QueuedUserMessageFailed:
		if s.FailureReason == nil {
			return fmt.Errorf("failed queued-message state requires failure reason")
		}
		switch *s.FailureReason {
		case clientui.QueuedUserMessageFailureClosing,
			clientui.QueuedUserMessageFailureTerminalWorkflowCompletion,
			clientui.QueuedUserMessageFailureRuntimeUnavailable,
			clientui.QueuedUserMessageFailureStopped:
		default:
			return fmt.Errorf("unknown queued-message failure reason %q", *s.FailureReason)
		}
		return validateRequiredOptionalText("failed queued-message state", s.Text)
	default:
		return fmt.Errorf("unknown queued-message state %q", s.Status)
	}
}

func validateRequiredOptionalText(owner string, text *string) error {
	if text == nil || strings.TrimSpace(*text) == "" {
		return fmt.Errorf("%s requires non-empty text", owner)
	}
	return nil
}
