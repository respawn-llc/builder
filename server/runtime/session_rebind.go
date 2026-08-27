package runtime

import (
	"core/server/session"
)

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
