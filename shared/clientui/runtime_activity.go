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
	RuntimeActivityActiveKindUserTurn            RuntimeActivityActiveKind = "user_turn"
	RuntimeActivityActiveKindWorkflowTurn        RuntimeActivityActiveKind = "workflow_turn"
	RuntimeActivityActiveKindGoalLoop            RuntimeActivityActiveKind = "goal_loop"
	RuntimeActivityActiveKindCompaction          RuntimeActivityActiveKind = "compaction"
	RuntimeActivityActiveKindPreSubmitCompaction RuntimeActivityActiveKind = "pre_submit_compaction"
	RuntimeActivityActiveKindUserShell           RuntimeActivityActiveKind = "user_shell"
	RuntimeActivityActiveKindBackground          RuntimeActivityActiveKind = "background"
	RuntimeActivityActiveKindRuntimeMaintenance  RuntimeActivityActiveKind = "runtime_maintenance"
)

type RuntimeActivity struct {
	State              RuntimeActivityState
	ActiveKind         RuntimeActivityActiveKind
	RunID              string
	StepID             string
	QueueAccepting     bool
	DiagnosticRecovery bool
}

type RuntimeActivityOptions struct {
	ActiveKind         RuntimeActivityActiveKind
	RunID              string
	StepID             string
	QueueAccepting     bool
	DiagnosticRecovery bool
}

func NewRuntimeActivity(state RuntimeActivityState, options RuntimeActivityOptions) (RuntimeActivity, error) {
	activity := RuntimeActivity{
		State:              state,
		ActiveKind:         options.ActiveKind,
		RunID:              strings.TrimSpace(options.RunID),
		StepID:             strings.TrimSpace(options.StepID),
		QueueAccepting:     options.QueueAccepting,
		DiagnosticRecovery: options.DiagnosticRecovery,
	}
	if err := activity.Validate(); err != nil {
		return RuntimeActivity{}, err
	}
	return activity, nil
}

func MustRuntimeActivity(state RuntimeActivityState, options RuntimeActivityOptions) RuntimeActivity {
	activity, err := NewRuntimeActivity(state, options)
	if err != nil {
		panic(err)
	}
	return activity
}

