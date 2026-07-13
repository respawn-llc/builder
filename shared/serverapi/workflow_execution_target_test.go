package serverapi

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"core/shared/protocol"
)

func TestWorkflowExecutionTargetSelectionRequestValidation(t *testing.T) {
	customRef := "refs/tags/v1"
	blankRef := " "
	valid := WorkflowExecutionTargetSelection{Mode: WorkflowExecutionTargetModeCustomRef, CustomRef: &customRef}

	for _, request := range []interface{ Validate() error }{
		WorkflowTaskStartRequest{TaskID: "task", SetupOperationID: NewWorktreeSetupOperationID(), ExecutionTarget: &valid},
		WorkflowTaskApproveRequest{TransitionID: "transition", SetupOperationID: NewWorktreeSetupOperationID(), ExecutionTarget: &valid},
		WorkflowTaskMoveRequest{TaskID: "task", TargetNodeID: "node", SetupOperationID: NewWorktreeSetupOperationID(), ExecutionTarget: &valid},
	} {
		if err := request.Validate(); err != nil {
			t.Fatalf("%T valid selection rejected: %v", request, err)
		}
	}

	for _, selection := range []WorkflowExecutionTargetSelection{
		{Mode: WorkflowExecutionTargetModeAskOnFirstExecution},
		{Mode: WorkflowExecutionTargetModeCustomRef},
		{Mode: WorkflowExecutionTargetModeCustomRef, CustomRef: &blankRef},
		{Mode: WorkflowExecutionTargetModeHead, CustomRef: &customRef},
		{Mode: WorkflowExecutionTargetMode("future")},
	} {
		if err := (WorkflowTaskStartRequest{TaskID: "task", SetupOperationID: NewWorktreeSetupOperationID(), ExecutionTarget: &selection}).Validate(); err == nil {
			t.Fatalf("selection %#v validated", selection)
		}
	}
}

func TestWorkflowGraphMetadataExecutionTargetPolicyValidation(t *testing.T) {
	customRef := "refs/tags/v1"
	if err := (WorkflowGraphSavePreviewRequest{
		WorkflowID:      "workflow",
		ExpectedVersion: 1,
		Metadata: &WorkflowGraphMetadata{
			Name:                  "Workflow",
			ExecutionTargetPolicy: &WorkflowExecutionTargetConfiguration{Mode: WorkflowExecutionTargetModeCustomRef, CustomRef: &customRef},
		},
	}).Validate(); err != nil {
		t.Fatalf("custom target policy metadata rejected: %v", err)
	}
	if err := (WorkflowGraphSavePreviewRequest{
		WorkflowID:      "workflow",
		ExpectedVersion: 1,
		Metadata: &WorkflowGraphMetadata{
			Name:                  "Workflow",
			ExecutionTargetPolicy: &WorkflowExecutionTargetConfiguration{Mode: WorkflowExecutionTargetModeHead, CustomRef: &customRef},
		},
	}).Validate(); err == nil {
		t.Fatal("non-custom policy metadata accepted a custom ref")
	}
}

func TestWorkflowExecutionTargetSelectionRequirementValidation(t *testing.T) {
	configured := WorkflowExecutionTargetSelectionRequirement{
		Reason: WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable,
		ConfiguredTarget: &WorkflowExecutionTargetConfiguredTarget{
			Mode: WorkflowExecutionTargetModeDefaultBranch,
		},
		UnavailableCause: WorkflowExecutionTargetUnavailableCauseDefaultBranchMissing,
	}
	if err := configured.Validate(); err != nil {
		t.Fatalf("configured requirement invalid: %v", err)
	}

	for _, requirement := range []WorkflowExecutionTargetSelectionRequirement{
		{Reason: WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection, UnavailableCause: WorkflowExecutionTargetUnavailableCauseGitFailure},
		{Reason: WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable},
		{Reason: WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable, ConfiguredTarget: configured.ConfiguredTarget, UnavailableCause: WorkflowExecutionTargetUnavailableCause("future")},
		{Reason: WorkflowExecutionTargetSelectionReason("future")},
	} {
		if err := requirement.Validate(); err == nil {
			t.Fatalf("invalid requirement %#v validated", requirement)
		}
	}
}

