package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/clientui"
)

func TestWorktreeTopologyEntryRequiresExactlyOneMatchingPayload(t *testing.T) {
	git := WorktreeGitFacts{CanonicalRoot: "/repo/feature", HeadObject: "abc123", PathAvailable: true}
	kent := WorktreeKentFacts{
		WorktreeID:    "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
		CanonicalRoot: "/repo/feature",
		DisplayName:   "feature",
	}
	valid := []WorktreeTopologyEntry{
		{Variant: WorktreeTopologyVariantRegistered, Registered: &WorktreeRegisteredFacts{Git: git, Kent: kent}},
		{Variant: WorktreeTopologyVariantExternal, External: &WorktreeExternalFacts{Git: git}},
		{Variant: WorktreeTopologyVariantMissing, Missing: &WorktreeMissingFacts{Kent: kent}},
	}
	for _, entry := range valid {
		if err := entry.Validate(); err != nil {
			t.Fatalf("%s entry rejected: %v", entry.Variant, err)
		}
	}

	invalid := []WorktreeTopologyEntry{
		{Variant: WorktreeTopologyVariantRegistered},
		{Variant: WorktreeTopologyVariantRegistered, Registered: &WorktreeRegisteredFacts{Git: git, Kent: kent}, External: &WorktreeExternalFacts{Git: git}},
		{Variant: WorktreeTopologyVariantExternal, Registered: &WorktreeRegisteredFacts{Git: git, Kent: kent}},
		{Variant: WorktreeTopologyVariantMissing, Missing: &WorktreeMissingFacts{}},
	}
	for _, entry := range invalid {
		if err := entry.Validate(); err == nil {
			t.Fatalf("invalid topology entry validated: %+v", entry)
		}
	}
	empty := ""
	if err := (WorktreeGitFacts{
		CanonicalRoot: "/repo/feature",
		HeadObject:    "abc123",
		BranchName:    &empty,
	}).Validate(); err == nil {
		t.Fatal("empty optional Git fact validated")
	}
	if err := (WorktreeKentFacts{
		WorktreeID:      kent.WorktreeID,
		CanonicalRoot:   kent.CanonicalRoot,
		DisplayName:     kent.DisplayName,
		OriginSessionID: &empty,
	}).Validate(); err == nil {
		t.Fatal("empty optional Kent fact validated")
	}
}

func TestWorktreeStatusHasNoSelectorAndValidatesTypedProblems(t *testing.T) {
	response := WorktreeStatusResponse{
		Target: clientui.SessionExecutionTarget{
			WorkspaceID:   "workspace",
			WorkspaceRoot: "/repo",
			Worktree:      &clientui.SessionExecutionWorktreeTarget{ID: "wt", Root: "/repo/feature"},
		},
		Worktree: WorktreeStatusTarget{
			RecordedRoot: "/repo/feature",
			DisplayName:  stringPointer("feature"),
		},
		Problems: []WorktreeStatusProblem{{
			Kind: WorktreeStatusProblemRootMissing,
			Root: "/repo/feature",
		}},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("status response rejected: %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if _, ok := fields["selector"]; ok {
		t.Fatal("status JSON exposed a selector")
	}
	for _, problem := range []WorktreeStatusProblem{
		{Kind: WorktreeStatusProblemRootInaccessible, Root: "/repo/feature"},
		{Kind: WorktreeStatusProblemGitBindingMissing, Root: "/repo/feature"},
		{Kind: WorktreeStatusProblemGitBindingMismatched, Root: "/repo/feature"},
		{Kind: WorktreeStatusProblemRecordedRefMissing, Ref: "refs/heads/feature"},
	} {
		if err := problem.Validate(); err != nil {
			t.Fatalf("status problem %s rejected: %v", problem.Kind, err)
		}
	}
	if err := (WorktreeStatusProblem{Kind: WorktreeStatusProblemRecordedRefMissing}).Validate(); err == nil {
		t.Fatal("recorded ref problem without ref validated")
	}
}

func TestWorktreeOperationContractValidatesUUIDV4PayloadAndLifecycle(t *testing.T) {
	operationID := NewWorktreeOperationID()
	if err := operationID.Validate(); err != nil {
		t.Fatalf("generated operation id rejected: %v", err)
	}
	for _, raw := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000", "11111111-1111-1111-1111-111111111111"} {
		if _, err := ParseWorktreeOperationID(raw); err == nil {
			t.Fatalf("ParseWorktreeOperationID(%q) succeeded", raw)
		}
	}
	var decoded WorktreeScheduledAcknowledgement
	if err := json.Unmarshal([]byte(`{"operation_id":"11111111-1111-1111-1111-111111111111"}`), &decoded); err == nil {
		t.Fatal("scheduled acknowledgement accepted a non-v4 operation ID")
	}

	selector := "feature"
	payload := WorktreeOperationPayload{
		Version:             WorktreeOperationPayloadVersion1,
		SessionID:           "session",
		Kind:                WorktreeOperationKindDelete,
		Selector:            &selector,
		ForceFolderRemoval:  true,
		BranchCleanupPolicy: WorktreeBranchCleanupPolicyDeleteSafe,
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("delete payload rejected: %v", err)
	}
	if err := (WorktreeOperationPayload{
		Version:             WorktreeOperationPayloadVersion1,
		SessionID:           "session",
		Kind:                WorktreeOperationKindLeave,
		Selector:            &selector,
		BranchCleanupPolicy: WorktreeBranchCleanupPolicyRetain,
	}).Validate(); err == nil {
		t.Fatal("leave payload with selector validated")
	}
	if err := (WorktreeOperationPayload{
		Version:             WorktreeOperationPayloadVersion1,
		SessionID:           "session",
		Kind:                WorktreeOperationKindEnter,
		BranchCleanupPolicy: WorktreeBranchCleanupPolicyRetain,
	}).Validate(); err == nil {
		t.Fatal("enter payload without selector validated")
	}

	acknowledgement := WorktreeScheduledAcknowledgement{OperationID: operationID}
	if err := acknowledgement.Validate(); err != nil {
		t.Fatalf("scheduled acknowledgement rejected: %v", err)
	}
	event := WorktreeOperationEvent{
		OperationID: operationID,
		Version:     1,
		Kind:        WorktreeOperationEventKindCompleted,
		Result:      &WorktreeOperationResult{Target: &clientui.SessionExecutionTarget{WorkspaceID: "workspace", WorkspaceRoot: "/repo"}},
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("completed event rejected: %v", err)
	}
	event.Version = 0
	if err := event.Validate(); err == nil {
		t.Fatal("zero lifecycle version validated")
	}
	failed := WorktreeOperationEvent{
		OperationID: operationID,
		Version:     2,
		Kind:        WorktreeOperationEventKindFailed,
		Failure: &WorktreeOperationFailure{
			Kind:       WorktreeOperationFailureKindExecutionIndeterminate,
			Diagnostic: "server stopped before the transition result was known",
		},
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("failed event rejected: %v", err)
	}
}

