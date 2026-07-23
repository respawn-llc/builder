package serverapi

import (
	"encoding/json"
	"testing"
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
		SetupOperationID:    id,
		SourceWorkspaceRoot: "/source",
		WorktreeRoot:        "/worktree",
		ScriptPath:          "/source/scripts/setup.sh",
		Phase:               WorktreeSetupPhaseStarted,
	}
	if err := started.Validate(); err != nil {
		t.Fatalf("started setup event validate: %v", err)
	}
	invalidStarted := started
	invalidStarted.ScriptPath = ""
	if err := invalidStarted.Validate(); err == nil {
		t.Fatal("started setup event without script path validated")
	}
	failed := started
	failed.Phase = WorktreeSetupPhaseFailed
	failed.Error = "exit status 1"
	if err := failed.Validate(); err != nil {
		t.Fatalf("failed setup event validate: %v", err)
	}
	invalidFailed := started
	invalidFailed.Phase = WorktreeSetupPhaseFailed
	if err := invalidFailed.Validate(); err == nil {
		t.Fatal("failed setup event without terminal facts validated")
	}
}

func TestWorktreeSetupOperationIDJSONRejectsNonV4(t *testing.T) {
	var req WorktreeSetupSubscribeRequest
	if err := json.Unmarshal([]byte(`{"setup_operation_id":"11111111-1111-1111-1111-111111111111"}`), &req); err == nil {
		t.Fatal("expected non-v4 setup operation id to fail JSON decoding")
	}
}

func TestForegroundSetupRequestsRequireSetupOperationID(t *testing.T) {
	id := NewWorktreeSetupOperationID()
	valid := []interface{ Validate() error }{
		WorktreeCreateRequest{ClientRequestID: "req", SetupOperationID: id, SessionID: "session", BaseRef: "HEAD", CreateBranch: true, BranchName: "feature"},
		WorkflowTaskStartRequest{TaskID: "task", SetupOperationID: id},
		WorkflowTaskApproveRequest{TransitionID: "transition", SetupOperationID: id},
		WorkflowTaskMoveRequest{TaskID: "task", TargetNodeID: "node", SetupOperationID: id},
	}
	for _, req := range valid {
		if err := req.Validate(); err != nil {
			t.Fatalf("%T Validate: %v", req, err)
		}
	}
	invalid := []interface{ Validate() error }{
		WorktreeCreateRequest{ClientRequestID: "req", SessionID: "session", BaseRef: "HEAD", CreateBranch: true, BranchName: "feature"},
		WorkflowTaskStartRequest{TaskID: "task"},
		WorkflowTaskApproveRequest{TransitionID: "transition"},
		WorkflowTaskMoveRequest{TaskID: "task", TargetNodeID: "node"},
	}
	for _, req := range invalid {
		if err := req.Validate(); err == nil {
			t.Fatalf("%T Validate succeeded without setup operation id", req)
		}
	}
}