func TestWorkflowExecutionTargetActionResponsesAreOneOf(t *testing.T) {
	startApplied := WorkflowTaskStartApplied{TransitionID: "transition", PlacementID: "placement", RunID: "run"}
	requirement := WorkflowExecutionTargetSelectionRequirement{
		Reason: WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection,
	}
	for _, response := range []interface{ Validate() error }{
		WorkflowTaskStartResponse{Outcome: WorkflowExecutionTargetActionOutcomeApplied, Applied: &startApplied},
		WorkflowTaskStartResponse{Outcome: WorkflowExecutionTargetActionOutcomeSelectionRequired, SelectionRequired: &requirement},
		WorkflowTaskApproveResponse{Outcome: WorkflowExecutionTargetActionOutcomeApplied, Applied: &WorkflowTaskApproveApplied{TransitionID: "transition", TaskID: "task", State: "applied"}},
		WorkflowTaskMoveResponse{Outcome: WorkflowExecutionTargetActionOutcomeSelectionRequired, SelectionRequired: &requirement},
	} {
		if err := response.Validate(); err != nil {
			t.Fatalf("%T valid response rejected: %v", response, err)
		}
	}
	if err := (WorkflowTaskStartResponse{Outcome: WorkflowExecutionTargetActionOutcomeApplied, Applied: &startApplied, SelectionRequired: &requirement}).Validate(); err == nil {
		t.Fatal("multi-branch response validated")
	}
	if err := (WorkflowTaskStartResponse{Outcome: WorkflowExecutionTargetActionOutcomeSelectionRequired, Applied: &startApplied}).Validate(); err == nil {
		t.Fatal("mismatched response branch validated")
	}
}

func TestWorkflowExecutionTargetActionResponsesExposeOnlyDiscriminatedPayloads(t *testing.T) {
	responseTypes := []reflect.Type{
		reflect.TypeOf(WorkflowTaskStartResponse{}),
		reflect.TypeOf(WorkflowTaskApproveResponse{}),
		reflect.TypeOf(WorkflowTaskMoveResponse{}),
	}
	for _, responseType := range responseTypes {
		for _, legacyField := range []string{"TransitionID", "PlacementID", "RunID", "TaskID", "State", "PlacementIDs", "RunIDs", "ApprovalError"} {
			if _, exists := responseType.FieldByName(legacyField); exists {
				t.Fatalf("%s still exposes legacy top-level field %s", responseType.Name(), legacyField)
			}
		}
	}
}

func TestWorkflowExecutionTargetDetailAndExplicitRefErrorEncoding(t *testing.T) {
	effectiveRoot := "/worktree"
	target := WorkflowExecutionTarget{
		Mode:          WorkflowExecutionTargetModeCustomRef,
		EffectiveRoot: &effectiveRoot,
		RequestedRef:  stringPointer("release/v1"),
		ResolvedRef:   stringPointer("refs/remotes/origin/release/v1"),
		CommitOID:     stringPointer("0123456789abcdef"),
		Provenance:    WorkflowExecutionTargetProvenanceResolved,
		CurrentBranch: stringPointer("operator-renamed"),
		ManagedWorktree: &WorktreeView{
			CanonicalRoot: "/worktree",
		},
	}
	if err := target.Validate(); err != nil {
		t.Fatalf("target invalid: %v", err)
	}
	legacy := target
	legacy.Provenance = WorkflowExecutionTargetProvenanceLegacyObserved
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy-observed target invalid: %v", err)
	}
	data, err := json.Marshal(WorkflowTaskDetail{ExecutionTarget: &target})
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	if string(data) == "" || !jsonFieldPresent(t, data, "execution_target") {
		t.Fatalf("detail JSON = %s", data)
	}

	sourceRoot := "/source"
	none := WorkflowExecutionTarget{Mode: WorkflowExecutionTargetModeNone, EffectiveRoot: &sourceRoot, Provenance: WorkflowExecutionTargetProvenanceResolved}
	noneData, err := json.Marshal(none)
	if err != nil {
		t.Fatalf("marshal none target: %v", err)
	}
	for _, forbidden := range []string{"requested_ref", "resolved_ref", "commit_oid", "current_branch", "managed_worktree"} {
		if jsonFieldPresent(t, noneData, forbidden) {
			t.Fatalf("none target JSON unexpectedly includes %q: %s", forbidden, noneData)
		}
	}

	resolutionErr := &WorkflowExecutionTargetResolutionError{Code: WorkflowExecutionTargetResolutionErrorInvalidRevision, RequestedRef: "not-a-ref"}
	data = resolutionErr.RPCErrorData()
	decoded := DecodeWorkflowExecutionTargetResolutionError(data, "fallback")
	var typed *WorkflowExecutionTargetResolutionError
	if !errors.As(decoded, &typed) || typed.Code != WorkflowExecutionTargetResolutionErrorInvalidRevision || typed.RequestedRef != "not-a-ref" {
		t.Fatalf("decoded explicit-ref error = %#v, want typed invalid revision", decoded)
	}
	if resolutionErr.RPCErrorCode() != protocol.ErrCodeWorkflowExecutionTargetResolution {
		t.Fatalf("resolution error code = %d, want %d", resolutionErr.RPCErrorCode(), protocol.ErrCodeWorkflowExecutionTargetResolution)
	}

	lockedErr := &WorkflowLockedExecutionTargetError{Cause: WorkflowLockedExecutionTargetCauseMissingBranch}
	lockedData := lockedErr.RPCErrorData()
	decodedLocked := DecodeWorkflowLockedExecutionTargetError(lockedData, "fallback")
	var typedLocked *WorkflowLockedExecutionTargetError
	if !errors.As(decodedLocked, &typedLocked) || typedLocked.Cause != WorkflowLockedExecutionTargetCauseMissingBranch {
		t.Fatalf("decoded locked-target error = %#v, want typed missing branch", decodedLocked)
	}
	if lockedErr.RPCErrorCode() != protocol.ErrCodeWorkflowLockedExecutionTarget {
		t.Fatalf("locked-target error code = %d, want %d", lockedErr.RPCErrorCode(), protocol.ErrCodeWorkflowLockedExecutionTarget)
	}
}

