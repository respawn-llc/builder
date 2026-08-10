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
	if a != GoalAvailabilityAvailable && a != GoalAvailabilityAgentCapabilityMissing { return fmt.Errorf("unknown goal availability %q", a) }
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
	if strings.TrimSpace(g.ID) == "" || strings.TrimSpace(g.Objective) == "" || (g.Status != RuntimeGoalStatusActive && g.Status != RuntimeGoalStatusPaused && g.Status != RuntimeGoalStatusComplete) || g.CreatedAt.IsZero() || g.UpdatedAt.IsZero() { return fmt.Errorf("invalid goal fields") }
	return nil
}
