package serverapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"core/shared/workflowcontract"
)

func TestWorkflowTaskMovePreviewResponseValidatesEachOutcome(t *testing.T) {
	currentNodes := []WorkflowTaskCurrentNode{{NodeID: "current"}}
	resolved := "plan output"
	choice := WorkflowTaskMovePreviewTransitionChoice{
		TransitionKey:         "plan-to-implement",
		Label:                 "Implement",
		SourceNodeDisplayName: "Plan",
		RequiredValues: []WorkflowTaskMoveRequiredValue{{
			NodeKey:       "plan",
			OutputName:    "summary",
			Description:   "Plan summary",
			ResolvedValue: &resolved,
		}},
	}

	responses := []WorkflowTaskMovePreviewResponse{
		{
			Outcome: WorkflowTaskMovePreviewOutcomeNoOp,
			NoOp:    &WorkflowTaskMovePreviewNoOp{CurrentNodes: currentNodes},
		},
		{
			Outcome: WorkflowTaskMovePreviewOutcomeDirect,
			Direct:  &WorkflowTaskMovePreviewDirect{},
		},
		{
			Outcome:    WorkflowTaskMovePreviewOutcomeTransition,
			Transition: &WorkflowTaskMovePreviewTransition{Choices: []WorkflowTaskMovePreviewTransitionChoice{choice}},
		},
		{
			Outcome: WorkflowTaskMovePreviewOutcomeBlocked,
			Blocked: &WorkflowTaskMovePreviewBlocked{Reason: WorkflowTaskMovePreviewBlockerWaitingQuestion},
		},
	}

	for _, response := range responses {
		if err := response.Validate(); err != nil {
			t.Fatalf("%s response rejected: %v", response.Outcome, err)
		}
	}
}

func TestWorkflowTaskMovePreviewResponseRejectsUnknownOrMixedDiscriminators(t *testing.T) {
	currentNodes := []WorkflowTaskCurrentNode{{NodeID: "current"}}
	invalid := []WorkflowTaskMovePreviewResponse{
		{Outcome: WorkflowTaskMovePreviewOutcome("future")},
		{
			Outcome: WorkflowTaskMovePreviewOutcomeNoOp,
			NoOp:    &WorkflowTaskMovePreviewNoOp{CurrentNodes: currentNodes},
			Direct:  &WorkflowTaskMovePreviewDirect{},
		},
		{
			Outcome: WorkflowTaskMovePreviewOutcomeBlocked,
			Blocked: &WorkflowTaskMovePreviewBlocked{Reason: WorkflowTaskMovePreviewBlocker("future")},
		},
		{
			Outcome: WorkflowTaskMovePreviewOutcomeTransition,
			Transition: &WorkflowTaskMovePreviewTransition{Choices: []WorkflowTaskMovePreviewTransitionChoice{{
				TransitionKey: "same",
				Label:         "Same",
			}}},
		},
	}

	for _, response := range invalid {
		if err := response.Validate(); err == nil {
			t.Fatalf("invalid response %#v validated", response)
		}
	}
}

func TestWorkflowTaskMoveRequestUsesStructuredTransitionValues(t *testing.T) {
	requestType := reflect.TypeOf(WorkflowTaskMoveRequest{})
	if _, exists := requestType.FieldByName("OutputValues"); exists {
		t.Fatal("manual move request still exposes flat output values")
	}
	if _, exists := requestType.FieldByName("TransitionKey"); !exists {
		t.Fatal("manual move request does not expose a transition key")
	}
	if _, exists := requestType.FieldByName("Values"); !exists {
		t.Fatal("manual move request does not expose structured values")
	}

	transitionKey := "plan-to-implement"
	request := WorkflowTaskMoveRequest{
		TaskID:        "task",
		TargetNodeID:  "implement",
		TransitionKey: &transitionKey,
		Values: map[string]map[string]string{
			"plan": {"summary": "manual plan"},
		},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("structured move request rejected: %v", err)
	}
}

func TestWorkflowTaskMoveRequestRejectsBlankStructuredValues(t *testing.T) {
	transitionKey := "plan-to-implement"
	for _, request := range []WorkflowTaskMoveRequest{
		{
			TaskID:        "task",
			TargetNodeID:  "implement",
			TransitionKey: &transitionKey,
			Values:        map[string]map[string]string{"plan": {"summary": " "}},
		},
		{
			TaskID:       "task",
			TargetNodeID: "implement",
			Values:       map[string]map[string]string{"": {"summary": "value"}},
		},
		{
			TaskID:       "task",
			TargetNodeID: "implement",
			Values:       map[string]map[string]string{"plan": {"": "value"}},
		},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("invalid structured move request %#v validated", request)
		}
	}
}

func TestWorkflowTaskMoveRequestRejectsOversizedStructuredValues(t *testing.T) {
	transitionKey := "plan-to-implement"
	request := WorkflowTaskMoveRequest{
		TaskID:        "task",
		TargetNodeID:  "implement",
		TransitionKey: &transitionKey,
		Values: map[string]map[string]string{
			"plan": {"summary": strings.Repeat("x", workflowcontract.MaxOutputValueBytes+1)},
		},
	}
	if err := request.Validate(); err == nil {
		t.Fatal("oversized structured move value validated")
	}
}

func TestWorkflowTaskMoveContractHasNoSetupCorrelation(t *testing.T) {
	requestType := reflect.TypeOf(WorkflowTaskMoveRequest{})
	if _, exists := requestType.FieldByName("SetupOperationID"); exists {
		t.Fatal("manual Move request still exposes Setup Operation correlation")
	}
	previewType := reflect.TypeOf(WorkflowTaskMovePreviewResponse{})
	if _, exists := previewType.FieldByName("SetupOperationID"); exists {
		t.Fatal("manual Move preview exposes Setup Operation correlation")
	}
	raw, err := json.Marshal(WorkflowTaskMoveRequest{TaskID: "task", TargetNodeID: "node"})
	if err != nil {
		t.Fatalf("marshal move request: %v", err)
	}
	if strings.Contains(string(raw), "setup_operation_id") {
		t.Fatalf("manual Move serialized setup correlation: %s", raw)
	}
}

func TestWorkflowTaskMoveAppliedValidatesOptionalRetainedPreviousWorktree(t *testing.T) {
	valid := WorkflowTaskMoveResponse{
		Outcome: WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &WorkflowTaskMoveApplied{
			CurrentNodes:             []WorkflowTaskCurrentNode{{NodeID: "node"}},
			RetainedPreviousWorktree: testRetainedPreviousWorktree(),
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("move applied with retained previous worktree rejected: %v", err)
	}
	invalid := valid
	invalid.Applied = &WorkflowTaskMoveApplied{
		CurrentNodes: []WorkflowTaskCurrentNode{{NodeID: "node"}},
		RetainedPreviousWorktree: &RetainedPreviousWorktree{
			Worktree: WorktreeTopologyEntry{Variant: WorktreeTopologyVariantRegistered},
		},
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("move applied accepted malformed retained previous worktree")
	}
}
