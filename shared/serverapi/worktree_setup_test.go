package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/worktreecontract"
)

func TestWorktreeSetupOperationIDRequiresUUIDV4(t *testing.T) {
	id := NewWorktreeSetupOperationID()
	if err := id.Validate(); err != nil {
		t.Fatalf("generated setup operation id invalid: %v", err)
	}
	for _, raw := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000", "11111111-1111-1111-1111-111111111111"} {
		if _, err := ParseWorktreeSetupOperationID(raw); err == nil {
			t.Fatalf("ParseWorktreeSetupOperationID(%q) succeeded, want error", raw)
		}
	}
}

func TestWorktreeSetupEventValidation(t *testing.T) {
	id := NewWorktreeSetupOperationID()
	started := WorktreeSetupEvent{
		SetupOperationID: id,
		Phase:            WorktreeSetupPhaseStarted,
		Started: &WorktreeSetupStarted{
			SourceWorkspaceRoot: "/source",
			WorktreeRoot:        "/worktree",
			ScriptPath:          "/source/scripts/setup.sh",
		},
	}
	if err := started.Validate(); err != nil {
		t.Fatalf("started setup event validate: %v", err)
	}
	invalidStarted := started
	invalidStarted.Started = &WorktreeSetupStarted{
		SourceWorkspaceRoot: started.Started.SourceWorkspaceRoot,
		WorktreeRoot:        started.Started.WorktreeRoot,
	}
	if err := invalidStarted.Validate(); err == nil {
		t.Fatal("started setup event without script path validated")
	}
	failed := WorktreeSetupEvent{
		SetupOperationID: id,
		Phase:            WorktreeSetupPhaseFailed,
		Failed: &WorktreeSetupFailed{
			RetryReadiness: WorktreeSetupRetryReady,
			Cause: WorktreeSetupFailureCause{
				Kind:        WorktreeSetupFailureTargetPreparation,
				Preparation: &WorktreeSetupPreparationFailure{},
			},
			Diagnostic: "target preparation failed",
		},
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("failed setup event validate: %v", err)
	}
	invalidFailed := failed
	invalidFailed.Failed = &WorktreeSetupFailed{
		RetryReadiness: WorktreeSetupRetryReady,
		Cause: WorktreeSetupFailureCause{
			Kind:        WorktreeSetupFailureTargetPreparation,
			Preparation: &WorktreeSetupPreparationFailure{},
		},
	}
	if err := invalidFailed.Validate(); err == nil {
		t.Fatal("failed setup event without terminal facts validated")
	}
}

func TestSetupRecoveryDetailValidationAtAPIContract(t *testing.T) {
	valid := worktreecontract.SetupRecoveryDetail[WorktreeSetupOperationID, WorkflowExecutionTargetSelection]{
		SetupOperationID: NewWorktreeSetupOperationID(), Cause: worktreecontract.SetupFailureTargetPreparation,
		Diagnostic: "target failed", SetupRequirement: worktreecontract.SetupRequirementRequired,
		ExecutionTarget: WorkflowExecutionTargetSelection{Mode: WorkflowExecutionTargetModeHead},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate target-preparation recovery: %v", err)
	}
	script, setup := "scripts/setup.sh", valid
	setup.Cause, setup.ScriptPath, setup.RetainedWorktree = worktreecontract.SetupFailureProcessExit, &script, &worktreecontract.RetainedWorktree{WorktreeID: "worktree-1", Root: "/worktree"}
	if err := setup.Validate(); err != nil {
		t.Fatalf("Validate setup-script recovery: %v", err)
	}
	invalid := valid
	invalid.Cause = worktreecontract.SetupFailureCanceled
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted a non-retry-ready cause")
	}
	invalid, invalid.Cause, invalid.ScriptPath = valid, worktreecontract.SetupFailureTargetPreparation, &script
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted target preparation with a setup script")
	}
}

func TestWorktreeSetupOperationIDJSONRejectsNonV4(t *testing.T) {
	var req WorktreeSetupSubscribeRequest
	if err := json.Unmarshal([]byte(`{"setup_operation_id":"11111111-1111-1111-1111-111111111111"}`), &req); err == nil {
		t.Fatal("expected non-v4 setup operation id to fail JSON decoding")
	}
}

func TestForegroundStartRequiresSetupOperationID(t *testing.T) {
	id := NewWorktreeSetupOperationID()
	valid := []interface{ Validate() error }{
		WorktreeCreateRequest{ClientRequestID: "req", SetupOperationID: id, SessionID: "session", BaseRef: "HEAD", CreateBranch: true, BranchName: "feature"},
		WorkflowTaskStartRequest{TaskID: "task", SetupOperationID: id},
	}
	for _, req := range valid {
		if err := req.Validate(); err != nil {
			t.Fatalf("%T Validate: %v", req, err)
		}
	}
	invalid := []interface{ Validate() error }{
		WorktreeCreateRequest{ClientRequestID: "req", SessionID: "session", BaseRef: "HEAD", CreateBranch: true, BranchName: "feature"},
		WorkflowTaskStartRequest{TaskID: "task"},
	}
	for _, req := range invalid {
		if err := req.Validate(); err == nil {
			t.Fatalf("%T Validate succeeded without setup operation id", req)
		}
	}
}
