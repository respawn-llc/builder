package session

import (
	"errors"
	"fmt"

	"core/shared/invariant"
	"core/shared/toolspec"
)

type GoalAvailability uint8

const (
	GoalAvailable GoalAvailability = iota + 1
	GoalAgentCapabilityMissing
)

func (s *Store) GoalAvailability() (GoalAvailability, error) {
	if s == nil {
		return 0, errors.New("session store is required")
	}
	return GoalAvailabilityFromMeta(s.Meta())
}

func (s *Store) GoalMutationAvailability() *GoalAvailability {
	availability, err := s.GoalAvailability()
	if err != nil {
		return nil
	}
	return &availability
}
func GoalAvailabilityFromMeta(meta Meta) (GoalAvailability, error) {
	if meta.Locked == nil || !meta.Locked.HasEnabledTools {
		return GoalAvailable, nil
	}
	availability := GoalAgentCapabilityMissing
	for _, raw := range meta.Locked.EnabledTools {
		tool, ok := toolspec.ParseID(raw)
		if !ok {
			return 0, malformedGoalContract(meta, fmt.Errorf("enabled tool %q is invalid", raw))
		}
		if tool == toolspec.ToolAskQuestion {
			availability = GoalAvailable
		}
	}
	return availability, nil
}

func malformedGoalContract(meta Meta, cause error) error {
	err := fmt.Errorf("session %q locked contract is malformed: %w", meta.SessionID, cause)
	d := invariant.FailureDiagnostic(invariant.ScopeSessionPersistence, "goal_availability", err)
	d.Fields[invariant.FieldSessionID] = meta.SessionID
	d.Fields[invariant.FieldResolverInputs] = "locked_contract"
	invariant.NewPolicy().Check(false, d)
	return err
}
