package runtime

import (
	"errors"

	"core/server/llm"
	"core/server/session"
)

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
	return e.steerWithoutStepWithCommitReceipt(
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
	receipt, err := e.steerMetaContextIfChangedWithReceipt(stepID, steeringPriorityNormal, result.SessionRebind)
	if !receipt.Committed {
		return err
	}
	return e.store.SetSessionRebindReminder(nil)
}
