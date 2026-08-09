package workflowcontract

import (
	"testing"

	"core/shared/worktreecontract"
	"github.com/google/uuid"
)

type validSetupRecoveryTarget struct{}

func (validSetupRecoveryTarget) Validate() error {
	return nil
}

func TestSetupRecoveryDetailOwnsCanonicalValidation(t *testing.T) {
	valid := SetupRecoveryDetail[uuid.UUID, validSetupRecoveryTarget]{
		SetupOperationID: uuid.New(), Cause: worktreecontract.SetupFailureProcessExit,
		Diagnostic: "setup failed", ScriptPath: stringPointer("scripts/setup.sh"),
		SetupRequirement: worktreecontract.SetupRequirementRequired, ExecutionTarget: validSetupRecoveryTarget{},
		RetainedWorktree: &RetainedWorktree{WorktreeID: "worktree-1", Root: "/repo/worktree-1"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate valid setup recovery: %v", err)
	}
	invalid := valid
	invalid.Cause = worktreecontract.SetupFailureCanceled
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted a non-retry-ready cause")
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
}

func stringPointer(value string) *string {
	return &value
}
