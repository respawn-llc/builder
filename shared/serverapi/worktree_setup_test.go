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

func TestWorktreeSetupEventValidationUsesPhaseDiscriminatedPayloads(t *testing.T) {
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
	completed := WorktreeSetupEvent{
		SetupOperationID: id,
		Phase:            WorktreeSetupPhaseCompleted,
		Completed: &WorktreeSetupCompleted{
			RetainedPreviousWorktree: testRetainedPreviousWorktree(),
		},
	}
	if err := completed.Validate(); err != nil {
		t.Fatalf("completed setup event validate: %v", err)
	}
	notRequired := WorktreeSetupEvent{
		SetupOperationID: id,
		Phase:            WorktreeSetupPhaseNotRequired,
		NotRequired: &WorktreeSetupNotRequired{
			Reason:                   WorktreeSetupNotRequiredNoConfiguredScript,
			RetainedPreviousWorktree: testRetainedPreviousWorktree(),
		},
	}
	if err := notRequired.Validate(); err != nil {
		t.Fatalf("not-required setup event validate: %v", err)
	}
	failed := WorktreeSetupEvent{
		SetupOperationID: id,
		Phase:            WorktreeSetupPhaseFailed,
		Failed: &WorktreeSetupFailed{
			RetryReadiness: WorktreeSetupRetryReady,
			Cause: WorktreeSetupFailureCause{
				Kind: WorktreeSetupFailureProcessExit,
				ProcessExit: &WorktreeSetupProcessExit{
					ExitCode: 7,
					Stdout:   stringValuePointer(""),
					Stderr:   stringValuePointer("failed"),
				},
			},
			Diagnostic:               "setup exited with status 7",
			ScriptPath:               stringValuePointer("/source/scripts/setup.sh"),
			RetainedWorktree:         testRegisteredWorktreeTopology(),
			RetainedPreviousWorktree: testRetainedPreviousWorktree(),
		},
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("failed setup event validate: %v", err)
	}

	for name, invalid := range map[string]WorktreeSetupEvent{
		"mixed payloads": {
			SetupOperationID: id,
			Phase:            WorktreeSetupPhaseStarted,
			Started:          started.Started,
			Completed:        &WorktreeSetupCompleted{},
		},
		"missing matching payload": {SetupOperationID: id, Phase: WorktreeSetupPhaseCompleted},
		"blank started path": {
			SetupOperationID: id,
			Phase:            WorktreeSetupPhaseStarted,
			Started: &WorktreeSetupStarted{
				SourceWorkspaceRoot: "/source",
				WorktreeRoot:        "",
				ScriptPath:          "/source/scripts/setup.sh",
			},
		},
		"blank failure diagnostic": {
			SetupOperationID: id,
			Phase:            WorktreeSetupPhaseFailed,
			Failed: &WorktreeSetupFailed{
				RetryReadiness: WorktreeSetupRetryReady,
				Cause: WorktreeSetupFailureCause{
					Kind:        WorktreeSetupFailureTargetPreparation,
					Preparation: &WorktreeSetupPreparationFailure{},
				},
				Diagnostic: " ",
			},
		},
		"empty inapplicable output sentinel": {
			SetupOperationID: id,
			Phase:            WorktreeSetupPhaseFailed,
			Failed: &WorktreeSetupFailed{
				RetryReadiness: WorktreeSetupNonRetryable,
				Cause: WorktreeSetupFailureCause{
					Kind:                    WorktreeSetupFailureInterruptionPersistence,
					InterruptionPersistence: &WorktreeSetupInterruptionPersistenceFailure{},
					ProcessExit:             &WorktreeSetupProcessExit{ExitCode: 1},
				},
				Diagnostic: "interruption persistence failed",
			},
		},
		"retryable cause marked non-retryable": {
			SetupOperationID: id,
			Phase:            WorktreeSetupPhaseFailed,
			Failed: &WorktreeSetupFailed{
				RetryReadiness: WorktreeSetupNonRetryable,
				Cause: WorktreeSetupFailureCause{
					Kind:        WorktreeSetupFailureTargetPreparation,
					Preparation: &WorktreeSetupPreparationFailure{},
				},
				Diagnostic: "target preparation failed",
			},
		},
		"non-retryable cause marked retry-ready": {
			SetupOperationID: id,
			Phase:            WorktreeSetupPhaseFailed,
			Failed: &WorktreeSetupFailed{
				RetryReadiness: WorktreeSetupRetryReady,
				Cause: WorktreeSetupFailureCause{
					Kind:     WorktreeSetupFailureCanceled,
					Canceled: &WorktreeSetupCanceled{},
				},
				Diagnostic: "preparation canceled",
			},
		},
		"malformed not-required retained previous worktree": {
			SetupOperationID: id,
			Phase:            WorktreeSetupPhaseNotRequired,
			NotRequired: &WorktreeSetupNotRequired{
				Reason: WorktreeSetupNotRequiredNoConfiguredScript,
				RetainedPreviousWorktree: &RetainedPreviousWorktree{
					Worktree: WorktreeTopologyEntry{Variant: WorktreeTopologyVariantMissing},
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := invalid.Validate(); err == nil {
				t.Fatalf("invalid setup event validated: %#v", invalid)
			}
		})
	}
}

func TestWorktreeSetupOperationIDJSONRejectsNonV4(t *testing.T) {
	var req WorktreeSetupSubscribeRequest
	if err := json.Unmarshal([]byte(`{"setup_operation_id":"11111111-1111-1111-1111-111111111111"}`), &req); err == nil {
		t.Fatal("expected non-v4 setup operation id to fail JSON decoding")
	}
}

func TestWorktreeSetupOperationIDStringNeverEncodesAbsence(t *testing.T) {
	if got := (WorktreeSetupOperationID{}).String(); got == "" {
		t.Fatal("zero setup operation id formatted as an empty-string absence sentinel")
	}
	if _, err := json.Marshal(WorktreeSetupOperationID{}); err == nil {
		t.Fatal("zero setup operation id serialized")
	}
}

func TestWorktreeSetupEventJSONKeepsInapplicableFactsAbsentAndNullableOutputPresent(t *testing.T) {
	id := NewWorktreeSetupOperationID()
	notRequired := WorktreeSetupEvent{
		SetupOperationID: id,
		Phase:            WorktreeSetupPhaseNotRequired,
		NotRequired: &WorktreeSetupNotRequired{
			Reason:                   WorktreeSetupNotRequiredNoTargetPreparation,
			RetainedPreviousWorktree: testRetainedPreviousWorktree(),
		},
	}
	raw, err := json.Marshal(notRequired)
	if err != nil {
		t.Fatalf("marshal not-required event: %v", err)
	}
	var eventFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &eventFields); err != nil {
		t.Fatalf("decode not-required event fields: %v", err)
	}
	for _, forbidden := range []string{"started", "completed", "failed"} {
		if _, exists := eventFields[forbidden]; exists {
			t.Fatalf("not-required event serialized inapplicable %q payload: %s", forbidden, raw)
		}
	}
	var notRequiredFields map[string]json.RawMessage
	if err := json.Unmarshal(eventFields["not_required"], &notRequiredFields); err != nil {
		t.Fatalf("decode not-required payload fields: %v", err)
	}
	for _, forbidden := range []string{"script_path", "stdout", "stderr"} {
		if _, exists := notRequiredFields[forbidden]; exists {
			t.Fatalf("not-required event serialized inapplicable %q fact: %s", forbidden, raw)
		}
	}
	if _, exists := notRequiredFields["retained_previous_worktree"]; !exists {
		t.Fatalf("not-required event omitted retained previous worktree: %s", raw)
	}

	failed := WorktreeSetupEvent{
		SetupOperationID: id,
		Phase:            WorktreeSetupPhaseFailed,
		Failed: &WorktreeSetupFailed{
			RetryReadiness: WorktreeSetupRetryReady,
			Cause: WorktreeSetupFailureCause{
				Kind: WorktreeSetupFailureTimeout,
				Timeout: &WorktreeSetupTimeout{
					Stdout: stringValuePointer(""),
				},
			},
			Diagnostic: "setup timed out",
			ScriptPath: stringValuePointer("/source/scripts/setup.sh"),
		},
	}
	raw, err = json.Marshal(failed)
	if err != nil {
		t.Fatalf("marshal failed event: %v", err)
	}
	eventFields = nil
	if err := json.Unmarshal(raw, &eventFields); err != nil {
		t.Fatalf("decode failed event fields: %v", err)
	}
	var failedFields map[string]json.RawMessage
	if err := json.Unmarshal(eventFields["failed"], &failedFields); err != nil {
		t.Fatalf("decode failed payload fields: %v", err)
	}
	var causeFields map[string]json.RawMessage
	if err := json.Unmarshal(failedFields["cause"], &causeFields); err != nil {
		t.Fatalf("decode failure cause fields: %v", err)
	}
	var scriptPath string
	if err := json.Unmarshal(failedFields["script_path"], &scriptPath); err != nil ||
		scriptPath != "/source/scripts/setup.sh" {
		t.Fatalf("failed setup script path was not preserved: %s", raw)
	}
	var timeoutFields map[string]json.RawMessage
	if err := json.Unmarshal(causeFields["timeout"], &timeoutFields); err != nil {
		t.Fatalf("decode timeout fields: %v", err)
	}
	var stdout string
	if err := json.Unmarshal(timeoutFields["stdout"], &stdout); err != nil || stdout != "" {
		t.Fatalf("nullable stdout output was not preserved: %s", raw)
	}
	if _, exists := timeoutFields["stderr"]; exists {
		t.Fatalf("nullable output presence was not preserved: %s", raw)
	}

	targetPreparation := WorktreeSetupEvent{
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
	raw, err = json.Marshal(targetPreparation)
	if err != nil {
		t.Fatalf("marshal target-preparation failure: %v", err)
	}
	eventFields = nil
	if err := json.Unmarshal(raw, &eventFields); err != nil {
		t.Fatalf("decode target-preparation event fields: %v", err)
	}
	failedFields = nil
	if err := json.Unmarshal(eventFields["failed"], &failedFields); err != nil {
		t.Fatalf("decode target-preparation failure fields: %v", err)
	}
	if got := string(failedFields["script_path"]); got != "null" {
		t.Fatalf("target-preparation script_path = %s, want explicit null: %s", got, raw)
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

func testRetainedPreviousWorktree() *RetainedPreviousWorktree {
	return &RetainedPreviousWorktree{Worktree: *testRegisteredWorktreeTopology()}
}

func testRegisteredWorktreeTopology() *WorktreeTopologyEntry {
	return &WorktreeTopologyEntry{
		Variant: WorktreeTopologyVariantRegistered,
		Registered: &WorktreeRegisteredFacts{
			Git: WorktreeGitFacts{
				CanonicalRoot: "/repo/feature",
				HeadObject:    "abc123",
				PathAvailable: true,
			},
			Kent: WorktreeKentFacts{
				WorktreeID:    "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
				CanonicalRoot: "/repo/feature",
				DisplayName:   "feature",
			},
		},
	}
}

func stringValuePointer(value string) *string {
	return &value
}
