package clientui

import (
	"fmt"
	"strings"
	"time"
)

type GoalAvailability string

const (
	GoalAvailabilityAvailable              GoalAvailability = "available"
	GoalAvailabilityAgentCapabilityMissing GoalAvailability = "agent_capability_missing"
)

func (a GoalAvailability) Validate() error {
	if a != GoalAvailabilityAvailable && a != GoalAvailabilityAgentCapabilityMissing {
		return fmt.Errorf("unknown goal availability %q", a)
	}
	return nil
}

type Goal struct {
	ID        string            `json:"id"`
	Objective string            `json:"objective"`
	Status    RuntimeGoalStatus `json:"status"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func (g Goal) Validate() error {
	if strings.TrimSpace(g.ID) == "" || strings.TrimSpace(g.Objective) == "" || (g.Status != RuntimeGoalStatusActive && g.Status != RuntimeGoalStatusPaused && g.Status != RuntimeGoalStatusComplete) || g.CreatedAt.IsZero() || g.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid goal fields")
	}
	return nil
}

type GoalEnvelope struct {
	Goal         *Goal            `json:"goal,omitempty"`
	Availability GoalAvailability `json:"availability"`
}
type GoalPreview struct {
	Objective string            `json:"objective"`
	Status    RuntimeGoalStatus `json:"status"`
}

type GoalMutationResult struct {
	Goal         *Goal            `json:"goal,omitempty"`
	Pending      *GoalPreview     `json:"pending,omitempty"`
	Availability GoalAvailability `json:"availability"`
}

func ProjectGoal(goal *Goal, availability GoalAvailability) GoalEnvelope {
	return GoalEnvelope{Goal: goal, Availability: availability}
}
func (g GoalEnvelope) Validate() error {
	if err := g.Availability.Validate(); err != nil || g.Goal == nil {
		return err
	}
	return g.Goal.Validate()
}

func (r GoalMutationResult) Validate() error {
	if err := r.Availability.Validate(); err != nil {
		return err
	}
	if r.Goal != nil && r.Pending != nil {
		return fmt.Errorf("goal mutation result cannot contain Goal and pending preview")
	}
	if r.Goal != nil {
		return r.Goal.Validate()
	}
	if r.Pending != nil {
		if strings.TrimSpace(r.Pending.Objective) == "" || (r.Pending.Status != RuntimeGoalStatusActive && r.Pending.Status != RuntimeGoalStatusPaused && r.Pending.Status != RuntimeGoalStatusComplete) {
			return fmt.Errorf("invalid goal preview fields")
		}
	}
	return nil
}
