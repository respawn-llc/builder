package worktreecontract

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type DirtyStateKind string
type SetupFailureKind string
type SetupRequirement string
type TransitionKind string

const (
	DirtyStateClean                     DirtyStateKind   = "clean"
	DirtyStateDirty                     DirtyStateKind   = "dirty"
	DirtyStateUnknown                   DirtyStateKind   = "unknown"
	SetupRequirementRequired            SetupRequirement = "required"
	SetupRequirementAlreadyCompleted    SetupRequirement = "already_completed"
	SetupFailureProcessExit             SetupFailureKind = "process_exit"
	SetupFailureTimeout                 SetupFailureKind = "timeout"
	SetupFailureTargetPreparation       SetupFailureKind = "target_preparation"
	SetupFailureInterruptionPersistence SetupFailureKind = "interruption_persistence"
	SetupFailureCanceled                SetupFailureKind = "canceled"
	SetupFailureControllerShutdown      SetupFailureKind = "controller_shutdown"
	SetupFailureOperational             SetupFailureKind = "operational"
	TransitionDelete                    TransitionKind   = "delete"
)

func IsRetryReadySetupFailure(kind SetupFailureKind) bool {
	return kind == SetupFailureProcessExit || kind == SetupFailureTimeout || kind == SetupFailureTargetPreparation || kind == SetupFailureOperational
}
func IsNonRetryableSetupFailure(kind SetupFailureKind) bool {
	return kind == SetupFailureInterruptionPersistence || kind == SetupFailureCanceled || kind == SetupFailureControllerShutdown
}
func HasFixedRetryReadiness(kind SetupFailureKind) bool { return kind != SetupFailureOperational }
func IsValidSetupRequirement(requirement SetupRequirement) bool {
	return requirement == SetupRequirementRequired || requirement == SetupRequirementAlreadyCompleted
}

type SetupRecoveryDetail[OperationID ~[16]byte, ExecutionTarget interface{ Validate() error }] struct {
	SetupOperationID         OperationID       `json:"setup_operation_id"`
	Cause                    SetupFailureKind  `json:"cause"`
	Diagnostic               string            `json:"diagnostic"`
	ScriptPath               *string           `json:"script_path"`
	SetupRequirement         SetupRequirement  `json:"setup_requirement"`
	ExecutionTarget          ExecutionTarget   `json:"execution_target"`
	RetainedWorktree         *RetainedWorktree `json:"retained_worktree"`
	RetainedPreviousWorktree *RetainedWorktree `json:"retained_previous_worktree"`
}

type RetainedWorktree struct {
	WorktreeID string `json:"worktree_id"`
	Root       string `json:"root"`
}

func (d SetupRecoveryDetail[OperationID, ExecutionTarget]) Validate() error {
	setupOperationID := uuid.UUID(d.SetupOperationID)
	switch {
	case setupOperationID == uuid.Nil || setupOperationID.Version() != 4 || setupOperationID.Variant() != uuid.RFC4122:
		return errors.New("setup recovery setup_operation_id must be a UUID v4")
	case strings.TrimSpace(d.Diagnostic) == "":
		return errors.New("setup recovery diagnostic is required")
	case !IsRetryReadySetupFailure(d.Cause):
		return errors.New("setup recovery cause must be retry-ready")
	case !IsValidSetupRequirement(d.SetupRequirement):
		return errors.New("setup recovery setup_requirement is invalid")
	case d.ScriptPath != nil && strings.TrimSpace(*d.ScriptPath) == "":
		return errors.New("setup recovery script_path must be non-blank when present")
	case d.Cause == SetupFailureTargetPreparation && d.ScriptPath != nil:
		return errors.New("target-preparation setup recovery cannot include script_path")
	case d.Cause != SetupFailureTargetPreparation && d.ScriptPath == nil:
		return errors.New("setup recovery script_path is required for setup-script failure")
	case d.Cause != SetupFailureTargetPreparation && d.SetupRequirement != SetupRequirementRequired:
		return errors.New("setup-script failure requires setup")
	}
	if err := d.ExecutionTarget.Validate(); err != nil {
		return fmt.Errorf("setup recovery execution target: %w", err)
	}
	if d.Cause != SetupFailureTargetPreparation && d.RetainedWorktree == nil {
		return errors.New("setup recovery retained_worktree is required for setup-script failure")
	}
	if d.SetupRequirement == SetupRequirementAlreadyCompleted && d.RetainedWorktree == nil {
		return errors.New("completed setup recovery requires retained_worktree")
	}
	for _, retained := range []*RetainedWorktree{d.RetainedWorktree, d.RetainedPreviousWorktree} {
		if retained != nil {
			if err := retained.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w RetainedWorktree) Validate() error {
	switch {
	case strings.TrimSpace(w.WorktreeID) == "":
		return errors.New("retained worktree id is required")
	case strings.TrimSpace(w.Root) == "":
		return errors.New("retained worktree root is required")
	default:
		return nil
	}
}
