package runtime

import (
	"errors"
	"fmt"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
)

func (e *Engine) SteerSessionRebindFailureDiagnostic(cause error) (session.CommitReceipt, error) {
	if cause == nil {
		return session.CommitReceipt{}, errors.New("Session rebind failure cause is required")
	}
	return e.steerRuntimeWithCommitReceipt(
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeErrorFeedback),
				Content: textutil.Value(fmt.Sprintf(
					"Session move failed before its destination could be applied: %s\nThe Session remains in its previous Project and Working Directory.",
					cause,
				)),
			}},
		),
	)
}

func (e *Engine) SteerSessionRebindFailure(reminder session.SessionRebindReminder) (session.CommitReceipt, error) {
	normalized, err := session.NormalizeSessionRebindReminder(reminder)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	if normalized.Kind != session.SessionRebindReminderFailed {
		return session.CommitReceipt{}, errors.New("failed Session rebind reminder is required")
	}
	message, ok := sessionRebindMetaMessage(normalized)
	if !ok {
		return session.CommitReceipt{}, errors.New("failed Session rebind reminder produced no model context")
	}
	return e.steerRuntimeWithCommitReceipt(
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			[]llm.Message{message},
		),
	)
}

func (e *Engine) materializePendingSessionRebindReminder(stepID string) error {
	reminder := session.CloneSessionRebindReminder(e.store.Meta().RebindReminder)
	if reminder == nil {
		return nil
	}
	result, err := e.activeMetaContextBuilder(e.currentModel(), e.cfg.SkillPolicy).Build(metaContextBuildOptions{
		SessionRebindReminder: reminder,
	})
	if err != nil {
		return err
	}
	receipt, err := e.steerMetaContextIfChangedWithReceipt(stepID, result.SessionRebind)
	if !receipt.Committed {
		return err
	}
	return e.store.SetSessionRebindReminder(nil)
}
