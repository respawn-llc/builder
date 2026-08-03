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
	QueuedUserMessageFailurePromptCommandNotFound      QueuedUserMessageFailureReason = "prompt_command_not_found"
	QueuedUserMessageFailurePromptCommandRead          QueuedUserMessageFailureReason = "prompt_command_read"
)

type TranscriptUserMessageFlushed struct {
	StepID     runtimeids.StepID
	Operations []RuntimeOperationRef
}

type TranscriptQueuedMessageState struct {
	ClientRequestID runtimeids.RuntimeClientRequestID
	QueueItemID     runtimeids.QueueItemID
	Status          QueuedUserMessageStatus
	FailureReason   *QueuedUserMessageFailureReason
	Text            *string
	PromptCommand   *string
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
		if operation.Kind == RuntimeOperationKindQueuedMessage && operation.QueueItemID == nil {
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
	case QueuedUserMessageAccepted:
		if s.FailureReason != nil {
			return fmt.Errorf("accepted queued-message state cannot carry failure reason")
		}
		if s.PromptCommand != nil {
			return fmt.Errorf("accepted queued-message state cannot carry prompt command")
		}
		return validateRequiredOptionalText("accepted queued-message state", s.Text)
	case QueuedUserMessageSubmitted, QueuedUserMessageDiscarded:
		if s.FailureReason != nil {
			return fmt.Errorf("%s queued-message state cannot carry failure reason", s.Status)
		}
		if s.PromptCommand != nil {
			return fmt.Errorf("%s queued-message state cannot carry prompt command", s.Status)
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
			if s.PromptCommand != nil {
				return fmt.Errorf("%s queued-message state cannot carry prompt command", *s.FailureReason)
			}
		case QueuedUserMessageFailurePromptCommandNotFound:
			if s.PromptCommand == nil || strings.TrimSpace(*s.PromptCommand) == "" {
				return fmt.Errorf("prompt-command failure requires prompt command")
			}
		case QueuedUserMessageFailurePromptCommandRead:
			if s.PromptCommand == nil || strings.TrimSpace(*s.PromptCommand) == "" {
				return fmt.Errorf("prompt-command failure requires prompt command")
			}
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