func (a RuntimeActivity) Validate() error {
	switch a.State {
	case RuntimeActivityUnavailable:
		if a.QueueAccepting {
			return fmt.Errorf("unavailable runtime activity cannot accept queue work")
		}
		return validateNoActiveStep(a)
	case RuntimeActivityRegisteredIdle:
		return validateNoActiveStep(a)
	case RuntimeActivityStarting:
		if a.QueueAccepting {
			return fmt.Errorf("starting runtime activity cannot accept queue work")
		}
		return validateNoActiveStep(a)
	case RuntimeActivityRunning, RuntimeActivityAwaitingPrompt:
		if err := a.ActiveKind.Validate(); err != nil {
			return err
		}
		if strings.TrimSpace(a.RunID) == "" {
			return fmt.Errorf("%s runtime activity requires run id", a.State)
		}
		if strings.TrimSpace(a.StepID) == "" {
			return fmt.Errorf("%s runtime activity requires step id", a.State)
		}
		return nil
	case RuntimeActivityDraining, RuntimeActivityClosing:
		if a.QueueAccepting {
			return fmt.Errorf("%s runtime activity cannot accept queue work", a.State)
		}
		if a.ActiveKind != "" {
			if err := a.ActiveKind.Validate(); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown runtime activity state %q", a.State)
	}
}

func (a RuntimeActivity) ActiveForControl() bool {
	switch a.State {
	case RuntimeActivityStarting, RuntimeActivityRunning, RuntimeActivityAwaitingPrompt, RuntimeActivityDraining, RuntimeActivityClosing:
		return true
	default:
		return false
	}
}

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

func validateNoActiveStep(activity RuntimeActivity) error {
	if activity.ActiveKind != "" {
		return fmt.Errorf("%s runtime activity cannot carry active kind", activity.State)
	}
	if strings.TrimSpace(activity.RunID) != "" {
		return fmt.Errorf("%s runtime activity cannot carry run id", activity.State)
	}
	if strings.TrimSpace(activity.StepID) != "" {
		return fmt.Errorf("%s runtime activity cannot carry step id", activity.State)
	}
	return nil
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

type RuntimeOperationRef struct {
	Kind            RuntimeOperationKind
	ClientRequestID string
	QueueItemID     string
}

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

func (r RuntimeOperationRef) Validate() error {
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	clientRequestID := strings.TrimSpace(r.ClientRequestID)
	queueItemID := strings.TrimSpace(r.QueueItemID)
	if r.Kind == RuntimeOperationKindQueuedMessage {
		switch {
		case queueItemID == "" && clientRequestID == "":
			return fmt.Errorf("queued-message runtime operation ref requires client request id or queue item id")
		case queueItemID != "" && clientRequestID != "":
			return fmt.Errorf("queued-message runtime operation ref cannot carry both client request id and queue item id")
		}
		return nil
	}
	if clientRequestID == "" {
		return fmt.Errorf("runtime operation ref client request id is required")
	}
	if queueItemID != "" {
		return fmt.Errorf("%s runtime operation ref cannot carry queue item id", r.Kind)
	}
	return nil
}

func (r RuntimeOperationRef) Key() string {
	if err := r.Validate(); err != nil {
		return ""
	}
	if r.Kind == RuntimeOperationKindQueuedMessage {
		if clientRequestID := strings.TrimSpace(r.ClientRequestID); clientRequestID != "" {
			return string(r.Kind) + ":client_request:" + clientRequestID
		}
		return string(r.Kind) + ":queue_item:" + strings.TrimSpace(r.QueueItemID)
	}
	return string(r.Kind) + ":" + strings.TrimSpace(r.ClientRequestID)
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

type RuntimeInputReconciliation struct {
	Version      ReadModelVersion
	OperationRef RuntimeOperationRef
	State        RuntimeInputReconciliationState
}

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

func (r RuntimeInputReconciliation) Validate() error {
	if err := r.Version.Validate(); err != nil {
		return err
	}
	if err := r.OperationRef.Validate(); err != nil {
		return err
	}
	return r.State.Validate()
}

func (r RuntimeInputReconciliation) RestoreRecommended() bool {
	return r.State == RuntimeInputReconciliationCanceledNotCommitted || r.State == RuntimeInputReconciliationFailedWithRestore
}

func (r RuntimeInputReconciliation) Ambiguous() bool {
	return r.State == RuntimeInputReconciliationUnknown || r.State == RuntimeInputReconciliationEvicted
}

type RuntimeInputReconciliationSnapshot struct {
	Version    ReadModelVersion
	Operations []RuntimeInputReconciliation
}

func NewEmptyRuntimeInputReconciliationSnapshot(version ReadModelVersion) RuntimeInputReconciliationSnapshot {
	return RuntimeInputReconciliationSnapshot{Version: version}
}

func NewUnknownRuntimeInputReconciliationSnapshot(version ReadModelVersion, refs []RuntimeOperationRef) RuntimeInputReconciliationSnapshot {
	operations := make([]RuntimeInputReconciliation, 0, len(refs))
	for _, ref := range refs {
		operations = append(operations, RuntimeInputReconciliation{
			Version:      version,
			OperationRef: ref,
			State:        RuntimeInputReconciliationUnknown,
		})
	}
	return RuntimeInputReconciliationSnapshot{Version: version, Operations: operations}
}

type RuntimeSubmitRequest struct {
	OperationRef                    RuntimeOperationRef
	PreSubmitCompactionOperationRef RuntimeOperationRef
	Text                            string
}

func (r RuntimeSubmitRequest) Validate() error {
	if err := validateOperationRefKind(r.OperationRef, RuntimeOperationKindSubmit); err != nil {
		return err
	}
	if !isZeroRuntimeOperationRef(r.PreSubmitCompactionOperationRef) {
		if err := validateOperationRefKind(r.PreSubmitCompactionOperationRef, RuntimeOperationKindPreSubmitCompact); err != nil {
			return err
		}
	}
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("submit text is required")
	}
	return nil
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

type RuntimePreSubmitCompactRequest struct {
	OperationRef RuntimeOperationRef
}

func (r RuntimePreSubmitCompactRequest) Validate() error {
	return validateOperationRefKind(r.OperationRef, RuntimeOperationKindPreSubmitCompact)
}

type RuntimeSubmitQueuedRequest struct {
	OperationRef RuntimeOperationRef
}

func (r RuntimeSubmitQueuedRequest) Validate() error {
	return validateOperationRefKind(r.OperationRef, RuntimeOperationKindSubmitQueued)
}

type RuntimeQueueUserMessageRequest struct {
	OperationRef RuntimeOperationRef
	Text         string
}

func (r RuntimeQueueUserMessageRequest) Validate() error {
	if err := validateOperationRefKind(r.OperationRef, RuntimeOperationKindQueuedMessage); err != nil {
		return err
	}
	if strings.TrimSpace(r.OperationRef.QueueItemID) != "" {
		return fmt.Errorf("queued-message create request operation ref must use client request id before server queue item id exists")
	}
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("queued message text is required")
	}
	return nil
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

func isZeroRuntimeOperationRef(ref RuntimeOperationRef) bool {
	return ref.Kind == "" && strings.TrimSpace(ref.ClientRequestID) == "" && strings.TrimSpace(ref.QueueItemID) == ""
}
