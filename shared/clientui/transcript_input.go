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
)

type TranscriptUserMessageFlushed struct {
	StepID *runtimeids.StepID
}

type TranscriptQueuedMessageState struct {
	QueueItemID   runtimeids.QueueItemID
	Status        QueuedUserMessageStatus
	FailureReason *QueuedUserMessageFailureReason
	Text          *string
}

type TranscriptInterruptedHumanInputItem struct {
	QueueItemID runtimeids.QueueItemID
	Text        string
}

type TranscriptHumanInputInterrupted struct {
	Items []TranscriptInterruptedHumanInputItem
}

func (f TranscriptUserMessageFlushed) Validate() error {
	if f.StepID != nil && f.StepID.IsZero() {
		return fmt.Errorf("user-message flush step id is invalid")
	}
	return nil
}

func (s TranscriptQueuedMessageState) Validate() error {
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
			QueuedUserMessageFailureRuntimeUnavailable:
		default:
			return fmt.Errorf("unknown queued-message failure reason %q", *s.FailureReason)
		}
		return validateRequiredOptionalText("failed queued-message state", s.Text)
	default:
		return fmt.Errorf("unknown queued-message state %q", s.Status)
	}
}

func (e TranscriptHumanInputInterrupted) Validate() error {
	if len(e.Items) == 0 {
		return fmt.Errorf("interrupted human input requires at least one item")
	}
	seen := make(map[runtimeids.QueueItemID]struct{}, len(e.Items))
	for index, item := range e.Items {
		if item.QueueItemID.IsZero() {
			return fmt.Errorf("interrupted human input item %d requires queue item id", index)
		}
		if _, exists := seen[item.QueueItemID]; exists {
			return fmt.Errorf("interrupted human input repeats queue item id %s", item.QueueItemID)
		}
		seen[item.QueueItemID] = struct{}{}
		if strings.TrimSpace(item.Text) == "" {
			return fmt.Errorf("interrupted human input item %d requires text", index)
		}
	}
	return nil
}

func validateRequiredOptionalText(owner string, text *string) error {
	if text == nil || strings.TrimSpace(*text) == "" {
		return fmt.Errorf("%s requires non-empty text", owner)
	}
	return nil
}
