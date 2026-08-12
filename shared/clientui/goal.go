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
	switch a {
	case GoalAvailabilityAvailable, GoalAvailabilityAgentCapabilityMissing:
		return nil
	default:
		return fmt.Errorf("unknown goal availability %q", a)
	}
}

type Goal struct {
	ID        string            `json:"id"`
	Objective string            `json:"objective"`
	Status    RuntimeGoalStatus `json:"status"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func (g Goal) Validate() error {
	if strings.TrimSpace(g.ID) == "" || strings.TrimSpace(g.Objective) == "" || !validGoalStatus(g.Status) || g.CreatedAt.IsZero() || g.UpdatedAt.IsZero() {
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
	Goal         *Goal             `json:"goal,omitempty"`
	Pending      *GoalPreview      `json:"pending,omitempty"`
	Availability *GoalAvailability `json:"availability,omitempty"`
}

func (g GoalEnvelope) Validate() error {
	if err := g.Availability.Validate(); err != nil {
		return err
	}
	if g.Goal == nil {
		return nil
	}
	return g.Goal.Validate()
}

func (r GoalMutationResult) Validate() error {
	if r.Availability != nil {
		if err := r.Availability.Validate(); err != nil {
			return err
		}
	}
	if r.Goal != nil && r.Pending != nil {
		return fmt.Errorf("goal mutation result cannot contain Goal and pending preview")
	}
	if r.Goal != nil {
		return r.Goal.Validate()
	}
	if r.Pending != nil && (strings.TrimSpace(r.Pending.Objective) == "" || !validGoalStatus(r.Pending.Status)) {
		return fmt.Errorf("invalid goal preview fields")
	}
	return nil
}

func validGoalStatus(status RuntimeGoalStatus) bool {
	return status == RuntimeGoalStatusActive || status == RuntimeGoalStatusPaused || status == RuntimeGoalStatusComplete
}
