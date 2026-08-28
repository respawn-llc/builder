package clientui

import (
	"fmt"
	"strings"

	"core/shared/runtimeinput"
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

type ReviewerActivity string

const (
	ReviewerActivityInactive           ReviewerActivity = "inactive"
	ReviewerActivityInvoking           ReviewerActivity = "invoking"
	ReviewerActivityAddressingFeedback ReviewerActivity = "addressing_feedback"
)

func (a ReviewerActivity) Validate() error {
	switch a {
	case ReviewerActivityInactive, ReviewerActivityInvoking, ReviewerActivityAddressingFeedback:
		return nil
	default:
		return fmt.Errorf("unknown reviewer activity %q", a)
	}
}

type RuntimeActivityActiveKind string

const (
	RuntimeActivityActiveKindUserTurn            RuntimeActivityActiveKind = "user_turn"
	RuntimeActivityActiveKindWorkflowTurn        RuntimeActivityActiveKind = "workflow_turn"
	RuntimeActivityActiveKindGoalLoop            RuntimeActivityActiveKind = "goal_loop"
	RuntimeActivityActiveKindCompaction          RuntimeActivityActiveKind = "compaction"
	RuntimeActivityActiveKindPreSubmitCompaction RuntimeActivityActiveKind = "pre_submit_compaction"
	RuntimeActivityActiveKindUserShell           RuntimeActivityActiveKind = "user_shell"
	RuntimeActivityActiveKindBackground          RuntimeActivityActiveKind = "background"
	RuntimeActivityActiveKindRuntimeMaintenance  RuntimeActivityActiveKind = "runtime_maintenance"
)

func (k RuntimeActivityActiveKind) Validate() error {
	switch k {
	case RuntimeActivityActiveKindUserTurn,
		RuntimeActivityActiveKindWorkflowTurn,
		RuntimeActivityActiveKindGoalLoop,
		RuntimeActivityActiveKindCompaction,
		RuntimeActivityActiveKindPreSubmitCompaction,
		RuntimeActivityActiveKindUserShell,
		RuntimeActivityActiveKindBackground,
		RuntimeActivityActiveKindRuntimeMaintenance:
		return nil
	default:
		return fmt.Errorf("unknown runtime activity active kind %q", k)
	}
}

type RuntimeSubmitRequest struct {
	ClientRequestID string
	Input           runtimeinput.Input
}

func (r RuntimeSubmitRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return fmt.Errorf("runtime submit requires client request id")
	}
	return r.Input.Validate()
}

type RuntimeShellRequest struct {
	Command string
}

func (r RuntimeShellRequest) Validate() error {
	if strings.TrimSpace(r.Command) == "" {
		return fmt.Errorf("shell command is required")
	}
	return nil
}

type RuntimeCompactRequest struct {
	Args string
}

func (r RuntimeCompactRequest) Validate() error {
	return nil
}
