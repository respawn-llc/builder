package runtime

import (
	"context"

	"core/prompts"
	"core/server/llm"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type compactionReminderCoordinator struct {
	engine *Engine
}

func newCompactionReminderCoordinator(engine *Engine) compactionReminderCoordinator {
	return compactionReminderCoordinator{engine: engine}
}

func (c compactionReminderCoordinator) maybeAppend(ctx context.Context, stepID string) error {
	e := c.engine
	planningSnapshot := e.compactionPlanningSnapshot()
	planner := e.compactionPlannerState()
	if !planner.autoCompactionAvailable(planningSnapshot) {
		return nil
	}
	limit := planner.soonReminderLimit(planningSnapshot)
	if limit <= 0 {
		return nil
	}
	if !e.usageAtOrAboveLimit(ctx, limit) {
		return nil
	}
	// Gate on forced compaction before estimating runway. Usage at or above the forced limit is
	// handled by compaction, not a reminder, and estimatedToolCallsUntilForcedHandoff treats that
	// state as an unreachable-invariant panic, so it must only be reached once usage is confirmed
	// to sit in the soon-reminder band (below the forced limit).
	if e.shouldAutoCompactWithContext(ctx) {
		return nil
	}
	if e.compactionRuntimeState().SoonReminderIssued() {
		return nil
	}
	// Re-snapshot after the forced-compaction gate so the estimate reads the post-recount usage the
	// gate just resolved (usageAtOrAboveLimit warms the precise current-token cache). The entry
	// snapshot holds the pre-recount usage, which can sit above the forced limit even when the gate's
	// precise recount cleared it, so using it here would trip the unreachable-state panic spuriously.
	estimatedToolCalls := planner.estimatedToolCallsUntilForcedHandoff(e.compactionPlanningSnapshot())
	content := prompts.RenderCompactionSoonReminderPrompt(e.triggerHandoffConfigured(), estimatedToolCalls)
	if content == "" {
		return nil
	}
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeCompactionSoonReminder),
		Content:     textutil.Value(content),
	}})); err != nil {
		return err
	}
	return e.persistCompactionSoonReminderIssued(true)
}

func (e *Engine) persistCompactionSoonReminderIssued(issued bool) error {
	e.compactionRuntimeState().SetSoonReminderIssued(issued)
	return e.store.SetCompactionSoonReminderIssued(issued)
}

func (e *Engine) triggerHandoffConfigured() bool {
	shape, err := e.lockedRequestShape()
	if err != nil {
		return false
	}
	for _, id := range shape.EnabledTools {
		if id == toolspec.ToolTriggerHandoff {
			return true
		}
	}
	return false
}
