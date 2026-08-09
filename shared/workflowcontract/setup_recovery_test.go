package workflowcontract

import (
	"encoding/json"
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
	encoded, err := json.Marshal(targetPreparation)
	if err != nil {
		t.Fatalf("Marshal target preparation: %v", err)
	}
	var fields map[string]*json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("Decode target preparation: %v", err)
	}
	for _, field := range []string{"retained_worktree", "retained_previous_worktree"} {
		if value, exists := fields[field]; !exists || value != nil {
			t.Fatalf("%s = %v, exists %t; want explicit null", field, value, exists)
		}
	}
	targetPreparation.ScriptPath = stringPointer("scripts/setup.sh")
	if err := targetPreparation.Validate(); err == nil {
		t.Fatal("Validate accepted target preparation with a setup script")
	}

	invalidTarget := SetupRecoveryDetail[uuid.UUID, invalidSetupRecoveryTarget]{
		SetupOperationID: uuid.New(), Cause: worktreecontract.SetupFailureTargetPreparation,
		Diagnostic: "target preparation failed", SetupRequirement: worktreecontract.SetupRequirementRequired,
		ExecutionTarget: invalidSetupRecoveryTarget{},
	}
	if err := invalidTarget.Validate(); err == nil {
		t.Fatal("Validate accepted invalid execution target")
	}
}

func stringPointer(value string) *string {
	return &value
}
