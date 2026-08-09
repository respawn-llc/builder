package workflow_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/runtimeids"
	"github.com/google/uuid"
)

func TestCurrentNodeInterruptionDetailOwnsPersistedDiagnosticField(t *testing.T) {
	detail := workflow.NewCurrentNodeInterruptionDetail("script_failed", errors.New("script exited"))
	if detail.Code != "script_failed" {
		t.Fatalf("code = %q", detail.Code)
	}
	diagnostic := detail.Diagnostic()
	if diagnostic == nil || *diagnostic != "script exited" {
		t.Fatalf("diagnostic = %v", diagnostic)
	}
	legacy := workflow.CurrentNodeInterruptionDetail{
		Code:   "script_failed",
		Fields: map[string]string{workflow.CurrentNodeInterruptionDiagnosticField: "legacy failure"},
	}
	diagnostic = legacy.Diagnostic()
	if diagnostic == nil || *diagnostic != "legacy failure" {
		t.Fatalf("legacy diagnostic = %v", diagnostic)
	}
}

func TestCurrentNodeInterruptionDetailCarriesTypedSetupRecovery(t *testing.T) {
	setupOperationID := uuid.New()
	scriptPath := "scripts/setup.sh"
	recovery := workflow.CurrentNodeSetupRecoveryDetail{
		SetupOperationID: setupOperationID,
		Cause:            workflow.CurrentNodeSetupRecoveryCauseProcessExit,
		Diagnostic:       "setup failed after retry",
		ScriptPath:       &scriptPath,
		SetupRequirement: workflow.CurrentNodeSetupRequirementRequired,
		ExecutionTarget: workflow.ExecutionTargetSelection{
			Mode: workflow.ExecutionTargetModeHead,
		},
		RetainedWorktree: &workflow.CurrentNodeRetainedWorktree{
			WorktreeID: "worktree-1",
			Root:       "/repo/worktree-1",
		},
	}
	if err := recovery.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	detail := workflow.CurrentNodeInterruptionDetail{
		Code:          "workflow_setup_recovery",
		SetupRecovery: &recovery,
	}
	diagnostic := detail.Diagnostic()
	if diagnostic == nil || *diagnostic != recovery.Diagnostic {
		t.Fatalf("derived setup recovery diagnostic = %v, want %q", diagnostic, recovery.Diagnostic)
	}
	duplicated := detail
	duplicated.Fields = map[string]string{
		workflow.CurrentNodeInterruptionDiagnosticField: "contradictory generic diagnostic",
	}
	if err := duplicated.Validate(); err == nil {
		t.Fatal("setup recovery with a duplicated generic diagnostic validated")
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal interruption detail: %v", err)
	}
	var decoded workflow.CurrentNodeInterruptionDetail
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal interruption detail: %v", err)
	}
	if decoded.SetupRecovery == nil ||
		decoded.SetupRecovery.SetupOperationID != setupOperationID ||
		decoded.SetupRecovery.ExecutionTarget.Mode != workflow.ExecutionTargetModeHead ||
		decoded.SetupRecovery.ScriptPath == nil ||
		*decoded.SetupRecovery.ScriptPath != scriptPath ||
		decoded.SetupRecovery.RetainedWorktree == nil ||
		decoded.SetupRecovery.RetainedWorktree.Root != recovery.RetainedWorktree.Root {
		t.Fatalf("decoded setup recovery = %+v, want %+v", decoded.SetupRecovery, recovery)
	}
}

func TestCurrentNodeSetupRecoveryRequiresExecutionTargetSelection(t *testing.T) {
	recovery := workflow.CurrentNodeSetupRecoveryDetail{
		SetupOperationID: uuid.New(),
		Cause:            workflow.CurrentNodeSetupRecoveryCauseTargetPreparation,
		Diagnostic:       "target preparation failed",
		SetupRequirement: workflow.CurrentNodeSetupRequirementRequired,
	}
	if err := recovery.Validate(); err == nil {
		t.Fatal("setup recovery without the failed execution target selection validated")
	}
}

func TestCurrentNodeSetupRecoveryAllowsTargetPreparationWithoutRetainedWorktree(t *testing.T) {
	setupOperationID := uuid.New()
	recovery := workflow.CurrentNodeSetupRecoveryDetail{
		SetupOperationID: setupOperationID,
		Cause:            workflow.CurrentNodeSetupRecoveryCauseTargetPreparation,
		Diagnostic:       "target resolution failed",
		SetupRequirement: workflow.CurrentNodeSetupRequirementRequired,
		ExecutionTarget: workflow.ExecutionTargetSelection{
			Mode: workflow.ExecutionTargetModeDefaultBranch,
		},
	}
	if err := recovery.Validate(); err != nil {
		t.Fatalf("Validate target-preparation recovery without topology: %v", err)
	}
	raw, err := json.Marshal(workflow.CurrentNodeInterruptionDetail{
		Code:          "workflow_target_preparation_failed",
		SetupRecovery: &recovery,
	})
	if err != nil {
		t.Fatalf("marshal interruption detail: %v", err)
	}
	decoded, err := workflow.DecodeCurrentNodeInterruptionDetail(string(raw))
	if err != nil {
		t.Fatalf("DecodeCurrentNodeInterruptionDetail: %v", err)
	}
	if decoded.SetupRecovery == nil ||
		decoded.SetupRecovery.SetupOperationID != setupOperationID ||
		decoded.SetupRecovery.Cause != workflow.CurrentNodeSetupRecoveryCauseTargetPreparation ||
		decoded.SetupRecovery.RetainedWorktree != nil {
		t.Fatalf("decoded target-preparation recovery = %+v", decoded.SetupRecovery)
	}
}

