package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
)

func TestWorkflowEdgeContractRoundTripsSelectionModesAndParameterPurposes(t *testing.T) {
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
}

func TestWorkflowGraphRequestsRejectInvalidEdgeUnionValues(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	valid := WorkflowGraphDraft{Edges: []WorkflowGraphDraftEdge{{
		ID: "edge", TransitionGroupID: "group", Key: "edge", TargetNodeID: "target",
		AssigneeSelection: "configured", ThinkingSelection: "configured",
		ContextMode: "new_session",
		Parameters:  []WorkflowParameter{{Key: "summary", Description: "Summary.", Purpose: "ordinary"}},
	}}}
	validators := []struct {
		name     string
		validate func(WorkflowGraphDraft) error
	}{
		{"validate draft", func(graph WorkflowGraphDraft) error {
			return (WorkflowGraphValidateDraftRequest{
				WorkflowID: workflowID, Graph: graph,
				Modes: []WorkflowValidationMode{WorkflowValidationModeDraft},
			}).Validate()
		}},
		{"derive wiring", func(graph WorkflowGraphDraft) error {
			return (WorkflowGraphDeriveWiringRequest{WorkflowID: workflowID, Graph: graph}).Validate()
		}},
		{"preview save", func(graph WorkflowGraphDraft) error {
			return (WorkflowGraphSavePreviewRequest{WorkflowID: workflowID, Graph: graph}).Validate()
		}},
		{"save", func(graph WorkflowGraphDraft) error {
			return (WorkflowGraphSaveRequest{WorkflowID: workflowID, Graph: graph}).Validate()
		}},
	}
	invalid := []struct {
		name   string
		mutate func(*WorkflowGraphDraftEdge)
	}{
		{"invalid assignee selection", func(edge *WorkflowGraphDraftEdge) { edge.AssigneeSelection = "invalid" }},
		{"invalid thinking selection", func(edge *WorkflowGraphDraftEdge) { edge.ThinkingSelection = "invalid" }},
		{"invalid parameter purpose", func(edge *WorkflowGraphDraftEdge) { edge.Parameters[0].Purpose = "invalid" }},
	}
	for _, request := range validators {
		t.Run(request.name, func(t *testing.T) {
			for _, test := range invalid {
				t.Run(test.name, func(t *testing.T) {
					graph := valid
					graph.Edges = append([]WorkflowGraphDraftEdge(nil), valid.Edges...)
					graph.Edges[0].Parameters = append([]WorkflowParameter(nil), valid.Edges[0].Parameters...)
					test.mutate(&graph.Edges[0])
					if err := request.validate(graph); err == nil {
						t.Fatalf("%s accepted invalid edge: %+v", request.name, graph.Edges[0])
					}
				})
			}
		})
	}
}

func TestWorkflowGraphRequestEnvelopeDefersSemanticShapeToWorkflowValidation(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	graph := WorkflowGraphDraft{Edges: []WorkflowGraphDraftEdge{{
		ID:                "not-a-canonical-id",
		TransitionGroupID: "missing-group",
		Key:               "duplicate",
		TargetNodeID:      "missing-target",
		AssigneeSelection: "previous_node",
		ThinkingSelection: "configured",
		ContextMode:       "new_session",
		ContextSource:     WorkflowContextSource{Kind: "selected_node"},
		Parameters: []WorkflowParameter{
			{Key: "duplicate", Purpose: "target_assignee"},
			{Key: "duplicate", Purpose: "target_assignee"},
		},
	}}}
	for name, validate := range map[string]func() error{
		"validate draft": func() error {
			return (WorkflowGraphValidateDraftRequest{
				WorkflowID: workflowID,
				Graph:      graph,
				Modes:      []WorkflowValidationMode{WorkflowValidationModeDraft},
			}).Validate()
		},
		"derive wiring": func() error {
			return (WorkflowGraphDeriveWiringRequest{WorkflowID: workflowID, Graph: graph}).Validate()
		},
		"preview save": func() error {
			return (WorkflowGraphSavePreviewRequest{WorkflowID: workflowID, Graph: graph}).Validate()
		},
		"save": func() error {
			return (WorkflowGraphSaveRequest{WorkflowID: workflowID, Graph: graph}).Validate()
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate(); err != nil {
				t.Fatalf("envelope validation rejected semantic graph shape: %v", err)
			}
		})
	}
}
