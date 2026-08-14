package clientui

import "fmt"

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
