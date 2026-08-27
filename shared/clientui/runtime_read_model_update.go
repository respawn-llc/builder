package clientui

import (
	"fmt"

	"core/shared/runtimeids"
)

type RuntimeReadModelUpdate struct {
	Version  ReadModelVersion
	Activity RuntimeActivity
}

type RuntimeActivity struct {
	State              RuntimeActivityState
	ActiveStep         *RuntimeActiveStep
	Reviewer           ReviewerActivity
	QueueAccepting     bool
	DiagnosticRecovery bool
}

type RuntimeActiveStep struct {
	RunID      runtimeids.RunID
	StepID     runtimeids.StepID
	ActiveKind RuntimeActivityActiveKind
}

func (u RuntimeReadModelUpdate) Validate() error {
	if err := u.Version.Validate(); err != nil {
		return fmt.Errorf("validate runtime read-model version: %w", err)
	}
	if err := u.Activity.Validate(); err != nil {
		return fmt.Errorf("validate runtime activity: %w", err)
	}
	return nil
}

func (a RuntimeActivity) Validate() error {
	if err := a.Reviewer.Validate(); err != nil {
		return err
	}
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
