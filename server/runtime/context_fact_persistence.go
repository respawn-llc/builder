package runtime

import (
	"fmt"

	"core/server/session"
)

func (e *Engine) persistManualCompactEligibilityBestEffort(stepID string, eligible bool) {
	if e == nil || e.store == nil {
		return
	}
	if err := e.store.SetManualCompactEligibility(eligible); err != nil {
		e.reportContextFactPersistenceError(stepID, "manual Compact eligibility", err)
		return
	}
	e.setPresentedManualCompactEligibility(eligible)
}

func (e *Engine) persistCompletedCompactionFactsBestEffort(
	stepID string,
	completedCompactionCount int,
) {
	if e == nil || e.store == nil {
		return
	}
	if err := e.store.SetSessionContextFacts(completedCompactionCount, false); err != nil {
		e.reportContextFactPersistenceError(stepID, "completed compaction", err)
		return
	}
	count := completedCompactionCount
	eligible := false
	e.setContextFacts(sessionContextFacts(count, eligible))
}

func sessionContextFacts(count int, eligible bool) session.SessionContextFacts {
	return session.SessionContextFacts{
		CompletedCompactionCount: &count,
		ManualCompactEligible:    &eligible,
	}
}

func (e *Engine) reportContextFactPersistenceError(stepID, field string, err error) {
	if err == nil {
		return
	}
	diagnostic := fmt.Errorf("persist Session Context %s: %w", field, err)
	if steerErr := e.steer(stepID, steerEventIntent(Event{
		Kind:   EventContextFactsPersistFailed,
		StepID: stepID,
		Error:  diagnostic.Error(),
	})); steerErr != nil {
		e.surfaceRunError(fmt.Errorf("%w; surface diagnostic: %v", diagnostic, steerErr))
	}
}