func TestWorktreeDeleteResultAndCleanupPoliciesAreDiscriminated(t *testing.T) {
	operationID := NewWorktreeOperationID()
	completed := WorktreeDeleteResult{
		Kind: WorktreeDeleteResultKindCompleted,
		Completed: &WorktreeDeleteCompletedResult{
			Cleanup: WorktreeBranchCleanupOutcome{
				Kind:       WorktreeBranchCleanupOutcomeDeleted,
				BranchName: stringPointer("feature"),
			},
		},
	}
	if err := completed.Validate(); err != nil {
		t.Fatalf("completed deletion result rejected: %v", err)
	}
	scheduled := WorktreeDeleteResult{
		Kind:      WorktreeDeleteResultKindScheduled,
		Scheduled: &WorktreeScheduledAcknowledgement{OperationID: operationID},
	}
	if err := scheduled.Validate(); err != nil {
		t.Fatalf("scheduled deletion result rejected: %v", err)
	}
	if err := (WorktreeDeleteResult{
		Kind:      WorktreeDeleteResultKindScheduled,
		Completed: completed.Completed,
		Scheduled: scheduled.Scheduled,
	}).Validate(); err == nil {
		t.Fatal("delete result with both payloads validated")
	}
	for _, policy := range []WorktreeBranchCleanupPolicy{
		WorktreeBranchCleanupPolicyRetain,
		WorktreeBranchCleanupPolicyAutoIfKentCreated,
		WorktreeBranchCleanupPolicyDeleteSafe,
	} {
		if err := policy.Validate(); err != nil {
			t.Fatalf("cleanup policy %q rejected: %v", policy, err)
		}
	}
	if err := (WorktreeBranchCleanupOutcome{
		Kind:       WorktreeBranchCleanupOutcomeRetained,
		BranchName: stringPointer("feature"),
	}).Validate(); err != nil {
		t.Fatalf("retained cleanup outcome rejected: %v", err)
	}
	if err := (WorktreeBranchCleanupOutcome{Kind: WorktreeBranchCleanupOutcomeDeleted}).Validate(); err == nil {
		t.Fatal("deleted cleanup outcome without branch validated")
	}
}

func TestWorktreeOperationRequestsRejectMissingRequiredFacts(t *testing.T) {
	operationID := NewWorktreeOperationID()
	valid := []interface{ Validate() error }{
		WorktreeSelectorPreviewRequest{SessionID: "session", Selector: "feature"},
		WorktreeEnterRequest{OperationID: operationID, SessionID: "session", Selector: "feature"},
		WorktreeLeaveRequest{OperationID: operationID, SessionID: "session"},
		WorktreeDeleteOperationRequest{
			OperationID:         operationID,
			SessionID:           "session",
			Selector:            "feature",
			BranchCleanupPolicy: WorktreeBranchCleanupPolicyRetain,
		},
	}
	for _, request := range valid {
		if err := request.Validate(); err != nil {
			t.Fatalf("%T rejected: %v", request, err)
		}
	}
	invalid := []interface{ Validate() error }{
		WorktreeSelectorPreviewRequest{SessionID: "session"},
		WorktreeEnterRequest{SessionID: "session", Selector: "feature"},
		WorktreeLeaveRequest{OperationID: operationID},
		WorktreeDeleteOperationRequest{OperationID: operationID, SessionID: "session", Selector: "feature"},
	}
	for _, request := range invalid {
		if err := request.Validate(); err == nil {
			t.Fatalf("%T validated without required facts", request)
		}
	}
}

func stringPointer(value string) *string {
	return &value
}
