package workflow_test

import (
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
)

type selectorCatalog struct {
	configured map[string]workflow.TargetAgentRole
	explicit   []workflow.TargetAgentRole
}

func (c selectorCatalog) ResolveConfiguredRole(role string) (workflow.TargetAgentRole, bool) {
	value, ok := c.configured[role]
	return value, ok
}

func (c selectorCatalog) ExplicitCallableRoles() []workflow.TargetAgentRole {
	return append([]workflow.TargetAgentRole(nil), c.explicit...)
}

func TestSelectorApplicabilityIsSemanticAndDraftSaveRemainsNonBlocking(t *testing.T) {
	def := validWorkflow(t)
	edge := edgeByIDForValidationTest(t, &def, "edge_done")
	edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
	edge.Parameters = []workflow.Parameter{{Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee}}

	draft := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context:      workflow.ValidationContextDraft,
		RoleResolver: selectorCatalog{configured: map[string]workflow.TargetAgentRole{"coder": {Identity: "coder", QuestionsEnabled: true}}},
	})
	assertHasCodes(t, draft, workflow.CodeAssigneeSelectionInapplicable)
	if draft.HasBlockingErrors() {
		t.Fatalf("draft selector applicability errors block save: %+v", draft.Errors)
	}

	task := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context:      workflow.ValidationContextTaskCreation,
		RoleResolver: selectorCatalog{configured: map[string]workflow.TargetAgentRole{"coder": {Identity: "coder", QuestionsEnabled: true}}},
	})
	assertHasCodes(t, task, workflow.CodeAssigneeSelectionInapplicable)
	if !task.HasBlockingErrors() {
		t.Fatalf("task selector applicability errors did not block creation: %+v", task.Errors)
	}
}

func TestSelectorApplicabilityRejectsFanoutAndUnavailableCatalogSemantically(t *testing.T) {
	def := serialAgentTargetWorkflow(t)
	edge := edgeByIDForValidationTest(t, &def, "edge_done")
	edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
	edge.Parameters = []workflow.Parameter{{Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee}}
	edge.ThinkingSelection = workflow.ThinkingSelectionPreviousNode
	edge.Parameters = append(edge.Parameters, workflow.Parameter{Key: "thinking", Purpose: workflow.ParameterPurposeTargetThinking})

	unavailable := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context: workflow.ValidationContextTaskCreation,
		RoleResolver: selectorCatalog{
			configured: map[string]workflow.TargetAgentRole{"coder": {Identity: "coder", QuestionsEnabled: true}},
		},
	})
	assertHasCodes(t, unavailable, workflow.CodeAssigneeSelectionUnavailable)
	assertHasCodes(t, unavailable, workflow.CodeThinkingSelectionUnavailable)

	fanout := fanoutWorkflow(t)
	fanoutEdge := edgeByIDForValidationTest(t, &fanout, "edge_split_a")
	fanoutEdge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
	fanoutEdge.ThinkingSelection = workflow.ThinkingSelectionPreviousNode
	fanoutEdge.Parameters = []workflow.Parameter{
		{Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee},
		{Key: "thinking", Purpose: workflow.ParameterPurposeTargetThinking},
	}
	fanoutResult := workflow.ValidateDefinition(fanout, workflow.ValidationOptions{
		Context: workflow.ValidationContextTaskCreation,
		RoleResolver: selectorCatalog{
			configured: map[string]workflow.TargetAgentRole{"coder": {Identity: "coder", QuestionsEnabled: true}},
			explicit:   []workflow.TargetAgentRole{{Identity: "coder", ExplicitAgentCallable: true, Thinking: workflow.ThinkingCapability{ReasoningCapable: true, Finite: true, Levels: []string{"low", "high"}}}},
		},
	})
	assertHasCodes(t, fanoutResult, workflow.CodeAssigneeSelectionInapplicable)
	assertHasCodes(t, fanoutResult, workflow.CodeThinkingSelectionInapplicable)
}

func TestAssigneeSelectionIsInapplicableToOtherContinueContextSources(t *testing.T) {
	def := serialAgentTargetWorkflow(t)
	edge := edgeByIDForValidationTest(t, &def, "edge_done")
	edge.ContextMode = workflow.ContextModeContinueSession
	edge.ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource}
	edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
	edge.Parameters = []workflow.Parameter{{Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee}}
	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context: workflow.ValidationContextTaskCreation,
		RoleResolver: selectorCatalog{
			configured: map[string]workflow.TargetAgentRole{"coder": {Identity: "coder", QuestionsEnabled: true}},
			explicit:   []workflow.TargetAgentRole{{Identity: "coder", ExplicitAgentCallable: true}},
		},
	})
	assertHasCodes(t, result, workflow.CodeAssigneeSelectionInapplicable)
}

func serialAgentTargetWorkflow(t *testing.T) workflow.Definition {
	def := validWorkflow(t)
	workflowID := def.ID
	def.Nodes = append(def.Nodes, testAgentNode(workflowID, "node_review", "review", "Review", workflow.NodeFields{
		SubagentRole: "coder",
	}))
	edge := edgeByIDForValidationTest(t, &def, "edge_done")
	edge.TargetNodeID = "node_review"
	def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{
		WorkflowID: workflowID, ID: "group_review_done", SourceNodeID: "node_review", TransitionID: "finish", DisplayName: "Finish",
	})
	def.Edges = append(def.Edges, workflow.Edge{
		WorkflowID: workflowID, ID: "edge_review_done", Key: "done", TransitionGroupID: "group_review_done",
		TargetNodeID: "node_done", ContextMode: workflow.ContextModeNewSession,
	})
	return normalizeWorkflowEdgeShape(def)
}

var _ = testsetup.WorkflowID
