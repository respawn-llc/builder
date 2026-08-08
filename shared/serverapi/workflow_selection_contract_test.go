package serverapi

import (
	"encoding/json"
	"testing"
)

func TestWorkflowCurrentNodeEffectiveSelectionRoundTripsAndValidates(t *testing.T) {
	node := WorkflowTaskCurrentNode{
		NodeID:            "agent-1",
		EffectiveAssignee: workflowSelectionStringPointer("reviewer"),
		EffectiveThinking: workflowSelectionStringPointer("max"),
	}
	encoded, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded WorkflowTaskCurrentNode
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := (WorkflowTaskDetail{
		Summary:      WorkflowTaskSummary{ID: "task-1"},
		CurrentNodes: []WorkflowTaskCurrentNode{decoded},
		Dependencies: validWorkflowSelectionDependencies(),
	}).Validate(); err != nil {
		t.Fatalf("decoded response: %v", err)
	}
	if decoded.EffectiveAssignee == nil || *decoded.EffectiveAssignee != "reviewer" {
		t.Fatalf("effective assignee = %v, want reviewer", decoded.EffectiveAssignee)
	}
	if decoded.EffectiveThinking == nil || *decoded.EffectiveThinking != "max" {
		t.Fatalf("effective thinking = %v, want max", decoded.EffectiveThinking)
	}
}

func TestWorkflowTaskDetailRejectsBlankEffectiveSelectionFields(t *testing.T) {
	for _, field := range []string{"assignee", "thinking"} {
		t.Run(field, func(t *testing.T) {
			node := WorkflowTaskCurrentNode{NodeID: "agent-1"}
			if field == "assignee" {
				node.EffectiveAssignee = workflowSelectionStringPointer(" ")
			} else {
				node.EffectiveThinking = workflowSelectionStringPointer("")
			}
			if err := (WorkflowTaskDetail{
				Summary:      WorkflowTaskSummary{ID: "task-1"},
				CurrentNodes: []WorkflowTaskCurrentNode{node},
				Dependencies: validWorkflowSelectionDependencies(),
			}).Validate(); err == nil {
				t.Fatalf("blank effective %s was accepted", field)
			}
		})
	}
}

func TestWorkflowDerivedEdgeWiringRejectsUnknownApplicabilityFacts(t *testing.T) {
	edge := WorkflowDerivedEdgeWiring{
		EdgeID: "edge-1",
		AssigneeSelectionApplicability: WorkflowSelectorApplicability{
			Available:        true,
			ParameterVisible: true,
			Reason:           WorkflowSelectorApplicabilityReason("future"),
		},
		ThinkingSelectionApplicability: WorkflowSelectorApplicability{
			Available:        true,
			ParameterVisible: true,
			Reason:           WorkflowSelectorApplicabilityReasonEligible,
		},
	}
	if err := edge.Validate(); err == nil {
		t.Fatal("unknown selector applicability reason was accepted")
	}
}

func workflowSelectionStringPointer(value string) *string {
	return &value
}

func validWorkflowSelectionDependencies() WorkflowTaskDependencies {
	zero := 0
	return WorkflowTaskDependencies{
		Directions: []WorkflowTaskDependencyDirectionProjection{
			{
				Direction:        WorkflowTaskDependencyDirectionBlockedBy,
				UnsatisfiedCount: &zero,
				Items:            []WorkflowTaskDependencyItem{},
				AddAvailability:  &WorkflowTaskDependencyAddAvailability{LimitReached: &WorkflowTaskDependencyLimitReached{}},
			},
			{
				Direction:       WorkflowTaskDependencyDirectionBlocks,
				Items:           []WorkflowTaskDependencyItem{},
				AddAvailability: &WorkflowTaskDependencyAddAvailability{LimitReached: &WorkflowTaskDependencyLimitReached{}},
			},
		},
	}
}
