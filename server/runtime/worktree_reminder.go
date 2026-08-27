package runtime

import (
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/textutil"
)

func (e *Engine) SteerWorktreeTransitionFailure(outcome clientui.WorktreeTransitionOutcome) error {
	if err := outcome.Validate(); err != nil {
		return fmt.Errorf("validate worktree transition outcome: %w", err)
	}
	if outcome.State != clientui.WorktreeTransitionFailed {
		return errors.New("failed worktree transition outcome is required")
	}
	diagnostic := strings.TrimSpace(outcome.Failure.Diagnostic)
	return e.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeErrorFeedback),
		Content: textutil.Value(fmt.Sprintf(
			"Scheduled worktree %s transition %s failed: %s",
			outcome.Transition,
			outcome.OperationID.String(),
			diagnostic,
		)),
	}}))
}

func (e *Engine) materializePendingWorktreeReminder(stepID string) error {
	state := session.CloneWorktreeReminderState(e.store.Meta().WorktreeReminder)
	if state == nil {
		return nil
	}
	metaResult, err := e.activeMetaContextBuilder(e.currentModel(), e.cfg.SkillPolicy).Build(metaContextBuildOptions{WorktreeReminder: state})
	if err != nil {
		return err
	}
	return e.steerMetaContextIfChanged(stepID, append(metaResult.Worktree, metaResult.WorktreeExit...))
}
