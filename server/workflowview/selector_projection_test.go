package workflowview

import (
	"testing"

	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestWorkflowCurrentNodeProjectsMaterializedAgentSelection(t *testing.T) {
	reference, err := workflow.NewCurrentNodeReference("task-1", "agent-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	thinking, err := workflow.NewThinkingValue("max")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	selection, err := workflow.NewAgentExecutionSelection("reviewer", &thinking, workflow.AssigneeOriginTransitionSelected)
	if err != nil {
		t.Fatalf("NewAgentExecutionSelection: %v", err)
	}
	currentNode, err := workflow.NewCurrentNodeWithExecutionSelection(reference, nil, workflow.MaterializedPriorValues{}, nil, nil, &selection)
	if err != nil {
		t.Fatalf("NewCurrentNodeWithExecutionSelection: %v", err)
	}

	projected := workflowCurrentNode(currentNode)
	if projected.EffectiveAssignee == nil || *projected.EffectiveAssignee != "reviewer" {
		t.Fatalf("effective assignee = %v, want reviewer", projected.EffectiveAssignee)
	}
	if projected.EffectiveThinking == nil || *projected.EffectiveThinking != "max" {
		t.Fatalf("effective thinking = %v, want max", projected.EffectiveThinking)
	}
	if projected.NodeID == "" {
		t.Fatal("projected node id is blank")
	}
}

func TestWorkflowCurrentNodeOmitsSelectionForNonAgentProjection(t *testing.T) {
	reference, err := workflow.NewCurrentNodeReference("task-1", "script-1", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	currentNode, err := workflow.NewCurrentNode(reference, nil, nil)
	if err != nil {
		t.Fatalf("NewCurrentNode: %v", err)
	}

	projected := workflowCurrentNode(currentNode)
	if projected.EffectiveAssignee != nil || projected.EffectiveThinking != nil {
		t.Fatalf("non-agent selection fields = assignee:%v thinking:%v, want omitted", projected.EffectiveAssignee, projected.EffectiveThinking)
	}
}

func TestWorkflowDerivedEdgeWiringProjectsTypedSelectorApplicability(t *testing.T) {
	def := selectorProjectionDefinition()
	projected := DerivedWiring(def, selectorProjectionCatalog{})
	if len(projected.Edges) != 1 {
		t.Fatalf("derived edges = %d, want one", len(projected.Edges))
	}
	edge := projected.Edges[0]
	if !edge.AssigneeSelectionApplicability.Available ||
		edge.AssigneeSelectionApplicability.ParameterVisible ||
		edge.AssigneeSelectionApplicability.Reason != serverapi.WorkflowSelectorApplicabilityReasonSoleCallableRole {
		t.Fatalf("assignee applicability = %+v, want automatic sole-role selection", edge.AssigneeSelectionApplicability)
	}
	if !edge.ThinkingSelectionApplicability.Available || !edge.ThinkingSelectionApplicability.ParameterVisible || edge.ThinkingSelectionApplicability.Reason != serverapi.WorkflowSelectorApplicabilityReasonEligible {
		t.Fatalf("thinking applicability = %+v, want eligible", edge.ThinkingSelectionApplicability)
	}
}

type selectorProjectionCatalog struct{}

func (selectorProjectionCatalog) ResolveConfiguredRole(string) (workflow.TargetAgentRole, bool) {
	return workflow.TargetAgentRole{
		Identity:         "coder",
		QuestionsEnabled: true,
		Thinking:         workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}},
	}, true
}

func (selectorProjectionCatalog) ExplicitCallableRoles() []workflow.TargetAgentRole {
	return []workflow.TargetAgentRole{{
		Identity:              "reviewer",
		ExplicitAgentCallable: true,
		QuestionsEnabled:      true,
		Thinking:              workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}},
	}}
}

func selectorProjectionDefinition() workflow.Definition {
	workflowID := runtimeids.NewWorkflowID()
	return workflow.Definition{
		ID: workflowID,
		Nodes: []workflow.Node{
			workflow.AgentNode{NodeIdentity: workflow.NodeIdentity{ID: "source", WorkflowID: workflowID, Key: "source"}, SubagentRole: "coder"},
			workflow.AgentNode{NodeIdentity: workflow.NodeIdentity{ID: "target", WorkflowID: workflowID, Key: "target"}, SubagentRole: "coder"},
		},
		TransitionGroups: []workflow.TransitionGroup{{ID: "group", WorkflowID: workflowID, SourceNodeID: "source", TransitionID: "next"}},
		Edges:            []workflow.Edge{{ID: "edge", WorkflowID: workflowID, TransitionGroupID: "group", Key: "next", TargetNodeID: "target"}},
	}
}
