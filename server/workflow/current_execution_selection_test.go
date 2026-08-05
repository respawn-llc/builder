package workflow

import "testing"

func TestAgentExecutionSelectionCanonicalizesAssigneeThinkingAndOrigin(t *testing.T) {
	thinking, err := NewThinkingValue("  high  ")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	selection, err := NewAgentExecutionSelection(
		"  reviewer  ",
		&thinking,
		AssigneeOriginTransitionSelected,
	)
	if err != nil {
		t.Fatalf("NewAgentExecutionSelection: %v", err)
	}
	if selection.Assignee != "reviewer" {
		t.Fatalf("assignee = %q, want reviewer", selection.Assignee)
	}
	if selection.Thinking == nil || *selection.Thinking != ThinkingValue("high") {
		t.Fatalf("thinking = %#v, want high", selection.Thinking)
	}
	if selection.Origin != AssigneeOriginTransitionSelected {
		t.Fatalf("origin = %q, want transition_selected", selection.Origin)
	}
}

func TestAgentExecutionSelectionRequiresCompleteAssigneeAndOrigin(t *testing.T) {
	thinking, err := NewThinkingValue("high")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	tests := []struct {
		name   string
		role   string
		origin AssigneeOrigin
	}{
		{name: "blank assignee", origin: AssigneeOriginConfiguredFallback},
		{name: "blank origin", role: "reviewer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewAgentExecutionSelection(test.role, &thinking, test.origin); err == nil {
				t.Fatal("NewAgentExecutionSelection succeeded")
			}
		})
	}
}

func TestCurrentNodeWithAgentExecutionSelectionClonesSelection(t *testing.T) {
	reference, err := NewCurrentNodeReference("task-1", "node-agent", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	selection, err := NewAgentExecutionSelection("reviewer", nil, AssigneeOriginConfiguredFallback)
	if err != nil {
		t.Fatalf("NewAgentExecutionSelection: %v", err)
	}
	currentNode, err := NewCurrentNodeWithExecutionSelection(
		reference,
		nil,
		MaterializedPriorValues{},
		nil,
		nil,
		&selection,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeWithExecutionSelection: %v", err)
	}
	selection.Assignee = "mutated"
	if currentNode.AgentExecutionSelection == nil || currentNode.AgentExecutionSelection.Assignee != "reviewer" {
		t.Fatalf("current node selection = %#v, want cloned reviewer selection", currentNode.AgentExecutionSelection)
	}
}
