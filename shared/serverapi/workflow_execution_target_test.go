package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
)

func TestWorkflowExecutionTargetSelectionRequestValidation(t *testing.T) {
	customRef := "refs/tags/v1"
	blankRef := " "
	valid := WorkflowExecutionTargetSelection{Mode: WorkflowExecutionTargetModeCustomRef, CustomRef: &customRef}

	for _, request := range []interface{ Validate() error }{
		WorkflowTaskStartRequest{TaskID: "task", SetupOperationID: NewWorktreeSetupOperationID(), ExecutionTarget: &valid},
		WorkflowTaskResumeRequest{TaskID: "task", SetupOperationID: NewWorktreeSetupOperationID(), ExecutionTarget: &valid},
		WorkflowTaskMoveRequest{TaskID: "task", TargetNodeID: "node", ExecutionTarget: &valid},
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
		if err := (WorkflowTaskResumeRequest{TaskID: "task", SetupOperationID: NewWorktreeSetupOperationID(), ExecutionTarget: &selection}).Validate(); err == nil {
			t.Fatalf("resume selection %#v validated", selection)
		}
		if err := (WorkflowTaskMoveRequest{TaskID: "task", TargetNodeID: "node", ExecutionTarget: &selection}).Validate(); err == nil {
			t.Fatalf("move selection %#v validated", selection)
		}
	}
}

func TestWorkflowTaskMutationRequestsValidateInvokingSession(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	for _, request := range []interface{ Validate() error }{
		WorkflowTaskStartRequest{
			TaskID:            "task",
			InvokingSessionID: &sessionID,
			SetupOperationID:  NewWorktreeSetupOperationID(),
		},
		WorkflowTaskApproveRequest{
			ApprovalID:        "approval",
			InvokingSessionID: &sessionID,
		},
		WorkflowTaskMoveRequest{
			TaskID:            "task",
			InvokingSessionID: &sessionID,
			TargetNodeID:      "node",
		},
		WorkflowTaskResumeRequest{
			TaskID:            "task",
			InvokingSessionID: &sessionID,
			SetupOperationID:  NewWorktreeSetupOperationID(),
		},
		WorkflowTaskInterruptRequest{
			TaskID:            "task",
			InvokingSessionID: &sessionID,
		},
	} {
		if err := request.Validate(); err != nil {
			t.Fatalf("%T valid invoking Session rejected: %v", request, err)
		}
	}

	zero := runtimeids.SessionID{}
	for _, request := range []interface{ Validate() error }{
		WorkflowTaskStartRequest{
			TaskID:            "task",
			InvokingSessionID: &zero,
			SetupOperationID:  NewWorktreeSetupOperationID(),
		},
		WorkflowTaskApproveRequest{
			ApprovalID:        "approval",
			InvokingSessionID: &zero,
		},
		WorkflowTaskMoveRequest{
			TaskID:            "task",
			InvokingSessionID: &zero,
			TargetNodeID:      "node",
		},
		WorkflowTaskResumeRequest{
			TaskID:            "task",
			InvokingSessionID: &zero,
			SetupOperationID:  NewWorktreeSetupOperationID(),
		},
		WorkflowTaskInterruptRequest{
			TaskID:            "task",
			InvokingSessionID: &zero,
		},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("%T accepted zero invoking Session", request)
		}
	}
}

func TestWorkflowTaskMutationSelfTargetErrorRoundTripsRPCData(t *testing.T) {
	original := &WorkflowTaskMutationSelfTargetError{TaskID: "task-1"}
	decoded := DecodeWorkflowTaskMutationSelfTargetError(original.RPCErrorData(), original.Error())
	var target *WorkflowTaskMutationSelfTargetError
	if !errors.As(decoded, &target) || target.TaskID != original.TaskID {
		t.Fatalf("decoded error = %#v, want self-target task %q", decoded, original.TaskID)
	}
	if original.RPCErrorCode() != protocol.ErrCodeWorkflowTaskMutationSelfTarget {
		t.Fatalf("RPC error code = %d", original.RPCErrorCode())
	}
}

