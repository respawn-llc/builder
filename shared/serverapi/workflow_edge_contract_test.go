package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
)

func TestWorkflowEdgeContractRoundTripsSelectionModesAndParameterPurposes(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	selected := WorkflowGraphDraftEdge{
		ID:                "edge-select",
		TransitionGroupID: "group",
		Key:               "select",
		TargetNodeID:      "agent",
		ContextMode:       "new_session",
		AssigneeSelection: "previous_node",
		ThinkingSelection: "previous_node",
		Parameters: []WorkflowParameter{
			{Key: "role", Description: "", Purpose: "target_assignee"},
			{Key: "thinking", Description: "Choose.", Purpose: "target_thinking"},
			{Key: "summary", Description: "Summary.", Purpose: "ordinary"},
		},
	}
	fallback := WorkflowGraphDraftEdge{
		ID:                "edge-fallback",
		TransitionGroupID: "group",
		Key:               "fallback",
		TargetNodeID:      "agent",
		ContextMode:       "new_session",
		AssigneeSelection: "configured",
		ThinkingSelection: "configured",
	}
	original := WorkflowGraphDraft{Edges: []WorkflowGraphDraftEdge{selected, fallback}}
	payload, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundTrip WorkflowGraphDraft
	if err := json.Unmarshal(payload, &roundTrip); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(roundTrip.Edges) != 2 {
		t.Fatalf("edge count changed across JSON round trip: %#v", roundTrip.Edges)
	}
	if roundTrip.Edges[0].AssigneeSelection != selected.AssigneeSelection || roundTrip.Edges[0].ThinkingSelection != selected.ThinkingSelection {
		t.Fatalf("selected edge modes changed across JSON round trip: %#v", roundTrip.Edges[0])
	}
	if len(roundTrip.Edges[0].Parameters) != len(selected.Parameters) || roundTrip.Edges[0].Parameters[0].Purpose != "target_assignee" {
		t.Fatalf("parameter purposes changed across JSON round trip: %#v", roundTrip.Edges[0].Parameters)
	}
	if roundTrip.Edges[1].AssigneeSelection != fallback.AssigneeSelection || len(roundTrip.Edges[1].Parameters) != 0 {
		t.Fatalf("fallback edge state changed across JSON round trip: %#v", roundTrip.Edges[1])
	}
	_ = workflowID
}

func TestWorkflowEdgeRequestsValidateSelectionModesAndParameterPurposes(t *testing.T) {
	request := WorkflowEdgeAddRequest{
		WorkflowID:        runtimeids.NewWorkflowID(),
		TransitionGroupID: "group",
		Key:               "select",
		TargetNodeID:      "agent",
		ContextMode:       "new_session",
		AssigneeSelection: "invalid",
		ThinkingSelection: "configured",
		Parameters: []WorkflowParameter{{
			Key: "role", Purpose: "target_assignee",
		}},
	}
	if err := request.Validate(); err == nil {
		t.Fatal("Validate accepted an invalid assignee selection mode")
	}

	request.AssigneeSelection = "previous_node"
	request.Parameters[0].Purpose = "invalid"
	if err := request.Validate(); err == nil {
		t.Fatal("Validate accepted an invalid parameter purpose")
	}
}

func TestWorkflowEdgeRequestsRejectBlankSelectionModesAndPurposes(t *testing.T) {
	request := WorkflowEdgeAddRequest{
		WorkflowID:        runtimeids.NewWorkflowID(),
		TransitionGroupID: "group",
		Key:               "select",
		TargetNodeID:      "agent",
		ContextMode:       "new_session",
		AssigneeSelection: "",
		ThinkingSelection: "configured",
		Parameters: []WorkflowParameter{{
			Key:         "summary",
			Description: "Summary.",
			Purpose:     "",
		}},
	}
	if err := request.Validate(); err == nil {
		t.Fatal("Validate accepted blank selection mode and parameter purpose")
	}
}
