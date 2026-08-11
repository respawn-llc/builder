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
	if goal == nil {
		return nil
	}
	return &clientui.Goal{ID: strings.TrimSpace(goal.ID), Objective: goal.Objective, Status: clientui.RuntimeGoalStatus(strings.TrimSpace(string(goal.Status))), CreatedAt: goal.CreatedAt, UpdatedAt: goal.UpdatedAt}
}
func (s *Store) GoalAvailability() (clientui.GoalAvailability, error) {
	if s == nil {
		return "", errors.New("session store is required")
	}
	return GoalAvailabilityFromMeta(s.Meta())
}
func (s *Store) GoalMutationAvailability() *clientui.GoalAvailability {
	availability, err := s.GoalAvailability()
	if err != nil {
		return nil
	}
	return &availability
}
func GoalAvailabilityFromMeta(meta Meta) (clientui.GoalAvailability, error) {
	if meta.Locked == nil {
		return clientui.GoalAvailabilityAvailable, nil
	}
	if !meta.Locked.HasEnabledTools {
		return "", malformedGoalContract(meta, errors.New("enabled tool snapshot is absent"))
	}
	a := clientui.GoalAvailabilityAgentCapabilityMissing
	for _, raw := range meta.Locked.EnabledTools {
		tool, ok := toolspec.ParseID(raw)
		if !ok {
			return "", malformedGoalContract(meta, fmt.Errorf("enabled tool %q is invalid", raw))
		}
		if tool == toolspec.ToolAskQuestion {
			a = clientui.GoalAvailabilityAvailable
		}
	}
	return a, nil
}

func malformedGoalContract(meta Meta, cause error) error {
	err := fmt.Errorf("session %q locked contract generation %d is malformed: %w", meta.SessionID, meta.PromptCacheLineageGeneration, cause)
	d := invariant.FailureDiagnostic(invariant.ScopeSessionPersistence, "goal_availability", err)
	d.Fields[invariant.FieldSessionID] = meta.SessionID
	d.Fields[invariant.FieldResolverInputs] = fmt.Sprintf("prompt_cache_lineage_generation=%d", meta.PromptCacheLineageGeneration)
	invariant.NewPolicy().Check(false, d)
	return err
}