func TestWorkflowGraphMetadataExecutionTargetPolicyValidation(t *testing.T) {
	customRef := "refs/tags/v1"
	if err := (WorkflowGraphSavePreviewRequest{
		WorkflowID:      runtimeids.NewWorkflowID(),
		ExpectedVersion: 1,
		Metadata: &WorkflowGraphMetadata{
			Name:                  "Workflow",
			ExecutionTargetPolicy: &WorkflowExecutionTargetConfiguration{Mode: WorkflowExecutionTargetModeCustomRef, CustomRef: &customRef},
		},
	}).Validate(); err != nil {
		t.Fatalf("custom target policy metadata rejected: %v", err)
	}
	if err := (WorkflowGraphSavePreviewRequest{
		WorkflowID:      runtimeids.NewWorkflowID(),
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
	currentNodes := []WorkflowTaskCurrentNode{{NodeID: "node-agent"}}
	startApplied := WorkflowTaskStartApplied{CurrentNodes: currentNodes}
	requirement := WorkflowExecutionTargetSelectionRequirement{
		Reason: WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection,
	}
	dependencyCount := 2
	for _, response := range []interface{ Validate() error }{
		WorkflowTaskStartResponse{Outcome: WorkflowTaskActionOutcomeApplied, Applied: &startApplied},
		WorkflowTaskStartResponse{Outcome: WorkflowTaskActionOutcomeSelectionRequired, SelectionRequired: &requirement},
		WorkflowTaskResumeResponse{Outcome: WorkflowExecutionTargetActionOutcomeApplied, Applied: &WorkflowTaskResumeApplied{CurrentNodes: currentNodes}},
		WorkflowTaskResumeResponse{Outcome: WorkflowExecutionTargetActionOutcomeSelectionRequired, SelectionRequired: &requirement},
		WorkflowTaskApproveResponse{Outcome: WorkflowExecutionTargetActionOutcomeApplied, Applied: &WorkflowTaskApproveApplied{TaskID: "task", CurrentNodes: currentNodes}},
		WorkflowTaskMoveResponse{Outcome: WorkflowExecutionTargetActionOutcomeNoOp, NoOp: &WorkflowTaskMoveNoOp{CurrentNodes: currentNodes}},
		WorkflowTaskMoveResponse{Outcome: WorkflowExecutionTargetActionOutcomeSelectionRequired, SelectionRequired: &requirement},
		WorkflowTaskMoveResponse{
			Outcome:                    WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired,
			UnsatisfiedDependencyCount: &dependencyCount,
		},
	} {
		if err := response.Validate(); err != nil {
			t.Fatalf("%T valid response rejected: %v", response, err)
		}
	}
	if err := (WorkflowTaskStartResponse{Outcome: WorkflowTaskActionOutcomeApplied, Applied: &startApplied, SelectionRequired: &requirement}).Validate(); err == nil {
		t.Fatal("multi-branch response validated")
	}
	if err := (WorkflowTaskStartResponse{Outcome: WorkflowTaskActionOutcomeSelectionRequired, Applied: &startApplied}).Validate(); err == nil {
		t.Fatal("mismatched response branch validated")
	}
	if err := (WorkflowTaskMoveResponse{
		Outcome:           WorkflowExecutionTargetActionOutcomeNoOp,
		NoOp:              &WorkflowTaskMoveNoOp{CurrentNodes: currentNodes},
		SelectionRequired: &requirement,
	}).Validate(); err == nil {
		t.Fatal("move no-op response with selection requirement validated")
	}
	if err := (WorkflowTaskMoveResponse{
		Outcome:                    WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired,
		UnsatisfiedDependencyCount: intPointer(0),
	}).Validate(); err == nil {
		t.Fatal("move dependency confirmation response accepted non-positive count")
	}
}

func TestWorkflowExecutionTargetActionResponsesExposeOnlyDiscriminatedPayloads(t *testing.T) {
	responseTypes := []reflect.Type{
		reflect.TypeOf(WorkflowTaskStartResponse{}),
		reflect.TypeOf(WorkflowTaskResumeResponse{}),
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

func TestWorkflowTaskMoveRequestHasNoCompatibilityFields(t *testing.T) {
	requestType := reflect.TypeOf(WorkflowTaskMoveRequest{})
	for _, removedField := range []string{"AllowMissingEdge", "AutoApprove"} {
		if _, exists := requestType.FieldByName(removedField); exists {
			t.Fatalf("%s still exposes removed compatibility field %s", requestType.Name(), removedField)
		}
	}
}

func TestWorkflowTaskActionResponseValidatesDependencyConfirmationOutcome(t *testing.T) {
	count := 2
	response := WorkflowTaskStartResponse{
		Outcome:                    WorkflowTaskActionOutcomeDependencyConfirmationRequired,
		UnsatisfiedDependencyCount: &count,
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("dependency confirmation response Validate: %v", err)
	}
	invalid := response
	invalid.UnsatisfiedDependencyCount = nil
	if err := invalid.Validate(); err == nil {
		t.Fatal("dependency confirmation response accepted missing count")
	}
}

func TestWorkflowExecutionTargetDetailAndExplicitRefErrorEncoding(t *testing.T) {
	target := WorkflowExecutionTarget{
		Mode:         WorkflowExecutionTargetModeCustomRef,
		RequestedRef: stringPointer("release/v1"),
		ResolvedRef:  stringPointer("refs/remotes/origin/release/v1"),
		CommitOID:    stringPointer("0123456789abcdef"),
		Provenance:   WorkflowExecutionTargetProvenanceResolved,
	}
	if err := target.Validate(); err != nil {
		t.Fatalf("target invalid: %v", err)
	}
	legacy := target
	legacy.Provenance = WorkflowExecutionTargetProvenanceLegacyObserved
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy-observed target invalid: %v", err)
	}
	data, err := json.Marshal(WorkflowTaskDetail{Summary: WorkflowTaskSummary{WorkflowID: runtimeids.NewWorkflowID()}, Workflow: WorkflowTaskWorkflowSummary{WorkflowID: runtimeids.NewWorkflowID()}, ExecutionTarget: &target})
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	if string(data) == "" || !jsonFieldPresent(t, data, "execution_target") {
		t.Fatalf("detail JSON = %s", data)
	}
	for _, forbidden := range []string{"effective_root", "current_branch", "managed_worktree"} {
		if jsonFieldPresent(t, mustJSONField(t, data, "execution_target"), forbidden) {
			t.Fatalf("durable execution target unexpectedly includes %q: %s", forbidden, data)
		}
	}

	none := WorkflowExecutionTarget{Mode: WorkflowExecutionTargetModeNone, Provenance: WorkflowExecutionTargetProvenanceResolved}
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

func TestWorkflowExecutionTargetContainsOnlyDurableFacts(t *testing.T) {
	target := WorkflowExecutionTarget{
		Mode:         WorkflowExecutionTargetModeHead,
		RequestedRef: stringPointer("HEAD"),
		CommitOID:    stringPointer("0123456789abcdef"),
		Provenance:   WorkflowExecutionTargetProvenanceResolved,
	}
	if err := target.Validate(); err != nil {
		t.Fatalf("durable managed target invalid: %v", err)
	}
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("marshal target: %v", err)
	}
	for _, removed := range []string{"effective_root", "current_branch", "managed_worktree"} {
		if jsonFieldPresent(t, data, removed) {
			t.Fatalf("removed operational fact %q encoded: %s", removed, data)
		}
	}
}

func TestWorkflowTaskDetailCarriesOnlyCurrentExecutionTargets(t *testing.T) {
	detailType := reflect.TypeOf(WorkflowTaskDetail{})
	for _, removedField := range []string{"Placements", "Runs", "Transitions", "Comments"} {
		if _, exists := detailType.FieldByName(removedField); exists {
			t.Fatalf("WorkflowTaskDetail still embeds %s history", removedField)
		}
	}

	data, err := json.Marshal(WorkflowTaskDetail{Summary: WorkflowTaskSummary{WorkflowID: runtimeids.NewWorkflowID()}, Workflow: WorkflowTaskWorkflowSummary{WorkflowID: runtimeids.NewWorkflowID()}})
	if err != nil {
		t.Fatalf("marshal empty detail: %v", err)
	}
	for _, requiredArray := range []string{"live_session_ids", "current_scripts"} {
		if !jsonFieldPresent(t, data, requiredArray) {
			t.Fatalf("task detail omitted required array %q: %s", requiredArray, data)
		}
	}
	workflowType := reflect.TypeOf(WorkflowTaskDetail{}.Workflow)
	for _, workflowLevelField := range []string{"ValidForTaskCreation", "ValidationErrors"} {
		if _, exists := workflowType.FieldByName(workflowLevelField); exists {
			t.Fatalf("WorkflowTaskDetail workflow still embeds workflow-level field %s", workflowLevelField)
		}
	}
}

func TestWorkflowTaskDetailKeepsRecordedWorktreePathOffBoardCards(t *testing.T) {
	detailType := reflect.TypeOf(WorkflowTaskDetail{})
	if _, exists := detailType.FieldByName("Attention"); exists {
		t.Fatal("WorkflowTaskDetail still embeds attention items")
	}
	attentionCount, exists := detailType.FieldByName("AttentionCount")
	if !exists || attentionCount.Type.Kind() != reflect.Int {
		t.Fatalf("WorkflowTaskDetail attention count contract = %v, want int", attentionCount.Type)
	}
	worktreePath, exists := detailType.FieldByName("WorktreePath")
	if !exists || worktreePath.Type != reflect.TypeOf((*string)(nil)) {
		t.Fatalf("WorkflowTaskDetail worktree path contract = %v, want *string", worktreePath.Type)
	}
	targetType := reflect.TypeOf(WorkflowExecutionTarget{})
	for _, removedField := range []string{"EffectiveRoot", "CurrentBranch", "ManagedWorktree"} {
		if _, exists := targetType.FieldByName(removedField); exists {
			t.Fatalf("WorkflowExecutionTarget still exposes %s", removedField)
		}
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
	valid := WorkflowTaskGetResponse{Task: WorkflowTaskDetail{
		Summary:        WorkflowTaskSummary{ID: "task-1"},
		CurrentNodes:   []WorkflowTaskCurrentNode{},
		LiveSessionIDs: []string{},
		CurrentScripts: []WorkflowTaskCurrentScript{},
		Dependencies:   emptyWorkflowTaskDependenciesForTest(),
		ExecutionTarget: &WorkflowExecutionTarget{
			Mode:       WorkflowExecutionTargetModeNone,
			Provenance: WorkflowExecutionTargetProvenanceResolved,
		},
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid task detail response rejected: %v", err)
	}
	invalid := valid
	invalid.Task.ExecutionTarget = &WorkflowExecutionTarget{
		Mode:         WorkflowExecutionTargetModeHead,
		RequestedRef: stringPointer("HEAD"),
		Provenance:   WorkflowExecutionTargetProvenanceResolved,
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("task detail response accepted an invalid execution target")
	}
	invalid = valid
	invalid.Task.AttentionCount = -1
	if err := invalid.Validate(); err == nil {
		t.Fatal("task detail response accepted a negative attention count")
	}

	invalid = valid
	invalid.Task.LiveSessionIDs = nil
	if err := invalid.Validate(); err == nil {
		t.Fatal("task detail response accepted a null live_session_ids collection")
	}
}

func TestWorkflowTaskGetResponseAcceptsEveryCurrentExecutionTarget(t *testing.T) {
	liveSessionIDs := make([]string, 0, 201)
	currentScripts := make([]WorkflowTaskCurrentScript, 0, 201)
	for index := range 201 {
		liveSessionIDs = append(liveSessionIDs, fmt.Sprintf("session-%03d", index))
		currentScripts = append(currentScripts, WorkflowTaskCurrentScript{
			CurrentNode: WorkflowTaskCurrentNode{NodeID: fmt.Sprintf("node-%03d", index)},
			Path:        "script",
		})
	}
	response := WorkflowTaskGetResponse{Task: WorkflowTaskDetail{
		Summary:        WorkflowTaskSummary{ID: "task-1"},
		CurrentNodes:   []WorkflowTaskCurrentNode{},
		LiveSessionIDs: liveSessionIDs,
		CurrentScripts: currentScripts,
		Dependencies:   emptyWorkflowTaskDependenciesForTest(),
	}}
	if err := response.Validate(); err != nil {
		t.Fatalf("all current execution targets rejected: %v", err)
	}
}

func mustJSONField(t *testing.T, data []byte, field string) []byte {
	t.Helper()
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	fieldValue, ok := value[field]
	if !ok {
		t.Fatalf("JSON omitted %q: %s", field, data)
	}
	return fieldValue
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
