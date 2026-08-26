package worktreecontract

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/workflowcontract"
)

type RetainedPreviousWorktree struct {
	Worktree TopologyEntry
}

func (r RetainedPreviousWorktree) Validate() error {
	if r.Worktree.Variant != TopologyVariantRegistered {
		return errors.New("retained previous worktree must be registered")
	}
	return r.Worktree.Validate()
}

type SetupPhase string

const (
	SetupPhaseStarted     SetupPhase = "started"
	SetupPhaseCompleted   SetupPhase = "completed"
	SetupPhaseNotRequired SetupPhase = "not_required"
	SetupPhaseFailed      SetupPhase = "failed"
)

type SetupNotRequiredReason string

const (
	SetupNotRequiredNoTargetPreparation SetupNotRequiredReason = "no_target_preparation"
	SetupNotRequiredNoConfiguredScript  SetupNotRequiredReason = "no_configured_script"
)

type SetupRetryReadiness string

const (
	SetupRetryReady   SetupRetryReadiness = "retry_ready"
	SetupNonRetryable SetupRetryReadiness = "non_retryable"
)

type SetupStarted struct {
	SourceWorkspaceRoot string
	WorktreeRoot        string
	ScriptPath          string
}

type SetupCompleted struct {
	RetainedPreviousWorktree *RetainedPreviousWorktree
}

type SetupNotRequired struct {
	Reason                   SetupNotRequiredReason
	RetainedPreviousWorktree *RetainedPreviousWorktree
}

type SetupProcessExit struct {
	ExitCode int
	Stdout   *string
	Stderr   *string
}

type SetupTimeout struct {
	Stdout *string
	Stderr *string
}

type SetupPreparationFailure struct{}
type SetupInterruptionPersistenceFailure struct{}
type SetupCanceled struct{}
type SetupControllerShutdown struct{}
type SetupOperationalFailure struct{}

type SetupFailureCause struct {
	Kind                    SetupFailureKind
	ProcessExit             *SetupProcessExit
	Timeout                 *SetupTimeout
	Preparation             *SetupPreparationFailure
	InterruptionPersistence *SetupInterruptionPersistenceFailure
	Canceled                *SetupCanceled
	ControllerShutdown      *SetupControllerShutdown
	Operational             *SetupOperationalFailure
}

type SetupFailed struct {
	RetryReadiness           SetupRetryReadiness
	Cause                    SetupFailureCause
	Diagnostic               string
	ScriptPath               *string
	ExecutionTarget          *workflowcontract.ExecutionTargetSelection
	RetainedWorktree         *TopologyEntry
	RetainedPreviousWorktree *RetainedPreviousWorktree
}

type SetupEvent struct {
	SetupOperationID SetupOperationID
	Phase            SetupPhase
	Started          *SetupStarted
	Completed        *SetupCompleted
	NotRequired      *SetupNotRequired
	Failed           *SetupFailed
}

func (e SetupEvent) Validate() error {
	if err := e.SetupOperationID.Validate(); err != nil {
		return err
	}
	payloadCount := 0
	if e.Started != nil {
		payloadCount++
	}
	if e.Completed != nil {
		payloadCount++
	}
	if e.NotRequired != nil {
		payloadCount++
	}
	if e.Failed != nil {
		payloadCount++
	}
	if payloadCount != 1 {
		return errors.New("worktree setup event requires exactly one phase payload")
	}
	switch e.Phase {
	case SetupPhaseStarted:
		if e.Started == nil {
			return errors.New("started setup event requires started payload")
		}
		return e.Started.Validate()
	case SetupPhaseCompleted:
		if e.Completed == nil {
			return errors.New("completed setup event requires completed payload")
		}
		return e.Completed.Validate()
	case SetupPhaseNotRequired:
		if e.NotRequired == nil {
			return errors.New("not_required setup event requires not_required payload")
		}
		return e.NotRequired.Validate()
	case SetupPhaseFailed:
		if e.Failed == nil {
			return errors.New("failed setup event requires failed payload")
		}
		return e.Failed.Validate()
	default:
		return errors.New("setup phase is invalid")
	}
}

func (p SetupStarted) Validate() error {
	if strings.TrimSpace(p.SourceWorkspaceRoot) == "" {
		return errors.New("started setup event source_workspace_root is required")
	}
	if strings.TrimSpace(p.WorktreeRoot) == "" {
		return errors.New("started setup event worktree_root is required")
	}
	if strings.TrimSpace(p.ScriptPath) == "" {
		return errors.New("started setup event script_path is required")
	}
	return nil
}

func (p SetupCompleted) Validate() error {
	return validateRetainedPreviousWorktree(p.RetainedPreviousWorktree)
}

