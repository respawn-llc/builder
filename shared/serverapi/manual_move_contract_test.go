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
			Description:   manualMoveStringPointer("Plan summary"),
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

func TestWorkflowTaskMoveRequiredValueAllowsAbsentDescriptionAndMarshalsNull(t *testing.T) {
	resolved := "plan output"
	response := WorkflowTaskMovePreviewResponse{
		Outcome: WorkflowTaskMovePreviewOutcomeTransition,
		Transition: &WorkflowTaskMovePreviewTransition{Choices: []WorkflowTaskMovePreviewTransitionChoice{{
			TransitionKey:         "plan-to-implement",
			Label:                 "Implement",
			SourceNodeDisplayName: "Plan",
			RequiredValues: []WorkflowTaskMoveRequiredValue{{
				NodeKey:       "plan",
				OutputName:    "summary",
				Description:   nil,
				ResolvedValue: &resolved,
			}},
		}}},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("absent description response rejected: %v", err)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal preview response: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	var transition map[string]json.RawMessage
	if err := json.Unmarshal(object["transition"], &transition); err != nil {
		t.Fatalf("decode transition: %v", err)
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(transition["choices"], &choices); err != nil {
		t.Fatalf("decode transition choices: %v", err)
	}
	var requiredValues []map[string]json.RawMessage
	if err := json.Unmarshal(choices[0]["required_values"], &requiredValues); err != nil {
		t.Fatalf("decode required values: %v", err)
	}
	description, present := requiredValues[0]["description"]
	if !present {
		t.Fatal("description member is absent")
	}
	var descriptionValue any
	if err := json.Unmarshal(description, &descriptionValue); err != nil {
		t.Fatalf("decode description member: %v", err)
	}
	if descriptionValue != nil {
		t.Fatalf("description member = %v, want JSON null", descriptionValue)
	}
}

func TestWorkflowTaskMoveRequiredValueAcceptsNonBlankDescription(t *testing.T) {
	value := WorkflowTaskMoveRequiredValue{
		NodeKey:     "plan",
		OutputName:  "summary",
		Description: manualMoveStringPointer("Plan summary"),
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("nonblank description rejected: %v", err)
	}
}

func TestWorkflowTaskMoveRequiredValueRejectsBlankDescription(t *testing.T) {
	for _, description := range []string{"", " \t\n"} {
		value := WorkflowTaskMoveRequiredValue{
			NodeKey:     "plan",
			OutputName:  "summary",
			Description: manualMoveStringPointer(description),
		}
		if err := value.Validate(); err == nil {
			t.Fatalf("blank description %q validated", description)
		}
	}
}

func manualMoveStringPointer(value string) *string {
	return &value
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
			"plan": {"summary": strings.Repeat("x", workflowcontract.MaxOutputValueBytes+1)},
		},
	}
	if err := request.Validate(); err == nil {
		t.Fatal("oversized structured move value validated")
	}
}
