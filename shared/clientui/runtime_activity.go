package clientui

import (
	"fmt"
	"strings"
)

type ReadModelVersion struct {
	Epoch      string
	Generation uint64
	Sequence   uint64
}

func NewReadModelVersion(epoch string, generation uint64, sequence uint64) (ReadModelVersion, error) {
	version := ReadModelVersion{
		Epoch:      strings.TrimSpace(epoch),
		Generation: generation,
		Sequence:   sequence,
	}
	if err := version.Validate(); err != nil {
		return ReadModelVersion{}, err
	}
	return version, nil
}

func (v ReadModelVersion) Validate() error {
	if strings.TrimSpace(v.Epoch) == "" {
		return fmt.Errorf("read model version epoch is required")
	}
	if v.Generation == 0 {
		return fmt.Errorf("read model version generation is required")
	}
	if v.Sequence == 0 {
		return fmt.Errorf("read model version sequence is required")
	}
	return nil
}

func (v ReadModelVersion) NewerThan(other ReadModelVersion) bool {
	return v.Epoch == other.Epoch &&
		v.Generation == other.Generation &&
		v.Sequence > other.Sequence
}

type RuntimeActivityState string

const (
	RuntimeActivityUnavailable    RuntimeActivityState = "unavailable"
	RuntimeActivityRegisteredIdle RuntimeActivityState = "registered_idle"
	RuntimeActivityStarting       RuntimeActivityState = "starting"
	RuntimeActivityRunning        RuntimeActivityState = "running"
	RuntimeActivityAwaitingPrompt RuntimeActivityState = "awaiting_prompt"
	RuntimeActivityDraining       RuntimeActivityState = "draining"
	RuntimeActivityClosing        RuntimeActivityState = "closing"
)

type RuntimeActivityActiveKind string

const (
	RuntimeActivityActiveKindUserTurn     RuntimeActivityActiveKind = "user_turn"
	RuntimeActivityActiveKindWorkflowTurn RuntimeActivityActiveKind = "workflow_turn"
	RuntimeActivityActiveKindGoalLoop     RuntimeActivityActiveKind = "goal_loop"
	RuntimeActivityActiveKindCompaction   RuntimeActivityActiveKind = "compaction"
)

func (k RuntimeActivityActiveKind) Validate() error {
	switch k {
	case RuntimeActivityActiveKindUserTurn,
		RuntimeActivityActiveKindWorkflowTurn,
		RuntimeActivityActiveKindGoalLoop,
		RuntimeActivityActiveKindCompaction:
		return nil
	default:
		return fmt.Errorf("unknown runtime activity active kind %q", k)
	}
}
