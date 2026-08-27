package runtime

import (
	"fmt"

	"core/server/chatcontext"
	compaction "core/shared/config"
	"core/shared/serverapi"
)

type compactionPlanningSnapshot struct {
	autoCompactionEnabled         bool
	preSubmitCompactionLeadTokens int
	policy                        chatcontext.Policy
	maxOutputTokens               int
	lockedMaxOutputTokens         int
	// currentUsedTokens is the live consumed-context estimate (currentTokenUsage), the same source
	// the auto-compaction gates check. It is what the handoff-runway estimate must measure so the
	// estimate and the gates never disagree.
	currentUsedTokens int
}

type compactionEngineKind string

const (
	compactionEngineNone   compactionEngineKind = "none"
	compactionEngineLocal  compactionEngineKind = "local"
	compactionEngineRemote compactionEngineKind = "remote"
)

type compactionEnginePlan struct {
	engineKind compactionEngineKind
}

type compactionPlanner struct{}

const eagerCompactionContextPercent = 88

func newCompactionPlanner() *compactionPlanner {
	return &compactionPlanner{}
}
func (p *compactionPlanner) mode(policy chatcontext.Policy) string {
	switch policy.CompactionMode {
	case serverapi.ChatContextCompactionModeDisabled:
		return "none"
	case serverapi.ChatContextCompactionModeProviderNative:
		return "native"
	default:
		return "local"
	}
}

func (p *compactionPlanner) autoCompactionAvailable(snapshot compactionPlanningSnapshot) bool {
	return snapshot.autoCompactionEnabled && snapshot.policy.CompactionMode != serverapi.ChatContextCompactionModeDisabled
}

func (p *compactionPlanner) enginePlan(snapshot compactionPlanningSnapshot) compactionEnginePlan {
	switch snapshot.policy.CompactionMode {
	case serverapi.ChatContextCompactionModeDisabled:
		return compactionEnginePlan{engineKind: compactionEngineNone}
	case serverapi.ChatContextCompactionModeProviderNative:
		return compactionEnginePlan{engineKind: compactionEngineRemote}
	default:
		return compactionEnginePlan{engineKind: compactionEngineLocal}
	}
}

func (p *compactionPlanner) contextWindowTokens(snapshot compactionPlanningSnapshot) int {
	return int(snapshot.policy.ContextWindowTokens)
}

func (p *compactionPlanner) eagerCompactionEligible(snapshot compactionPlanningSnapshot) bool {
	window := p.contextWindowTokens(snapshot)
	if window <= 0 || snapshot.currentUsedTokens < 0 {
		return false
	}
	return snapshot.currentUsedTokens >= window*eagerCompactionContextPercent/100
}
func (p *compactionPlanner) autoCompactTokenLimit(snapshot compactionPlanningSnapshot) int {
	return int(snapshot.policy.AutomaticThresholdTokens)
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
