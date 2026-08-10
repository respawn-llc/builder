package session

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/invariant"
	"core/shared/toolspec"
)
func GoalCoreFromState(goal *GoalState) *clientui.Goal {
	if goal == nil { return nil }
	return &clientui.Goal{ID: strings.TrimSpace(goal.ID), Objective: goal.Objective, Status: clientui.RuntimeGoalStatus(strings.TrimSpace(string(goal.Status))), CreatedAt: goal.CreatedAt, UpdatedAt: goal.UpdatedAt}
}
func (s *Store) GoalAvailability() (clientui.GoalAvailability, error) {
	if s == nil { return "", errors.New("session store is required") }
	return GoalAvailabilityFromMeta(s.Meta())
}
func GoalAvailabilityFromMeta(meta Meta) (clientui.GoalAvailability, error) {
	if meta.Locked == nil { return clientui.GoalAvailabilityAvailable, nil }
	if !meta.Locked.HasEnabledTools { return "", malformedGoalContract(meta, errors.New("enabled tool snapshot is absent")) }
	availability := clientui.GoalAvailabilityAgentCapabilityMissing
	for _, raw := range meta.Locked.EnabledTools {
		tool, ok := toolspec.ParseID(raw)
		if !ok { return "", malformedGoalContract(meta, fmt.Errorf("enabled tool %q is invalid", raw)) }
		if tool == toolspec.ToolAskQuestion { availability = clientui.GoalAvailabilityAvailable }
	}
	return availability, nil
}
type malformedGoalContractError struct { SessionID string; Generation int; Cause error }
func (e malformedGoalContractError) Error() string { return fmt.Sprintf("session %q locked contract generation %d is malformed: %v", e.SessionID, e.Generation, e.Cause) }
func (e malformedGoalContractError) Unwrap() error { return e.Cause }
func malformedGoalContract(meta Meta, cause error) error {
	err := malformedGoalContractError{SessionID: meta.SessionID, Generation: meta.PromptCacheLineageGeneration, Cause: cause}
	diagnostic := invariant.FailureDiagnostic(invariant.ScopeSessionPersistence, "goal_availability", err)
	diagnostic.Fields[invariant.FieldSessionID] = meta.SessionID
	diagnostic.Fields[invariant.FieldResolverInputs] = fmt.Sprintf("prompt_cache_lineage_generation=%d", meta.PromptCacheLineageGeneration)
	invariant.NewPolicy().Check(false, diagnostic)
	return err
}
