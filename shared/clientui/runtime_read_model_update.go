package clientui

import (
	"fmt"

	"core/shared/runtimeids"
)

type RuntimeReadModelUpdate struct {
	Version             ReadModelVersion
	Activity            RuntimeActivity
	InputReconciliation RuntimeInputReconciliationSnapshot
}

type RuntimeActivity struct {
	State              RuntimeActivityState
	ActiveStep         *RuntimeActiveStep
	QueueAccepting     bool
	DiagnosticRecovery bool
}

type RuntimeActiveStep struct {
	RunID      runtimeids.RunID
	StepID     runtimeids.StepID
	ActiveKind RuntimeActivityActiveKind
}

type RuntimeOperationRef struct {
	Kind            RuntimeOperationKind
	ClientRequestID runtimeids.RuntimeClientRequestID
	QueueItemID     *runtimeids.QueueItemID
}

type RuntimeInputReconciliationSnapshot struct {
	Operations []RuntimeInputReconciliation
}

type RuntimeInputReconciliation struct {
	Operation RuntimeOperationRef
	State     RuntimeInputReconciliationState
}

func (u RuntimeReadModelUpdate) Validate() error {
	if err := u.Version.Validate(); err != nil {
		return fmt.Errorf("validate runtime read-model version: %w", err)
	}
	if err := u.Activity.Validate(); err != nil {
		return fmt.Errorf("validate runtime activity: %w", err)
	}
	if err := u.InputReconciliation.Validate(); err != nil {
		return fmt.Errorf("validate runtime input reconciliation: %w", err)
	}
	return nil
}

func (a RuntimeActivity) Validate() error {
	switch a.State {
	case RuntimeActivityUnavailable,
		RuntimeActivityStarting,
		RuntimeActivityDraining,
		RuntimeActivityClosing:
		if a.QueueAccepting {
			return fmt.Errorf("%s runtime activity cannot accept queue work", a.State)
		}
		if a.ActiveStep != nil {
			return fmt.Errorf("%s runtime activity cannot carry active step", a.State)
		}
		return nil
	case RuntimeActivityRegisteredIdle:
		if a.ActiveStep != nil {
			return fmt.Errorf("registered-idle runtime activity cannot carry active step")
		}
		return nil
	case RuntimeActivityRunning, RuntimeActivityAwaitingPrompt:
		if a.ActiveStep == nil {
			return fmt.Errorf("%s runtime activity requires active step", a.State)
		}
		return a.ActiveStep.Validate()
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

func (s RuntimeActiveStep) Validate() error {
	if s.RunID.IsZero() {
		return fmt.Errorf("runtime active step requires run id")
	}
	if s.StepID.IsZero() {
		return fmt.Errorf("runtime active step requires step id")
	}
	if err := s.ActiveKind.Validate(); err != nil {
		return err
	}
	return nil
}

func (r RuntimeOperationRef) Validate() error {
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if r.ClientRequestID.IsZero() {
		return fmt.Errorf("runtime operation requires client request id")
	}
	if r.Kind != RuntimeOperationKindQueuedMessage && r.QueueItemID != nil {
		return fmt.Errorf("%s runtime operation cannot carry queue item id", r.Kind)
	}
	if r.QueueItemID != nil && r.QueueItemID.IsZero() {
		return fmt.Errorf("runtime queued-message operation queue item id is invalid")
	}
	return nil
}

func (s RuntimeInputReconciliationSnapshot) Validate() error {
	seen := make(map[runtimeids.RuntimeClientRequestID]struct{}, len(s.Operations))
	for index, operation := range s.Operations {
		if err := operation.Validate(); err != nil {
			return fmt.Errorf("validate runtime input reconciliation operation %d: %w", index, err)
		}
		if _, exists := seen[operation.Operation.ClientRequestID]; exists {
			return fmt.Errorf("runtime input reconciliation repeats client request id %q", operation.Operation.ClientRequestID.String())
		}
		seen[operation.Operation.ClientRequestID] = struct{}{}
	}
	return nil
}

func (r RuntimeInputReconciliation) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return fmt.Errorf("validate runtime operation identity: %w", err)
	}
	if err := r.State.Validate(); err != nil {
		return fmt.Errorf("validate runtime input reconciliation state: %w", err)
	}
	return nil
}