func (p SetupNotRequired) Validate() error {
	switch p.Reason {
	case SetupNotRequiredNoTargetPreparation, SetupNotRequiredNoConfiguredScript:
		return validateRetainedPreviousWorktree(p.RetainedPreviousWorktree)
	default:
		return errors.New("not_required setup event reason is invalid")
	}
}

func (p SetupFailed) Validate() error {
	switch p.RetryReadiness {
	case SetupRetryReady, SetupNonRetryable:
	default:
		return errors.New("failed setup event retry_readiness is invalid")
	}
	if err := p.Cause.Validate(); err != nil {
		return err
	}
	switch {
	case HasFixedRetryReadiness(p.Cause.Kind) &&
		IsRetryReadySetupFailure(p.Cause.Kind):
		if p.RetryReadiness != SetupRetryReady {
			return errors.New("retryable setup failure cause requires retry_ready")
		}
	case HasFixedRetryReadiness(p.Cause.Kind) &&
		IsNonRetryableSetupFailure(p.Cause.Kind):
		if p.RetryReadiness != SetupNonRetryable {
			return errors.New("non-retryable setup failure cause requires non_retryable")
		}
	}
	if strings.TrimSpace(p.Diagnostic) == "" {
		return errors.New("failed setup event diagnostic is required")
	}
	if p.ScriptPath != nil && strings.TrimSpace(*p.ScriptPath) == "" {
		return errors.New("failed setup event script_path must be non-blank when present")
	}
	if p.Cause.Kind == SetupFailureTargetPreparation && p.ScriptPath != nil {
		return errors.New("target-preparation setup failure cannot include script_path")
	}
	if p.RetryReadiness == SetupRetryReady &&
		p.Cause.Kind != SetupFailureTargetPreparation &&
		p.ScriptPath == nil {
		return errors.New("retry-ready setup-script failure requires script_path")
	}
	if p.ExecutionTarget != nil {
		if err := p.ExecutionTarget.Validate(); err != nil {
			return fmt.Errorf("failed setup event execution target: %w", err)
		}
	}
	if p.RetryReadiness == SetupRetryReady &&
		p.Cause.Kind != SetupFailureTargetPreparation &&
		p.RetainedWorktree == nil {
		return errors.New("retry-ready setup failure requires retained_worktree")
	}
	if p.RetainedWorktree != nil {
		if p.RetainedWorktree.Variant != TopologyVariantRegistered {
			return errors.New("failed setup event retained worktree must be registered")
		}
		if err := p.RetainedWorktree.Validate(); err != nil {
			return err
		}
	}
	return validateRetainedPreviousWorktree(p.RetainedPreviousWorktree)
}

func (c SetupFailureCause) Validate() error {
	payloadCount := 0
	if c.ProcessExit != nil {
		payloadCount++
	}
	if c.Timeout != nil {
		payloadCount++
	}
	if c.Preparation != nil {
		payloadCount++
	}
	if c.InterruptionPersistence != nil {
		payloadCount++
	}
	if c.Canceled != nil {
		payloadCount++
	}
	if c.ControllerShutdown != nil {
		payloadCount++
	}
	if c.Operational != nil {
		payloadCount++
	}
	if payloadCount != 1 {
		return errors.New("worktree setup failure cause requires exactly one matching payload")
	}
	switch c.Kind {
	case SetupFailureProcessExit:
		if c.ProcessExit == nil {
			return errors.New("process_exit failure requires process_exit payload")
		}
		if c.ProcessExit.ExitCode == 0 {
			return errors.New("process_exit failure exit_code must be non-zero")
		}
	case SetupFailureTimeout:
		if c.Timeout == nil {
			return errors.New("timeout failure requires timeout payload")
		}
	case SetupFailureTargetPreparation:
		if c.Preparation == nil {
			return errors.New("target_preparation failure requires target_preparation payload")
		}
	case SetupFailureInterruptionPersistence:
		if c.InterruptionPersistence == nil {
			return errors.New("interruption_persistence failure requires interruption_persistence payload")
		}
	case SetupFailureCanceled:
		if c.Canceled == nil {
			return errors.New("canceled failure requires canceled payload")
		}
	case SetupFailureControllerShutdown:
		if c.ControllerShutdown == nil {
			return errors.New("controller_shutdown failure requires controller_shutdown payload")
		}
	case SetupFailureOperational:
		if c.Operational == nil {
			return errors.New("operational failure requires operational payload")
		}
	default:
		return errors.New("worktree setup failure kind is invalid")
	}
	return nil
}

func validateRetainedPreviousWorktree(retained *RetainedPreviousWorktree) error {
	if retained == nil {
		return nil
	}
	return retained.Validate()
}

type SetupSubscribeRequest struct {
	SetupOperationID SetupOperationID
}

func (r SetupSubscribeRequest) Validate() error {
	return r.SetupOperationID.Validate()
}

type SetupSubscription interface {
	Next(context.Context) (SetupEvent, error)
	Close() error
}