func TestWorkflowExecutionTargetManagedOperationalFactsMayBeUnavailable(t *testing.T) {
	target := WorkflowExecutionTarget{
		Mode:         WorkflowExecutionTargetModeHead,
		RequestedRef: stringPointer("HEAD"),
		CommitOID:    stringPointer("0123456789abcdef"),
		Provenance:   WorkflowExecutionTargetProvenanceResolved,
	}
	if err := target.Validate(); err != nil {
		t.Fatalf("managed target without current operational facts invalid: %v", err)
	}
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("marshal target: %v", err)
	}
	for _, unavailable := range []string{"effective_root", "current_branch", "managed_worktree"} {
		if jsonFieldPresent(t, data, unavailable) {
			t.Fatalf("unavailable operational fact %q encoded: %s", unavailable, data)
		}
	}

	root := "/worktree"
	target.EffectiveRoot = &root
	if err := target.Validate(); err == nil {
		t.Fatal("managed target accepted effective_root without managed_worktree")
	}
	target.EffectiveRoot = nil
	target.CurrentBranch = stringPointer("main")
	if err := target.Validate(); err == nil {
		t.Fatal("managed target accepted current_branch without an effective root")
	}
}

func TestWorkflowTaskDetailDoesNotDuplicateManagedWorktreeOrChangeBoardCards(t *testing.T) {
	detailType := reflect.TypeOf(WorkflowTaskDetail{})
	if _, exists := detailType.FieldByName("ManagedWorktree"); exists {
		t.Fatal("WorkflowTaskDetail still duplicates execution_target.managed_worktree")
	}
	for _, boardType := range []reflect.Type{
		reflect.TypeOf(WorkflowBoardTaskCard{}),
		reflect.TypeOf(WorkflowTaskListItem{}),
	} {
		for _, targetField := range []string{"ExecutionTarget", "ManagedWorktree"} {
			if _, exists := boardType.FieldByName(targetField); exists {
				t.Fatalf("%s unexpectedly exposes %s", boardType.Name(), targetField)
			}
		}
	}
}

func TestWorkflowTaskGetResponseValidatesExecutionTarget(t *testing.T) {
	sourceRoot := "/source"
	valid := WorkflowTaskGetResponse{Task: WorkflowTaskDetail{
		ExecutionTarget: &WorkflowExecutionTarget{
			Mode:          WorkflowExecutionTargetModeNone,
			EffectiveRoot: &sourceRoot,
			Provenance:    WorkflowExecutionTargetProvenanceResolved,
		},
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid task detail response rejected: %v", err)
	}
	invalid := valid
	blankRoot := " "
	invalid.Task.ExecutionTarget = &WorkflowExecutionTarget{
		Mode:          WorkflowExecutionTargetModeNone,
		EffectiveRoot: &blankRoot,
		Provenance:    WorkflowExecutionTargetProvenanceResolved,
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("task detail response accepted an invalid execution target")
	}
}

func stringPointer(value string) *string {
	return &value
}

func jsonFieldPresent(t *testing.T, data []byte, field string) bool {
	t.Helper()
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	_, ok := value[field]
	return ok
}
