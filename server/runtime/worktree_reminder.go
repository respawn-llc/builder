package runtime

import (
	"core/server/session"
)

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
