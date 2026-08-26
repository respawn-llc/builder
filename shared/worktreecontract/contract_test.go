package worktreecontract

import (
	"testing"

	"core/shared/workflowcontract"
)

func TestCanonicalWorktreeContractOwnsIdentityTopologyAndSetupFacts(t *testing.T) {
	operationID := NewOperationID()
	if err := operationID.Validate(); err != nil {
		t.Fatalf("OperationID.Validate: %v", err)
	}
	setupOperationID := NewSetupOperationID()
	if err := setupOperationID.Validate(); err != nil {
		t.Fatalf("SetupOperationID.Validate: %v", err)
	}

	target := workflowcontract.ExecutionTargetSelection{Mode: workflowcontract.ExecutionTargetModeHead}
	recovery := SetupRecoveryDetail{
		SetupOperationID: setupOperationID,
		Cause:            SetupFailureTargetPreparation,
		Diagnostic:       "target preparation failed",
		SetupRequirement: SetupRequirementRequired,
		ExecutionTarget:  target,
	}
	if err := recovery.Validate(); err != nil {
		t.Fatalf("SetupRecoveryDetail.Validate: %v", err)
	}
}
