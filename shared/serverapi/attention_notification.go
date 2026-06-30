package serverapi

import (
	"context"
	"errors"
	"fmt"

	"core/shared/clientui"
)

type AttentionNotificationSubscribeRequest struct{}

func (r AttentionNotificationSubscribeRequest) Validate() error {
	return nil
}

type AttentionSessionNotificationSubscribeRequest struct {
	SessionID                    string `json:"session_id"`
	IncludePendingPromptSnapshot bool   `json:"include_pending_prompt_snapshot,omitempty"`
}

func (r AttentionSessionNotificationSubscribeRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}

type AttentionNotificationSubscription interface {
	Next(context.Context) (clientui.AttentionNotificationEvent, error)
	Close() error
}

func ValidateAttentionNotificationEvent(event clientui.AttentionNotificationEvent) error {
	if event.Sequence == 0 {
		return errors.New("attention notification sequence is required")
	}
	switch event.Type {
	case clientui.AttentionNotificationEventPending:
		if err := validateAttentionNotificationSource(event.Source); err != nil {
			return err
		}
		if event.Pending == nil {
			return errors.New("pending attention notification payload is required")
		}
		return validateAttentionNotification(*event.Pending)
	case clientui.AttentionNotificationEventResolved:
		if err := validateAttentionNotificationSource(event.Source); err != nil {
			return err
		}
		if event.Pending != nil {
			return errors.New("resolved attention notification must not carry pending payload")
		}
		if event.ID == "" {
			return errors.New("resolved attention notification id is required")
		}
		if event.Kind == "" {
			return errors.New("resolved attention notification kind is required")
		}
		if err := validateAttentionNotificationKind(event.Kind); err != nil {
			return err
		}
		if event.OccurredAt.IsZero() {
			return errors.New("resolved attention notification occurred_at is required")
		}
		return nil
	case clientui.AttentionNotificationEventSnapshotComplete:
		if event.Source != clientui.AttentionNotificationSourceSnapshot {
			return errors.New("snapshot_complete attention notification source must be snapshot")
		}
		if event.Pending != nil || event.ID != "" || event.Kind != "" || !event.OccurredAt.IsZero() {
			return errors.New("snapshot_complete attention notification must not carry id, kind, time, target, or presentation payload")
		}
		if event.SessionID == "" {
			return errors.New("snapshot_complete attention notification session_id is required")
		}
		return nil
	default:
		return fmt.Errorf("unsupported attention notification event type %q", event.Type)
	}
}

func validateAttentionNotification(notification clientui.AttentionNotification) error {
	if notification.ID == "" {
		return errors.New("attention notification id is required")
	}
	if notification.Kind == "" {
		return errors.New("attention notification kind is required")
	}
	if err := validateAttentionNotificationKind(notification.Kind); err != nil {
		return err
	}
	if notification.Revision == 0 {
		return errors.New("attention notification revision is required")
	}
	if notification.OccurredAt.IsZero() {
		return errors.New("attention notification occurred_at is required")
	}
	if err := validateAttentionNotificationTarget(notification.Target); err != nil {
		return err
	}
	if notification.Presentation.Title == "" || notification.Presentation.Body == "" {
		return errors.New("attention notification presentation title and body are required")
	}
	return nil
}

func validateAttentionNotificationSource(source clientui.AttentionNotificationSource) error {
	switch source {
	case clientui.AttentionNotificationSourceLive, clientui.AttentionNotificationSourceSnapshot:
		return nil
	default:
		return fmt.Errorf("unsupported attention notification source %q", source)
	}
}

func validateAttentionNotificationKind(kind clientui.AttentionNotificationKind) error {
	switch kind {
	case clientui.AttentionNotificationKindQuestion, clientui.AttentionNotificationKindApproval:
		return nil
	default:
		return fmt.Errorf("unsupported attention notification kind %q", kind)
	}
}

func validateAttentionNotificationTarget(target clientui.AttentionNotificationTarget) error {
	switch target.Kind {
	case clientui.AttentionNotificationTargetTaskDetail:
		if target.TaskID == "" {
			return errors.New("task-detail attention notification target task_id is required")
		}
		if target.Focus == nil {
			return errors.New("task-detail attention notification target focus is required")
		}
		return validateTaskDetailFocus(*target.Focus)
	case clientui.AttentionNotificationTargetSessionPrompt:
		if target.SessionID == "" {
			return errors.New("session-prompt attention notification target session_id is required")
		}
		return nil
	default:
		return fmt.Errorf("unsupported attention notification target kind %q", target.Kind)
	}
}

func validateTaskDetailFocus(focus clientui.AttentionNotificationTaskDetailFocus) error {
	switch focus.Kind {
	case clientui.AttentionNotificationFocusQuestion:
		if len(focus.AskIDs) == 0 {
			return errors.New("question attention notification focus ask_ids is required")
		}
		for _, askID := range focus.AskIDs {
			if askID == "" {
				return errors.New("question attention notification focus ask_ids must be non-empty")
			}
		}
		return nil
	case clientui.AttentionNotificationFocusApproval:
		if focus.TaskTransitionID == "" {
			return errors.New("approval attention notification focus task_transition_id is required")
		}
		return nil
	default:
		return fmt.Errorf("unsupported attention notification focus kind %q", focus.Kind)
	}
}
