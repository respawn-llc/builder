package serverapi

import (
	"encoding/json"
	"slices"
	"testing"

	"core/shared/runtimeids"
)

func TestWorkflowGraphEntityReferenceComparisonDefinesCanonicalOrder(t *testing.T) {
	references := []WorkflowGraphEntityReference{
		{EntityType: WorkflowGraphEntityTypeTransitionGroup, EntityID: "transition-1"},
		{EntityType: WorkflowGraphEntityTypeNode, EntityID: "node-2"},
		{EntityType: WorkflowGraphEntityTypeEdge, EntityID: "edge-1"},
		{EntityType: WorkflowGraphEntityTypeNodeGroup, EntityID: "group-1"},
		{EntityType: WorkflowGraphEntityTypeNode, EntityID: "node-1"},
	}

	slices.SortFunc(references, CompareWorkflowGraphEntityReferences)

	want := []WorkflowGraphEntityReference{
		{EntityType: WorkflowGraphEntityTypeEdge, EntityID: "edge-1"},
		{EntityType: WorkflowGraphEntityTypeNode, EntityID: "node-1"},
		{EntityType: WorkflowGraphEntityTypeNode, EntityID: "node-2"},
		{EntityType: WorkflowGraphEntityTypeNodeGroup, EntityID: "group-1"},
		{EntityType: WorkflowGraphEntityTypeTransitionGroup, EntityID: "transition-1"},
	}
	if !slices.Equal(references, want) {
		t.Fatalf("canonical order = %+v, want %+v", references, want)
	}
}

func TestWorkflowGraphSaveResponseValidatesExactRemovedEntities(t *testing.T) {
	response := WorkflowGraphSavePreviewResponse{
		CurrentVersion: 4,
		Changed:        true,
		Impact: WorkflowGraphSaveImpact{
			RemovedNodeGroupCount: 1,
			RemovedEdgeCount:      1,
			RemovedEntities: []WorkflowGraphEntityReference{
				{EntityType: WorkflowGraphEntityTypeEdge, EntityID: "edge-1"},
				{EntityType: WorkflowGraphEntityTypeNodeGroup, EntityID: "group-1"},
			},
		},
		Blockers: []WorkflowGraphSaveBlocker{},
	}

	if err := response.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	first, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal first response: %v", err)
	}
	second, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal second response: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("JSON projection is not deterministic:\nfirst:  %s\nsecond: %s", first, second)
	}
}

func TestWorkflowGraphSaveResponseRejectsInvalidEntityImpact(t *testing.T) {
	validImpact := WorkflowGraphSaveImpact{RemovedEntities: []WorkflowGraphEntityReference{}}
	tests := []struct {
		name     string
		response WorkflowGraphSavePreviewResponse
	}{
		{
			name:     "missing removed entities",
			response: WorkflowGraphSavePreviewResponse{},
		},
		{
			name: "unknown entity type",
			response: WorkflowGraphSavePreviewResponse{Impact: WorkflowGraphSaveImpact{
				RemovedEntities: []WorkflowGraphEntityReference{{EntityType: "unknown", EntityID: "entity-1"}},
			}},
		},
		{
			name: "aggregate count mismatch",
			response: WorkflowGraphSavePreviewResponse{Impact: WorkflowGraphSaveImpact{
				RemovedEdgeCount: 1,
				RemovedEntities:  []WorkflowGraphEntityReference{},
			}},
		},
		{
			name: "non-canonical entity order",
			response: WorkflowGraphSavePreviewResponse{Impact: WorkflowGraphSaveImpact{
				RemovedNodeCount: 1,
				RemovedEdgeCount: 1,
				RemovedEntities: []WorkflowGraphEntityReference{
					{EntityType: WorkflowGraphEntityTypeNode, EntityID: "node-1"},
					{EntityType: WorkflowGraphEntityTypeEdge, EntityID: "edge-1"},
				},
			}},
		},
		{
			name: "missing blocker affected entities",
			response: WorkflowGraphSavePreviewResponse{
				Impact: validImpact,
				Blockers: []WorkflowGraphSaveBlocker{{
					Code:    "confirmation_required",
					Message: "confirmation required",
					Count:   1,
				}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.response.Validate(); err == nil {
				t.Fatalf("Validate accepted invalid response: %+v", test.response)
			}
		})
	}
}

func TestWorkflowGraphSaveRequestRejectsNegativeNodeGroupConfirmationCount(t *testing.T) {
	request := WorkflowGraphSaveRequest{
		WorkflowID: runtimeids.NewWorkflowID(),
		Graph: WorkflowGraphDraft{
			NodeGroups:       []WorkflowGraphDraftNodeGroup{},
			Nodes:            []WorkflowGraphDraftNode{},
			TransitionGroups: []WorkflowGraphDraftTransitionGroup{},
			Edges:            []WorkflowGraphDraftEdge{},
		},
		Confirmation: &WorkflowGraphSaveConfirmation{
			ExpectedRemovedNodeGroupCount: -1,
		},
	}

	if err := request.Validate(); !hasWorkflowRequestError(err, "expected_removed_node_group_count", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("Validate error = %v, want expected_removed_node_group_count invalid-value error", err)
	}
}
