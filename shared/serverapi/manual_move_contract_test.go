package serverapi

import (
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowTaskMovePreviewResponseValidatesEachOutcome(t *testing.T) {
	currentNodes := []WorkflowTaskCurrentNode{{NodeID: "current"}}
	resolved := "plan output"
	choice := WorkflowTaskMovePreviewTransitionChoice{
		ChoiceKey:             "group-plan-next",
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

func TestWorkflowTaskMovePreviewResponseRejectsDuplicateChoiceKeys(t *testing.T) {
	choice := WorkflowTaskMovePreviewTransitionChoice{
		ChoiceKey:             "group-next",
		TransitionKey:         "next",
		Label:                 "Next",
		SourceNodeDisplayName: "Plan",
		RequiredValues:        []WorkflowTaskMoveRequiredValue{},
	}
	response := WorkflowTaskMovePreviewResponse{
		Outcome: WorkflowTaskMovePreviewOutcomeTransition,
		Transition: &WorkflowTaskMovePreviewTransition{
			Choices: []WorkflowTaskMovePreviewTransitionChoice{
				choice,
				{ChoiceKey: choice.ChoiceKey, TransitionKey: "alternate", Label: "Alternate", SourceNodeDisplayName: "Review", RequiredValues: []WorkflowTaskMoveRequiredValue{}},
			},
		},
	}
	if err := response.Validate(); err == nil {
		t.Fatal("duplicate ChoiceKeys validated")
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
	if _, exists := requestType.FieldByName("TransitionChoiceKey"); !exists {
		t.Fatal("manual move request does not expose a unique transition choice key")
	}
	if _, exists := requestType.FieldByName("Values"); !exists {
		t.Fatal("manual move request does not expose structured values")
	}

	transitionKey := "plan-to-implement"
	request := WorkflowTaskMoveRequest{
		SetupOperationID: NewWorktreeSetupOperationID(),
		TaskID:           "task",
		TargetNodeID:     "implement",
		TransitionKey:    &transitionKey,
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
			SetupOperationID: NewWorktreeSetupOperationID(),
			TaskID:           "task",
			TargetNodeID:     "implement",
			TransitionKey:    &transitionKey,
			Values:           map[string]map[string]string{"plan": {"summary": " "}},
		},
		{
			SetupOperationID: NewWorktreeSetupOperationID(),
			TaskID:           "task",
			TargetNodeID:     "implement",
			Values:           map[string]map[string]string{"": {"summary": "value"}},
		},
		{
			SetupOperationID: NewWorktreeSetupOperationID(),
			TaskID:           "task",
			TargetNodeID:     "implement",
			Values:           map[string]map[string]string{"plan": {"": "value"}},
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
		SetupOperationID: NewWorktreeSetupOperationID(),
		TaskID:           "task",
		TargetNodeID:     "implement",
		TransitionKey:    &transitionKey,
		Values: map[string]map[string]string{
			"plan": {"summary": strings.Repeat("x", MaxWorkflowOutputValueBytes+1)},
		},
	}
	if err := request.Validate(); err == nil {
		t.Fatal("oversized structured move value validated")
	}
}
