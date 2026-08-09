package workflowcontract

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/worktreecontract"
	"github.com/google/uuid"
)

type SetupRecoveryUUID interface {
	~[16]byte
}

type SetupRecoveryExecutionTarget interface {
	Validate() error
}

type SetupRecoveryDetail[OperationID SetupRecoveryUUID, ExecutionTarget SetupRecoveryExecutionTarget] struct {
	SetupOperationID         OperationID                       `json:"setup_operation_id"`
	Cause                    worktreecontract.SetupFailureKind `json:"cause"`
	Diagnostic               string                            `json:"diagnostic"`
	ScriptPath               *string                           `json:"script_path"`
	SetupRequirement         worktreecontract.SetupRequirement `json:"setup_requirement"`
	ExecutionTarget          ExecutionTarget                   `json:"execution_target"`
	RetainedWorktree         *RetainedWorktree                 `json:"retained_worktree,omitempty"`
	RetainedPreviousWorktree *RetainedWorktree                 `json:"retained_previous_worktree,omitempty"`
}

type RetainedWorktree struct {
	WorktreeID string `json:"worktree_id"`
	Root       string `json:"root"`
}

func (d SetupRecoveryDetail[OperationID, ExecutionTarget]) Validate() error {
	setupOperationID := uuid.UUID(d.SetupOperationID)
	if setupOperationID == uuid.Nil ||
		setupOperationID.Version() != 4 ||
		setupOperationID.Variant() != uuid.RFC4122 {
		return errors.New("setup recovery setup_operation_id must be a UUID v4")
	}
	if strings.TrimSpace(d.Diagnostic) == "" {
		return errors.New("setup recovery diagnostic is required")
	}
	if !worktreecontract.IsRetryReadySetupFailure(d.Cause) {
		return errors.New("setup recovery cause must be retry-ready")
	}
	if !worktreecontract.IsValidSetupRequirement(d.SetupRequirement) {
		return errors.New("setup recovery setup_requirement is invalid")
	}
	if d.ScriptPath != nil && strings.TrimSpace(*d.ScriptPath) == "" {
		return errors.New("setup recovery script_path must be non-blank when present")
	}
	if d.Cause == worktreecontract.SetupFailureTargetPreparation && d.ScriptPath != nil {
		return errors.New("target-preparation setup recovery cannot include script_path")
	}
	if d.Cause != worktreecontract.SetupFailureTargetPreparation && d.ScriptPath == nil {
		return errors.New("setup recovery script_path is required for setup-script failure")
	}
	if d.Cause != worktreecontract.SetupFailureTargetPreparation &&
		d.SetupRequirement != worktreecontract.SetupRequirementRequired {
		return errors.New("setup-script failure requires setup")
	}
	if err := d.ExecutionTarget.Validate(); err != nil {
		return fmt.Errorf("setup recovery execution target: %w", err)
	}
	if d.Cause != worktreecontract.SetupFailureTargetPreparation && d.RetainedWorktree == nil {
		return errors.New("setup recovery retained_worktree is required for setup-script failure")
	}
	if d.RetainedWorktree != nil {
		if err := d.RetainedWorktree.Validate(); err != nil {
			return err
		}
	}
	if d.SetupRequirement == worktreecontract.SetupRequirementAlreadyCompleted &&
		d.RetainedWorktree == nil {
		return errors.New("completed setup recovery requires retained_worktree")
	}
	if d.RetainedPreviousWorktree != nil {
		return d.RetainedPreviousWorktree.Validate()
	}
	return nil
}

func (w RetainedWorktree) Validate() error {
	if strings.TrimSpace(w.WorktreeID) == "" {
		return errors.New("retained worktree id is required")
	}
	if strings.TrimSpace(w.Root) == "" {
		return errors.New("retained worktree root is required")
	}
	return nil
}
