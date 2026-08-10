package session

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
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

func GoalAvailabilityFromMeta(meta Meta) (clientui.GoalAvailability, error) {
	if meta.Locked == nil {
		return clientui.GoalAvailabilityAvailable, nil
	}
	if !meta.Locked.HasEnabledTools {
		return "", fmt.Errorf("session %q locked contract has no enabled tool snapshot", meta.SessionID)
	}
	for _, raw := range meta.Locked.EnabledTools {
		tool, ok := toolspec.ParseID(raw)
		if !ok {
			return "", fmt.Errorf("session %q locked contract has invalid enabled tool %q", meta.SessionID, raw)
		}
		if tool == toolspec.ToolAskQuestion {
			return clientui.GoalAvailabilityAvailable, nil
		}
	}
	return clientui.GoalAvailabilityAgentCapabilityMissing, nil
}
