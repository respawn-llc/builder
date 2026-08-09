package serverapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/worktreecontract"
	"github.com/google/uuid"
)

type WorktreeSetupOperationID uuid.UUID

func NewWorktreeSetupOperationID() WorktreeSetupOperationID {
	return WorktreeSetupOperationID(uuid.New())
}

func ParseWorktreeSetupOperationID(value string) (WorktreeSetupOperationID, error) {
	parsed, err := parseWorktreeUUIDV4(value, "setup_operation_id")
	if err != nil {
		return WorktreeSetupOperationID{}, err
	}
	return WorktreeSetupOperationID(parsed), nil
}

func (id WorktreeSetupOperationID) String() string {
	return uuid.UUID(id).String()
}

func (id WorktreeSetupOperationID) Validate() error {
	return validateWorktreeUUIDV4(uuid.UUID(id), "setup_operation_id")
}

func (id WorktreeSetupOperationID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(id.String())
}

func (id *WorktreeSetupOperationID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseWorktreeSetupOperationID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

type RetainedPreviousWorktree struct {
	Worktree WorktreeTopologyEntry `json:"worktree"`
}

func (r RetainedPreviousWorktree) Validate() error {
	if r.Worktree.Variant != WorktreeTopologyVariantRegistered {
		return errors.New("retained previous worktree must be registered")
	}
	return r.Worktree.Validate()
}

type WorktreeSetupPhase string

const (
	WorktreeSetupPhaseStarted     WorktreeSetupPhase = "started"
	WorktreeSetupPhaseCompleted   WorktreeSetupPhase = "completed"
	WorktreeSetupPhaseNotRequired WorktreeSetupPhase = "not_required"
	WorktreeSetupPhaseFailed      WorktreeSetupPhase = "failed"
)

type WorktreeSetupNotRequiredReason string

const (
	WorktreeSetupNotRequiredNoTargetPreparation WorktreeSetupNotRequiredReason = "no_target_preparation"
	WorktreeSetupNotRequiredNoConfiguredScript  WorktreeSetupNotRequiredReason = "no_configured_script"
)

type WorktreeSetupRetryReadiness string

const (
	WorktreeSetupRetryReady   WorktreeSetupRetryReadiness = "retry_ready"
	WorktreeSetupNonRetryable WorktreeSetupRetryReadiness = "non_retryable"
)

type WorktreeSetupFailureKind = worktreecontract.SetupFailureKind

const (
	WorktreeSetupFailureProcessExit             = worktreecontract.SetupFailureProcessExit
	WorktreeSetupFailureTimeout                 = worktreecontract.SetupFailureTimeout
	WorktreeSetupFailureTargetPreparation       = worktreecontract.SetupFailureTargetPreparation
	WorktreeSetupFailureInterruptionPersistence = worktreecontract.SetupFailureInterruptionPersistence
	WorktreeSetupFailureCanceled                = worktreecontract.SetupFailureCanceled
	WorktreeSetupFailureControllerShutdown      = worktreecontract.SetupFailureControllerShutdown
	WorktreeSetupFailureOperational             = worktreecontract.SetupFailureOperational
)

type WorktreeSetupStarted struct {
	SourceWorkspaceRoot string `json:"source_workspace_root"`
	WorktreeRoot        string `json:"worktree_root"`
	ScriptPath          string `json:"script_path"`
}

type WorktreeSetupCompleted struct {
	RetainedPreviousWorktree *RetainedPreviousWorktree `json:"retained_previous_worktree"`
}

type WorktreeSetupNotRequired struct {
	Reason                   WorktreeSetupNotRequiredReason `json:"reason"`
	RetainedPreviousWorktree *RetainedPreviousWorktree      `json:"retained_previous_worktree"`
}

type WorktreeSetupProcessExit struct {
	ExitCode int     `json:"exit_code"`
	Stdout   *string `json:"stdout,omitempty"`
	Stderr   *string `json:"stderr,omitempty"`
}

type WorktreeSetupTimeout struct {
	Stdout *string `json:"stdout,omitempty"`
	Stderr *string `json:"stderr,omitempty"`
}

type WorktreeSetupPreparationFailure struct{}
type WorktreeSetupInterruptionPersistenceFailure struct{}
type WorktreeSetupCanceled struct{}
type WorktreeSetupControllerShutdown struct{}
type WorktreeSetupOperationalFailure struct{}

type WorktreeSetupFailureCause struct {
	Kind                    WorktreeSetupFailureKind                     `json:"kind"`
	ProcessExit             *WorktreeSetupProcessExit                    `json:"process_exit,omitempty"`
	Timeout                 *WorktreeSetupTimeout                        `json:"timeout,omitempty"`
	Preparation             *WorktreeSetupPreparationFailure             `json:"target_preparation,omitempty"`
	InterruptionPersistence *WorktreeSetupInterruptionPersistenceFailure `json:"interruption_persistence,omitempty"`
	Canceled                *WorktreeSetupCanceled                       `json:"canceled,omitempty"`
	ControllerShutdown      *WorktreeSetupControllerShutdown             `json:"controller_shutdown,omitempty"`
	Operational             *WorktreeSetupOperationalFailure             `json:"operational,omitempty"`
}

type WorktreeSetupFailed struct {
	RetryReadiness           WorktreeSetupRetryReadiness       `json:"retry_readiness"`
	Cause                    WorktreeSetupFailureCause         `json:"cause"`
	Diagnostic               string                            `json:"diagnostic"`
	ScriptPath               *string                           `json:"script_path"`
	ExecutionTarget          *WorkflowExecutionTargetSelection `json:"execution_target,omitempty"`
	RetainedWorktree         *WorktreeTopologyEntry            `json:"retained_worktree,omitempty"`
	RetainedPreviousWorktree *RetainedPreviousWorktree         `json:"retained_previous_worktree,omitempty"`
}

type WorktreeSetupEvent struct {
	SetupOperationID WorktreeSetupOperationID  `json:"setup_operation_id"`
	Phase            WorktreeSetupPhase        `json:"phase"`
	Started          *WorktreeSetupStarted     `json:"started,omitempty"`
	Completed        *WorktreeSetupCompleted   `json:"completed,omitempty"`
	NotRequired      *WorktreeSetupNotRequired `json:"not_required,omitempty"`
	Failed           *WorktreeSetupFailed      `json:"failed,omitempty"`
}

func (e WorktreeSetupEvent) Validate() error {
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
	case WorktreeSetupPhaseStarted:
		if e.Started == nil {
			return errors.New("started setup event requires started payload")
		}
		return e.Started.Validate()
	case WorktreeSetupPhaseCompleted:
		if e.Completed == nil {
			return errors.New("completed setup event requires completed payload")
		}
		return e.Completed.Validate()
	case WorktreeSetupPhaseNotRequired:
		if e.NotRequired == nil {
			return errors.New("not_required setup event requires not_required payload")
		}
		return e.NotRequired.Validate()
	case WorktreeSetupPhaseFailed:
		if e.Failed == nil {
			return errors.New("failed setup event requires failed payload")
		}
		return e.Failed.Validate()
	default:
		return errors.New("setup phase is invalid")
	}
}

