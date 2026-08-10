package clientui

import (
	"fmt"
	"strings"

	"core/shared/runtimeids"
)

type QueuedUserMessageStatus string

const (
	QueuedUserMessageAccepted  QueuedUserMessageStatus = "accepted"
	QueuedUserMessageSubmitted QueuedUserMessageStatus = "submitted"
	QueuedUserMessageFailed    QueuedUserMessageStatus = "failed"
	QueuedUserMessageDiscarded QueuedUserMessageStatus = "discarded"
)

type QueuedUserMessageFailureReason string

const (
	QueuedUserMessageFailureClosing                    QueuedUserMessageFailureReason = "closing"
	QueuedUserMessageFailureTerminalWorkflowCompletion QueuedUserMessageFailureReason = "terminal_workflow_completion"
	QueuedUserMessageFailureRuntimeUnavailable         QueuedUserMessageFailureReason = "runtime_unavailable"
	QueuedUserMessageFailureStopped                    QueuedUserMessageFailureReason = "stopped"
)

type TranscriptUserMessageFlushed struct {
	StepID   runtimeids.StepID
	Messages []QueuedUserMessageIdentity
}

type QueuedUserMessageIdentity struct {
	ClientRequestID runtimeids.RuntimeClientRequestID
	QueueItemID     runtimeids.QueueItemID
}

type TranscriptQueuedMessageState struct {
	ClientRequestID runtimeids.RuntimeClientRequestID
	QueueItemID     runtimeids.QueueItemID
	Status          QueuedUserMessageStatus
	FailureReason   *QueuedUserMessageFailureReason
	Text            *string
}

func (f TranscriptUserMessageFlushed) Validate() error {
	if f.StepID.IsZero() {
		return fmt.Errorf("user-message flush step id is required")
	}
	if len(f.Messages) == 0 {
		return fmt.Errorf("user-message flush requires queued-message identities")
	}
	seen := make(map[runtimeids.RuntimeClientRequestID]struct{}, len(f.Messages))
	for index, message := range f.Messages {
		if message.ClientRequestID.IsZero() {
			return fmt.Errorf("user-message flush identity %d requires client request id", index)
		}
		if message.QueueItemID.IsZero() {
			return fmt.Errorf("user-message flush identity %d requires queue item id", index)
		}
		if _, exists := seen[message.ClientRequestID]; exists {
			return fmt.Errorf("user-message flush repeats client request id %q", message.ClientRequestID.String())
		}
		seen[message.ClientRequestID] = struct{}{}
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
	case QueuedUserMessageAccepted:
		if s.FailureReason != nil {
			return fmt.Errorf("accepted queued-message state cannot carry failure reason")
		}
		return validateRequiredOptionalText("accepted queued-message state", s.Text)
	case QueuedUserMessageSubmitted, QueuedUserMessageDiscarded:
		if s.FailureReason != nil {
			return fmt.Errorf("%s queued-message state cannot carry failure reason", s.Status)
		}
		if s.Text != nil {
			return fmt.Errorf("%s queued-message state cannot carry restore text", s.Status)
		}
		return nil
	case QueuedUserMessageFailed:
		if s.FailureReason == nil {
			return fmt.Errorf("failed queued-message state requires failure reason")
		}
		switch *s.FailureReason {
		case QueuedUserMessageFailureClosing,
			QueuedUserMessageFailureTerminalWorkflowCompletion,
			QueuedUserMessageFailureRuntimeUnavailable,
			QueuedUserMessageFailureStopped:
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