func TestCurrentNodeReferenceUsesTaskNodeAndOptionalBranchAsItsNaturalIdentity(t *testing.T) {
	branchA := workflow.TransitionBranchKey("implementation")
	branchB := workflow.TransitionBranchKey("review")

	serial, err := workflow.NewCurrentNodeReference("task-1", "node-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference serial: %v", err)
	}
	sameSerial, err := workflow.NewCurrentNodeReference("task-1", "node-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference same serial: %v", err)
	}
	parallelA, err := workflow.NewCurrentNodeReference("task-1", "node-1", &branchA)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference branch A: %v", err)
	}
	sameParallelA, err := workflow.NewCurrentNodeReference("task-1", "node-1", &branchA)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference same branch A: %v", err)
	}
	parallelB, err := workflow.NewCurrentNodeReference("task-1", "node-1", &branchB)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference branch B: %v", err)
	}

	if !serial.Equal(sameSerial) {
		t.Fatalf("serial references must be equal: %v and %v", serial, sameSerial)
	}
	if serial.Equal(parallelA) {
		t.Fatalf("serial and branch-scoped references must differ: %v and %v", serial, parallelA)
	}
	if !parallelA.Equal(sameParallelA) {
		t.Fatalf("same branch-scoped references must be equal: %v and %v", parallelA, sameParallelA)
	}
	if parallelA.Equal(parallelB) {
		t.Fatalf("different branch-scoped references must differ: %v and %v", parallelA, parallelB)
	}
	if branch, ok := serial.TransitionBranchKey(); ok || branch != "" {
		t.Fatalf("serial reference branch = %q, %v; want absent", branch, ok)
	}
	if branch, ok := parallelA.TransitionBranchKey(); !ok || branch != branchA {
		t.Fatalf("branch-scoped reference branch = %q, %v; want %q, true", branch, ok, branchA)
	}
}

func TestCurrentNodeCarriesNullableSessionBindingAndTypedSchedulingInterruption(t *testing.T) {
	ref, err := workflow.NewCurrentNodeReference("task-1", "node-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	sessionID := runtimeids.NewSessionID()
	occurredAt := time.Date(2026, time.July, 23, 8, 30, 0, 0, time.UTC)
	node, err := workflow.NewCurrentNode(ref, &sessionID, &workflow.CurrentNodeScheduling{
		State: workflow.CurrentNodeSchedulingInterrupted,
		Interruption: &workflow.CurrentNodeInterruption{
			Reason: workflow.CurrentNodeInterruptionReason("server_restart"),
			Detail: workflow.CurrentNodeInterruptionDetail{
				Code:   "workflow.execution.restarted",
				Fields: map[string]string{"operation": "recovery"},
			},
			OccurredAt: occurredAt,
		},
	})
	if err != nil {
		t.Fatalf("NewCurrentNode: %v", err)
	}

	if node.SessionID == nil || *node.SessionID != sessionID {
		t.Fatalf("session binding = %+v, want %q", node.SessionID, sessionID)
	}
	if node.Scheduling == nil || node.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("scheduling = %+v, want interrupted", node.Scheduling)
	}
	if node.Scheduling.Interruption == nil || node.Scheduling.Interruption.OccurredAt != occurredAt {
		t.Fatalf("interruption = %+v, want occurrence %s", node.Scheduling.Interruption, occurredAt)
	}

	terminal, err := workflow.NewCurrentNode(ref, nil, nil)
	if err != nil {
		t.Fatalf("NewCurrentNode terminal: %v", err)
	}
	if terminal.SessionID != nil || terminal.Scheduling != nil {
		t.Fatalf("terminal node = %+v, want no session or scheduling state", terminal)
	}
}

func TestApprovalIdentityAndMutationResultsUseCurrentNodeFacts(t *testing.T) {
	approvalID := workflow.NewApprovalID()
	if err := approvalID.Validate(); err != nil {
		t.Fatalf("new approval id invalid: %v", err)
	}
	if _, err := workflow.ParseApprovalID(approvalID.String()); err != nil {
		t.Fatalf("ParseApprovalID: %v", err)
	}

	source, err := workflow.NewCurrentNodeReference("task-1", "node-source", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference source: %v", err)
	}
	target, err := workflow.NewCurrentNodeReference("task-1", "node-target", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference target: %v", err)
	}
	targetNode, err := workflow.NewCurrentNode(target, nil, nil)
	if err != nil {
		t.Fatalf("NewCurrentNode target: %v", err)
	}
	result := workflow.CurrentNodeMutationResult{
		Removed: []workflow.CurrentNodeReference{source},
		Created: []workflow.CurrentNode{targetNode},
		Updated: []workflow.CurrentNode{targetNode},
	}

	if len(result.Removed) != 1 || !result.Removed[0].Equal(source) {
		t.Fatalf("removed = %+v, want source", result.Removed)
	}
	if len(result.Created) != 1 || !result.Created[0].Reference.Equal(target) {
		t.Fatalf("created = %+v, want target", result.Created)
	}
	if len(result.Updated) != 1 || !result.Updated[0].Reference.Equal(target) {
		t.Fatalf("updated = %+v, want target", result.Updated)
	}
}
