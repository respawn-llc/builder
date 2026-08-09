package workflowcontract

import (
	"errors"
	"testing"

	"core/shared/worktreecontract"
	"github.com/google/uuid"
)

type validSetupRecoveryTarget struct{}

func (validSetupRecoveryTarget) Validate() error {
	return nil
}

type invalidSetupRecoveryTarget struct{}

func (invalidSetupRecoveryTarget) Validate() error {
	return errors.New("invalid target")
}

func TestSetupRecoveryDetailOwnsCanonicalValidation(t *testing.T) {
	valid := SetupRecoveryDetail[uuid.UUID, validSetupRecoveryTarget]{
		SetupOperationID: uuid.New(),
		Cause:            worktreecontract.SetupFailureProcessExit,
		Diagnostic:       "setup failed",
		ScriptPath:       stringPointer("scripts/setup.sh"),
		SetupRequirement: worktreecontract.SetupRequirementRequired,
		ExecutionTarget:  validSetupRecoveryTarget{},
		RetainedWorktree: &RetainedWorktree{
			WorktreeID: "worktree-1",
			Root:       "/repo/worktree-1",
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate valid setup recovery: %v", err)
	}

	tests := []struct {
		name   string
		detail SetupRecoveryDetail[uuid.UUID, validSetupRecoveryTarget]
	}{
		{name: "non-v4 operation id", detail: func() SetupRecoveryDetail[uuid.UUID, validSetupRecoveryTarget] {
			detail := valid
			detail.SetupOperationID = uuid.MustParse("00000000-0000-1000-8000-000000000000")
			return detail
		}()},
		{name: "non-retry-ready cause", detail: func() SetupRecoveryDetail[uuid.UUID, validSetupRecoveryTarget] {
			detail := valid
			detail.Cause = worktreecontract.SetupFailureCanceled
			return detail
		}()},
		{name: "invalid setup requirement", detail: func() SetupRecoveryDetail[uuid.UUID, validSetupRecoveryTarget] {
			detail := valid
			detail.SetupRequirement = worktreecontract.SetupRequirement("unknown")
			return detail
		}()},
		{name: "blank diagnostic", detail: func() SetupRecoveryDetail[uuid.UUID, validSetupRecoveryTarget] {
			detail := valid
			detail.Diagnostic = " "
			return detail
		}()},
		{name: "missing setup script", detail: func() SetupRecoveryDetail[uuid.UUID, validSetupRecoveryTarget] {
			detail := valid
			detail.ScriptPath = nil
			return detail
		}()},
		{name: "missing retained worktree", detail: func() SetupRecoveryDetail[uuid.UUID, validSetupRecoveryTarget] {
			detail := valid
			detail.RetainedWorktree = nil
			return detail
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.detail.Validate(); err == nil {
				t.Fatal("Validate succeeded")
			}
		})
	}

	targetPreparation := valid
	targetPreparation.Cause = worktreecontract.SetupFailureTargetPreparation
	targetPreparation.ScriptPath = nil
	targetPreparation.RetainedWorktree = nil
	if err := targetPreparation.Validate(); err != nil {
		t.Fatalf("Validate target preparation without retained worktree: %v", err)
	}
	targetPreparation.ScriptPath = stringPointer("scripts/setup.sh")
	if err := targetPreparation.Validate(); err == nil {
		t.Fatal("Validate accepted target preparation with a setup script")
	}

	invalidTarget := SetupRecoveryDetail[uuid.UUID, invalidSetupRecoveryTarget]{
		SetupOperationID: uuid.New(),
		Cause:            worktreecontract.SetupFailureTargetPreparation,
		Diagnostic:       "target preparation failed",
		ExecutionTarget:  invalidSetupRecoveryTarget{},
	}
	if err := invalidTarget.Validate(); err == nil {
		t.Fatal("Validate accepted invalid execution target")
	}
}

func stringPointer(value string) *string {
	return &value
}
