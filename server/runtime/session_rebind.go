package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/shared/serverapi"
	"core/shared/textutil"
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

func (e *Engine) SteerSessionRebindReminder(reminder session.SessionRebindReminder) error {
	normalized, err := session.NormalizeSessionRebindReminder(reminder)
	if err != nil {
		return fmt.Errorf("normalize Session rebind reminder: %w", err)
	}
	result, err := e.activeMetaContextBuilder(e.currentModel(), e.cfg.SkillPolicy).Build(metaContextBuildOptions{
		SessionRebindReminder: &normalized,
	})
	if err != nil {
		return err
	}
	receipt, err := e.steerMetaContextIfChangedWithReceipt("", steeringPriorityNormal, result.SessionRebind)
	if err != nil || !receipt.Committed {
		return err
	}
	return e.store.SetSessionRebindReminder(nil)
}

func (e *Engine) SteerSessionRetargetFailure(outcome serverapi.SessionRetargetOutcome) error {
	if err := outcome.Validate(); err != nil {
		return fmt.Errorf("validate session retarget outcome: %w", err)
	}
	if outcome.Kind != serverapi.SessionRetargetOutcomeFailed {
		return fmt.Errorf("failed session retarget outcome is required")
	}
	failure := outcome.Failure
	content := fmt.Sprintf(
		"Session move failed: %s\nThe Session remains in Project %s with Working Directory %s.",
		strings.TrimSpace(failure.Diagnostic),
		failure.UnchangedProject.Name,
		failure.UnchangedWorkingDirectory,
	)
	return e.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeErrorFeedback),
		Content:     textutil.Value(content),
	}}))
}
