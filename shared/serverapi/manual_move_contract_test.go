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
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode move request fields: %v", err)
	}
	if _, exists := fields["setup_operation_id"]; exists {
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

func TestWorkflowTaskMoveResponsesEmitExplicitNullRetainedPreviousWorktree(t *testing.T) {
	for name, response := range map[string]WorkflowTaskMoveResponse{
		"applied": {
			Outcome: WorkflowExecutionTargetActionOutcomeApplied,
			Applied: &WorkflowTaskMoveApplied{
				CurrentNodes: []WorkflowTaskCurrentNode{{NodeID: "node"}},
			},
		},
		"no op": {
			Outcome: WorkflowExecutionTargetActionOutcomeNoOp,
			NoOp: &WorkflowTaskMoveNoOp{
				CurrentNodes: []WorkflowTaskCurrentNode{{NodeID: "node"}},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("marshal Move response: %v", err)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatalf("decode Move response: %v", err)
			}
			payloadName := "applied"
			if response.Outcome == WorkflowExecutionTargetActionOutcomeNoOp {
				payloadName = "no_op"
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(envelope[payloadName], &payload); err != nil {
				t.Fatalf("decode %s payload: %v", payloadName, err)
			}
			if got := string(payload["retained_previous_worktree"]); got != "null" {
				t.Fatalf("retained_previous_worktree = %s, want explicit null: %s", got, raw)
			}
		})
	}
}
