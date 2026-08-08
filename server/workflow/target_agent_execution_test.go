package workflow_test

import (
	"testing"

	"core/server/workflow"
)

func TestPlanTargetAgentExecutionSelectionClearsRetainedThinkingForFiniteNoThinkingRole(t *testing.T) {
	previousThinking, err := workflow.NewThinkingValue("high")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	retained, err := workflow.NewAgentExecutionSelection(
		"reviewer",
		&previousThinking,
		workflow.AssigneeOriginRetainedSession,
	)
	if err != nil {
		t.Fatalf("NewAgentExecutionSelection: %v", err)
	}
	selection, err := workflow.PlanTargetAgentExecutionSelection(workflow.TargetAgentExecutionSelectionRequest{
		Edge: workflow.Edge{
			ThinkingSelection: workflow.ThinkingSelectionPreviousNode,
		},
		Catalog: targetAgentCatalogStub{
			fallback: map[string]workflow.TargetAgentRole{
				"reviewer": {
					Identity: "reviewer",
					Thinking: workflow.ThinkingCapability{
						ReasoningCapable: true,
						Finite:           true,
					},
				},
			},
		},
		RetainedSession: &retained,
	})
	if err != nil {
		t.Fatalf("PlanTargetAgentExecutionSelection: %v", err)
	}
	if selection.Thinking != nil {
		t.Fatalf("retained thinking = %v, want explicit no-thinking materialization", *selection.Thinking)
	}
}

func TestPlanTargetAgentExecutionSelectionMaterializesRetainedConfiguredThinking(t *testing.T) {
	retained, err := workflow.NewAgentExecutionSelection("reviewer", nil, workflow.AssigneeOriginRetainedSession)
	if err != nil {
		t.Fatalf("NewAgentExecutionSelection: %v", err)
	}
	selection, err := workflow.PlanTargetAgentExecutionSelection(workflow.TargetAgentExecutionSelectionRequest{
		Catalog: targetAgentCatalogStub{
			fallback: map[string]workflow.TargetAgentRole{
				"reviewer": {
					Identity:           "reviewer",
					ConfiguredThinking: "high",
				},
			},
		},
		RetainedSession: &retained,
	})
	if err != nil {
		t.Fatalf("PlanTargetAgentExecutionSelection: %v", err)
	}
	if selection.Thinking == nil || *selection.Thinking != workflow.ThinkingValue("high") {
		t.Fatalf("retained configured thinking = %#v, want high", selection.Thinking)
	}
}
