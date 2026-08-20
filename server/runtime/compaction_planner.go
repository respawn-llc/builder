package runtime

import (
	"fmt"

	"core/server/llm"
	compaction "core/shared/config"
)

type compactionPlanningSnapshot struct {
	autoCompactionEnabled         bool
	compactionMode                string
	autoCompactTokenLimit         int
	preSubmitCompactionLeadTokens int
	contextWindowTokens           int
	effectiveContextWindowPercent int
	maxOutputTokens               int
	lockedMaxOutputTokens         int
	lastUsage                     llm.Usage
	// currentUsedTokens is the live consumed-context estimate (currentTokenUsage), the same source
	// the auto-compaction gates check. It is what the handoff-runway estimate must measure so the
	// estimate and the gates never disagree; lastUsage is a stale provider snapshot kept only for
	// window-capacity derivation.
	currentUsedTokens int
}

type compactionEngineKind string

const (
	compactionEngineNone   compactionEngineKind = "none"
	compactionEngineLocal  compactionEngineKind = "local"
	compactionEngineRemote compactionEngineKind = "remote"
)

type compactionEnginePlan struct {
	engineKind                     compactionEngineKind
	fallbackToLocalOnBadCheckpoint bool
}

type compactionPlanner struct{}

const eagerCompactionContextPercent = 88

func newCompactionPlanner() *compactionPlanner {
	return &compactionPlanner{}
}

func (p *compactionPlanner) mode(raw string) string {
	normalized, ok := NormalizeCompactionMode(raw)
	if !ok {
		return "native"
	}
	return normalized
}

func (p *compactionPlanner) autoCompactionAvailable(snapshot compactionPlanningSnapshot) bool {
	return snapshot.autoCompactionEnabled && p.mode(snapshot.compactionMode) != "none"
}

func (p *compactionPlanner) enginePlan(snapshot compactionPlanningSnapshot, caps llm.ProviderCapabilities) compactionEnginePlan {
	switch p.mode(snapshot.compactionMode) {
	case "none":
		return compactionEnginePlan{engineKind: compactionEngineNone}
	case "native":
		if caps.SupportsResponsesCompact {
			return compactionEnginePlan{
				engineKind:                     compactionEngineRemote,
				fallbackToLocalOnBadCheckpoint: true,
			}
		}
		return compactionEnginePlan{engineKind: compactionEngineLocal}
	default:
		return compactionEnginePlan{engineKind: compactionEngineLocal}
	}
}

func (p *compactionPlanner) contextWindowTokens(snapshot compactionPlanningSnapshot) int {
	if snapshot.contextWindowTokens > 0 {
		return snapshot.contextWindowTokens
	}
	if snapshot.lastUsage.WindowTokens > 0 {
		return snapshot.lastUsage.WindowTokens
	}
	return defaultContextWindowTokens
}

func (p *compactionPlanner) effectiveContextTokenLimit(snapshot compactionPlanningSnapshot) int {
	percent := snapshot.effectiveContextWindowPercent
	if percent <= 0 || percent > 100 {
		percent = 95
	}
	return (p.contextWindowTokens(snapshot) * percent) / 100
}

func (p *compactionPlanner) eagerCompactionEligible(snapshot compactionPlanningSnapshot) bool {
	window := p.contextWindowTokens(snapshot)
	if window <= 0 || snapshot.currentUsedTokens < 0 {
		return false
	}
	return snapshot.currentUsedTokens >= window*eagerCompactionContextPercent/100
}

func (p *compactionPlanner) autoCompactTokenLimit(snapshot compactionPlanningSnapshot) int {
	if snapshot.autoCompactTokenLimit > 0 {
		return snapshot.autoCompactTokenLimit
	}
	limit := p.effectiveContextTokenLimit(snapshot)
	if limit < 1 {
		return 1
	}
	return limit
}

func (p *compactionPlanner) preSubmitTokenLimit(snapshot compactionPlanningSnapshot) int {
	limit := p.autoCompactTokenLimit(snapshot)
	if limit <= 0 {
		return 0
	}
	runwayTokens := compaction.DefaultPreSubmitRunwayTokens
	if snapshot.preSubmitCompactionLeadTokens > 0 {
		runwayTokens = snapshot.preSubmitCompactionLeadTokens
	}
	return compaction.EffectivePreSubmitThresholdTokens(limit, runwayTokens)
}

func (p *compactionPlanner) soonReminderLimit(snapshot compactionPlanningSnapshot) int {
	limit := p.autoCompactTokenLimit(snapshot)
	if limit <= 0 {
		return 0
	}
	reminderLimit := (limit * compactionSoonReminderPercent) / 100
	if reminderLimit < 1 {
		return 1
	}
	return reminderLimit
}

// estimatedToolCallsUntilForcedHandoff estimates how many more assistant tool
// calls fit before the forced compaction threshold, so the soon-reminder can
// tell the model roughly how much runway it has left to trigger a handoff
// voluntarily.
//
// It measures currentUsedTokens (currentTokenUsage), the same source the
// auto-compaction gates check, so "the forced gate let us through" and "the
// estimate is below the forced limit" are the same measurement. The forced gate
// (usageAtOrAboveLimit) compares input + reservedOutput against the limit, so
// the reserved output budget is subtracted here too; otherwise the runway would
// promise tool calls whose tokens are already reserved for the model response.
func (p *compactionPlanner) estimatedToolCallsUntilForcedHandoff(snapshot compactionPlanningSnapshot) int {
	forcedLimit := p.autoCompactTokenLimit(snapshot)
	reservedOutput := p.reservedOutputTokens(snapshot)
	remaining := forcedLimit - reservedOutput - snapshot.currentUsedTokens
	if remaining <= 0 {
		// The soon-reminder is gated behind the same usage-vs-forced-limit check that triggers forced
		// compaction, which already accounts for reservedOutput, so by the time this estimate is computed
		// consumed tokens plus the reservation are always strictly below the forced limit. Reaching here
		// means forced compaction failed to precede the reminder: an unreachable-state invariant
		// violation, not a value to clamp away.
		panic(fmt.Sprintf(
			"compaction soon reminder estimate computed with consumed tokens %d plus reserved output %d at or above forced compaction limit %d; forced compaction must precede the reminder",
			snapshot.currentUsedTokens, reservedOutput, forcedLimit,
		))
	}
	return compaction.EstimatedToolCallsForTokenBudget(remaining)
}

func (p *compactionPlanner) reservedOutputTokens(snapshot compactionPlanningSnapshot) int {
	if snapshot.lockedMaxOutputTokens > 0 {
		return snapshot.lockedMaxOutputTokens
	}
	if snapshot.maxOutputTokens > 0 {
		return snapshot.maxOutputTokens
	}
	return 0
}
