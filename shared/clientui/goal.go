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
	if strings.TrimSpace(g.ID) == "" || strings.TrimSpace(g.Objective) == "" ||
		(g.Status != RuntimeGoalStatusActive && g.Status != RuntimeGoalStatusPaused && g.Status != RuntimeGoalStatusComplete) ||
		g.CreatedAt.IsZero() || g.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid goal fields")
	}
	return nil
}

type GoalEnvelope struct {
	Goal         *Goal            `json:"goal,omitempty"`
	Availability GoalAvailability `json:"availability"`
}

func ProjectGoal(goal *Goal, availability GoalAvailability) GoalEnvelope {
	return GoalEnvelope{Goal: goal, Availability: availability}
}
func RuntimeGoalFromEnvelope(e GoalEnvelope, suspended bool) RuntimeGoal {
	return RuntimeGoal{Goal: e.Goal, Availability: e.Availability, Suspended: suspended}
}

func TranscriptGoalStatusFromEnvelope(e GoalEnvelope, suspended bool) TranscriptGoalStatus {
	if e.Goal == nil {
		return TranscriptGoalStatus{Availability: e.Availability}
	}
	return TranscriptGoalStatus{Goal: &TranscriptGoal{Goal: e.Goal, Suspended: suspended}, Availability: e.Availability}
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
