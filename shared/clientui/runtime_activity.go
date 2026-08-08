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

type RuntimeOperationKind string

const (
	RuntimeOperationKindSubmit           RuntimeOperationKind = "submit"
	RuntimeOperationKindQueuedMessage    RuntimeOperationKind = "queued_message"
	RuntimeOperationKindUserShell        RuntimeOperationKind = "user_shell"
	RuntimeOperationKindCompact          RuntimeOperationKind = "compact"
	RuntimeOperationKindPreSubmitCompact RuntimeOperationKind = "pre_submit_compact"
	RuntimeOperationKindSubmitQueued     RuntimeOperationKind = "submit_queued"
)

func (k RuntimeOperationKind) Validate() error {
	switch k {
	case RuntimeOperationKindSubmit,
		RuntimeOperationKindQueuedMessage,
		RuntimeOperationKindUserShell,
		RuntimeOperationKindCompact,
		RuntimeOperationKindPreSubmitCompact,
		RuntimeOperationKindSubmitQueued:
		return nil
	default:
		return fmt.Errorf("unknown runtime operation kind %q", k)
	}
}

func (r RuntimeOperationRef) Key() string {
	if err := r.Validate(); err != nil {
		return ""
	}
	if r.Kind == RuntimeOperationKindQueuedMessage {
		if r.QueueItemID != nil {
			return string(r.Kind) + ":queue_item:" + r.QueueItemID.String()
		}
		return string(r.Kind) + ":client_request:" + r.ClientRequestID.String()
	}
	return string(r.Kind) + ":" + r.ClientRequestID.String()
}

type RuntimeInputReconciliationState string

const (
	RuntimeInputReconciliationCommitted            RuntimeInputReconciliationState = "committed"
	RuntimeInputReconciliationAccepted             RuntimeInputReconciliationState = "accepted"
	RuntimeInputReconciliationSubmitted            RuntimeInputReconciliationState = "submitted"
	RuntimeInputReconciliationCanceledNotCommitted RuntimeInputReconciliationState = "canceled_not_committed"
	RuntimeInputReconciliationFailedWithRestore    RuntimeInputReconciliationState = "failed_with_restore"
	RuntimeInputReconciliationUnknown              RuntimeInputReconciliationState = "unknown"
	RuntimeInputReconciliationEvicted              RuntimeInputReconciliationState = "evicted"
)

func (s RuntimeInputReconciliationState) Validate() error {
	switch s {
	case RuntimeInputReconciliationCommitted,
		RuntimeInputReconciliationAccepted,
		RuntimeInputReconciliationSubmitted,
		RuntimeInputReconciliationCanceledNotCommitted,
		RuntimeInputReconciliationFailedWithRestore,
		RuntimeInputReconciliationUnknown,
		RuntimeInputReconciliationEvicted:
		return nil
	default:
		return fmt.Errorf("unknown runtime input reconciliation state %q", s)
	}
}

func (r RuntimeInputReconciliation) RestoreRecommended() bool {
	return r.State == RuntimeInputReconciliationCanceledNotCommitted || r.State == RuntimeInputReconciliationFailedWithRestore
}

func (r RuntimeInputReconciliation) Ambiguous() bool {
	return r.State == RuntimeInputReconciliationUnknown || r.State == RuntimeInputReconciliationEvicted
}

type RuntimeSubmitRequest struct {
	OperationRef                    RuntimeOperationRef
	PreSubmitCompactionOperationRef RuntimeOperationRef
	Input                           runtimeinput.Input
}

func (r RuntimeSubmitRequest) Validate() error {
	if err := validateOperationRefKind(r.OperationRef, RuntimeOperationKindSubmit); err != nil {
		return err
	}
	if err := validateOperationRefKind(r.PreSubmitCompactionOperationRef, RuntimeOperationKindPreSubmitCompact); err != nil {
		return err
	}
	return r.Input.Validate()
}

type RuntimeShellRequest struct {
	OperationRef RuntimeOperationRef
	Command      string
}

func (r RuntimeShellRequest) Validate() error {
	if err := validateOperationRefKind(r.OperationRef, RuntimeOperationKindUserShell); err != nil {
		return err
	}
	if strings.TrimSpace(r.Command) == "" {
		return fmt.Errorf("shell command is required")
	}
	return nil
}

type RuntimeCompactRequest struct {
	OperationRef RuntimeOperationRef
	Args         string
}

func (r RuntimeCompactRequest) Validate() error {
	return validateOperationRefKind(r.OperationRef, RuntimeOperationKindCompact)
}

type RuntimeSubmitQueuedRequest struct {
	OperationRef RuntimeOperationRef
}

func (r RuntimeSubmitQueuedRequest) Validate() error {
	return validateOperationRefKind(r.OperationRef, RuntimeOperationKindSubmitQueued)
}

func validateOperationRefKind(ref RuntimeOperationRef, kind RuntimeOperationKind) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Kind != kind {
		return fmt.Errorf("runtime operation ref kind = %q, want %q", ref.Kind, kind)
	}
	return nil
}