func (p WorktreeSetupStarted) Validate() error {
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

func (p WorktreeSetupCompleted) Validate() error {
	return validateRetainedPreviousWorktree(p.RetainedPreviousWorktree)
}

func (p WorktreeSetupNotRequired) Validate() error {
	switch p.Reason {
	case WorktreeSetupNotRequiredNoTargetPreparation, WorktreeSetupNotRequiredNoConfiguredScript:
		return validateRetainedPreviousWorktree(p.RetainedPreviousWorktree)
	default:
		return errors.New("not_required setup event reason is invalid")
	}
}

func (p WorktreeSetupFailed) Validate() error {
	switch p.RetryReadiness {
	case WorktreeSetupRetryReady, WorktreeSetupNonRetryable:
	default:
		return errors.New("failed setup event retry_readiness is invalid")
	}
	if err := p.Cause.Validate(); err != nil {
		return err
	}
	switch {
	case worktreecontract.HasFixedRetryReadiness(p.Cause.Kind) &&
		worktreecontract.IsRetryReadySetupFailure(p.Cause.Kind):
		if p.RetryReadiness != WorktreeSetupRetryReady {
			return errors.New("retryable setup failure cause requires retry_ready")
		}
	case worktreecontract.HasFixedRetryReadiness(p.Cause.Kind) &&
		worktreecontract.IsNonRetryableSetupFailure(p.Cause.Kind):
		if p.RetryReadiness != WorktreeSetupNonRetryable {
			return errors.New("non-retryable setup failure cause requires non_retryable")
		}
	}
	if strings.TrimSpace(p.Diagnostic) == "" {
		return errors.New("failed setup event diagnostic is required")
	}
	if p.ScriptPath != nil && strings.TrimSpace(*p.ScriptPath) == "" {
		return errors.New("failed setup event script_path must be non-blank when present")
	}
	if p.Cause.Kind == WorktreeSetupFailureTargetPreparation && p.ScriptPath != nil {
		return errors.New("target-preparation setup failure cannot include script_path")
	}
	if p.RetryReadiness == WorktreeSetupRetryReady &&
		p.Cause.Kind != WorktreeSetupFailureTargetPreparation &&
		p.ScriptPath == nil {
		return errors.New("retry-ready setup-script failure requires script_path")
	}
	if p.ExecutionTarget != nil {
		if err := p.ExecutionTarget.Validate(); err != nil {
			return fmt.Errorf("failed setup event execution target: %w", err)
		}
	}
	if p.RetryReadiness == WorktreeSetupRetryReady &&
		p.Cause.Kind != WorktreeSetupFailureTargetPreparation &&
		p.RetainedWorktree == nil {
		return errors.New("retry-ready setup failure requires retained_worktree")
	}
	if p.RetainedWorktree != nil {
		if p.RetainedWorktree.Variant != WorktreeTopologyVariantRegistered {
			return errors.New("failed setup event retained worktree must be registered")
		}
		if err := p.RetainedWorktree.Validate(); err != nil {
			return err
		}
	}
	return validateRetainedPreviousWorktree(p.RetainedPreviousWorktree)
}

func (c WorktreeSetupFailureCause) Validate() error {
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
	case WorktreeSetupFailureProcessExit:
		if c.ProcessExit == nil {
			return errors.New("process_exit failure requires process_exit payload")
		}
		if c.ProcessExit.ExitCode == 0 {
			return errors.New("process_exit failure exit_code must be non-zero")
		}
	case WorktreeSetupFailureTimeout:
		if c.Timeout == nil {
			return errors.New("timeout failure requires timeout payload")
		}
	case WorktreeSetupFailureTargetPreparation:
		if c.Preparation == nil {
			return errors.New("target_preparation failure requires target_preparation payload")
		}
	case WorktreeSetupFailureInterruptionPersistence:
		if c.InterruptionPersistence == nil {
			return errors.New("interruption_persistence failure requires interruption_persistence payload")
		}
	case WorktreeSetupFailureCanceled:
		if c.Canceled == nil {
			return errors.New("canceled failure requires canceled payload")
		}
	case WorktreeSetupFailureControllerShutdown:
		if c.ControllerShutdown == nil {
			return errors.New("controller_shutdown failure requires controller_shutdown payload")
		}
	case WorktreeSetupFailureOperational:
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

type WorktreeSetupSubscribeRequest struct {
	SetupOperationID WorktreeSetupOperationID `json:"setup_operation_id"`
}

func (r WorktreeSetupSubscribeRequest) Validate() error {
	return r.SetupOperationID.Validate()
}

type WorktreeSetupSubscription interface {
	Next(context.Context) (WorktreeSetupEvent, error)
	Close() error
}
