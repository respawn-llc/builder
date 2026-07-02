package workflow_test

import (
	"reflect"
	"testing"

	"core/server/workflow"
)

func TestDeriveWiringPropagatesTransitionParametersAcrossNormalEdge(t *testing.T) {
	def := parameterWorkflow()

	derived := workflow.DeriveWiring(def)

	if len(derived.Diagnostics) > 0 {
		t.Fatalf("expected no diagnostics, got %+v", derived.Diagnostics)
	}
	assertInputBindings(t, derived.InputBindingsForEdge("edge_plan_implement"), []workflow.InputBinding{
		{Name: "plan", Source: workflow.BindingSourceTransitionOutput, Field: "plan"},
		{Name: "risk", Source: workflow.BindingSourceTransitionOutput, Field: "risk"},
	})
	assertOutputFields(t, derived.RequiredProvisionFieldsForTransitionGroup("group_plan_implement"), []workflow.OutputField{
		{Name: "plan", Description: "Implementation plan."},
		{Name: "risk", Description: "Known implementation risk."},
	})
	assertOutputFields(t, derived.PossibleProvisionFieldsForNode("node_plan"), []workflow.OutputField{
		{Name: "plan", Description: "Implementation plan."},
		{Name: "risk", Description: "Known implementation risk."},
	})
}

func TestDeriveWiringUnionsFanoutBranchParametersPerTransitionGroup(t *testing.T) {
	def := fanoutParameterWorkflow()

	derived := workflow.DeriveWiring(def)

	if len(derived.Diagnostics) > 0 {
		t.Fatalf("expected no diagnostics, got %+v", derived.Diagnostics)
	}
	assertOutputFields(t, derived.RequiredProvisionFieldsForTransitionGroup("group_plan_split"), []workflow.OutputField{
		{Name: "plan", Description: "Implementation plan."},
		{Name: "risk", Description: "Known implementation risk."},
	})
	assertOutputFields(t, derived.PossibleProvisionFieldsForNode("node_plan"), []workflow.OutputField{
		{Name: "plan", Description: "Implementation plan."},
		{Name: "risk", Description: "Known implementation risk."},
	})
	assertInputBindings(t, derived.InputBindingsForEdge("edge_split_a"), []workflow.InputBinding{
		{Name: "plan", Source: workflow.BindingSourceTransitionOutput, Field: "plan"},
	})
	assertInputBindings(t, derived.InputBindingsForEdge("edge_split_b"), []workflow.InputBinding{
		{Name: "risk", Source: workflow.BindingSourceTransitionOutput, Field: "risk"},
	})
}

func TestDeriveWiringReportsFanoutParameterDescriptionConflict(t *testing.T) {
	def := fanoutParameterWorkflow()
	edgeByIDForDerivedTest(t, &def, "edge_split_b").Parameters = []workflow.Parameter{
		{Key: "plan", Description: "Plan review criteria."},
	}

	derived := workflow.DeriveWiring(def)

	assertDerivedDiagnosticCodes(t, derived, workflow.CodeProvisionFieldOverlap)
	assertDerivedDiagnosticsBlock(t, derived)
}

func TestDeriveWiringReportsSiblingTransitionParameterDescriptionConflict(t *testing.T) {
	def := parameterWorkflow()
	def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_plan_block", SourceNodeID: "node_plan", TransitionID: "block", DisplayName: "Block"})
	def.Edges = append(def.Edges, workflow.Edge{
		WorkflowID:        def.ID,
		ID:                "edge_plan_block",
		Key:               "block",
		TransitionGroupID: "group_plan_block",
		TargetNodeID:      "node_done",
		ContextMode:       workflow.ContextModeNewSession,
		Parameters:        []workflow.Parameter{{Key: "plan", Description: "Plan review criteria."}},
	})

	derived := workflow.DeriveWiring(def)

	assertDerivedDiagnosticCodes(t, derived, workflow.CodeProvisionFieldOverlap)
	assertDerivedDiagnosticsBlock(t, derived)
}

func TestDeriveWiringAggregatesJoinIncomingParameters(t *testing.T) {
	def := joinParameterWorkflow()

	derived := workflow.DeriveWiring(def)

	if len(derived.Diagnostics) > 0 {
		t.Fatalf("expected no diagnostics, got %+v", derived.Diagnostics)
	}
	assertOutputFields(t, derived.JoinOutputFieldsForNode("node_join"), []workflow.OutputField{
		{Name: "plan", Description: "Implementation plan."},
		{Name: "risk", Description: "Known implementation risk."},
	})
	assertOutputFields(t, derived.RequiredProviderFieldsForJoinEdge("edge_branch_a_join"), []workflow.OutputField{
		{Name: "plan", Description: "Implementation plan."},
	})
	assertOutputFields(t, derived.RequiredProviderFieldsForJoinEdge("edge_branch_b_join"), []workflow.OutputField{
		{Name: "risk", Description: "Known implementation risk."},
	})
	assertOutputFields(t, derived.RequiredProvisionFieldsForTransitionGroup("group_branch_a_join"), []workflow.OutputField{
		{Name: "plan", Description: "Implementation plan."},
	})
	assertOutputFields(t, derived.RequiredProvisionFieldsForTransitionGroup("group_branch_b_join"), []workflow.OutputField{
		{Name: "risk", Description: "Known implementation risk."},
	})
}

func TestTransitionOutputFieldsForTargetNodeUsesJoinAggregate(t *testing.T) {
	def := joinParameterWorkflow()
	derived := workflow.DeriveWiring(def)

	assertOutputFields(t, workflow.TransitionOutputFieldsForTargetNode(def, derived, "node_consume"), []workflow.OutputField{
		{Name: "plan", Description: "Implementation plan."},
		{Name: "risk", Description: "Known implementation risk."},
	})
}

func TestDeriveWiringReportsJoinAggregateCollisionsAcrossProducingTransitions(t *testing.T) {
	def := joinParameterWorkflow()
	edgeByIDForDerivedTest(t, &def, "edge_branch_b_join").Parameters = []workflow.Parameter{
		{Key: "plan", Description: "Implementation plan."},
	}

	derived := workflow.DeriveWiring(def)

	assertDerivedDiagnosticCodes(t, derived, workflow.CodeProvisionFieldOverlap)
	assertDerivedDiagnosticsBlock(t, derived)
}

func TestDeriveWiringSkipsNonAgentSourceParameters(t *testing.T) {
	def := joinParameterWorkflow()
	edgeByIDForDerivedTest(t, &def, "edge_start_split").Parameters = []workflow.Parameter{
		{Key: "task_context", Description: "Task context."},
	}
	edgeByIDForDerivedTest(t, &def, "edge_join_consume").Parameters = []workflow.Parameter{
		{Key: "aggregate", Description: "Join aggregate."},
	}

	derived := workflow.DeriveWiring(def)

	assertOutputFields(t, derived.RequiredProvisionFieldsForTransitionGroup("group_start"), nil)
	assertOutputFields(t, derived.RequiredProvisionFieldsForTransitionGroup("group_join_consume"), nil)
}

func parameterWorkflow() workflow.Definition {
	return workflow.Definition{
		ID:          "workflow_parameters",
		DisplayName: "Parameter Workflow",
		Nodes: []workflow.Node{
			testStartNode("workflow_parameters", "node_start", "backlog", "Backlog"),
			testAgentNode("workflow_parameters", "node_plan", "plan", "Plan", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "Legacy plan prompt."}),
			testAgentNode("workflow_parameters", "node_implement", "implement", "Implement", workflow.NodeFields{SubagentRole: "coder", PromptTemplate: "Legacy implement prompt.", InputFields: []workflow.InputField{{Name: "legacy", Description: "Legacy input."}}}),
			testTerminalNode("workflow_parameters", "node_done", "done", "Done"),
		},
		TransitionGroups: []workflow.TransitionGroup{
			{WorkflowID: "workflow_parameters", ID: "group_start", SourceNodeID: "node_start", TransitionID: "start", DisplayName: "Start"},
			{WorkflowID: "workflow_parameters", ID: "group_plan_implement", SourceNodeID: "node_plan", TransitionID: "implement", DisplayName: "Implement"},
			{WorkflowID: "workflow_parameters", ID: "group_implement_done", SourceNodeID: "node_implement", TransitionID: "done", DisplayName: "Done"},
		},
		Edges: []workflow.Edge{
			{WorkflowID: "workflow_parameters", ID: "edge_start_plan", Key: "start", TransitionGroupID: "group_start", TargetNodeID: "node_plan", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
			{
				WorkflowID:        "workflow_parameters",
				ID:                "edge_plan_implement",
				Key:               "implement",
				TransitionGroupID: "group_plan_implement",
				TargetNodeID:      "node_implement",
				ContextMode:       workflow.ContextModeNewSession,
				PromptTemplate:    "Use {{.Params.plan}} and {{.Params.risk}}.",
				Parameters: []workflow.Parameter{
					{Key: "plan", Description: "Implementation plan."},
					{Key: "risk", Description: "Known implementation risk."},
				},
			},
			{
				WorkflowID:        "workflow_parameters",
				ID:                "edge_implement_done",
				Key:               "done",
				TransitionGroupID: "group_implement_done",
				TargetNodeID:      "node_done",
				ContextMode:       workflow.ContextModeNewSession,
				Parameters:        []workflow.Parameter{{Key: "summary", Description: "Implementation summary."}},
			},
		},
	}
}

func fanoutParameterWorkflow() workflow.Definition {
	def := parameterWorkflow()
	def.Nodes = append(def.Nodes, testAgentNode("workflow_parameters", "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "coder"}))
	def.TransitionGroups[1] = workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_plan_split", SourceNodeID: "node_plan", TransitionID: "split", DisplayName: "Split"}
	def.Edges[1] = workflow.Edge{
		WorkflowID:        def.ID,
		ID:                "edge_split_a",
		Key:               "implement",
		TransitionGroupID: "group_plan_split",
		TargetNodeID:      "node_implement",
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Implement {{.Params.plan}}.",
		Parameters:        []workflow.Parameter{{Key: "plan", Description: "Implementation plan."}},
	}
	def.Edges = append(def.Edges, workflow.Edge{
		WorkflowID:        def.ID,
		ID:                "edge_split_b",
		Key:               "review",
		TransitionGroupID: "group_plan_split",
		TargetNodeID:      "node_review",
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Review {{.Params.risk}}.",
		Parameters:        []workflow.Parameter{{Key: "risk", Description: "Known implementation risk."}},
	})
	return def
}

func joinParameterWorkflow() workflow.Definition {
	return workflow.Definition{
		ID:          "workflow_join_parameters",
		DisplayName: "Join Parameter Workflow",
		Nodes: []workflow.Node{
			testStartNode("workflow_join_parameters", "node_start", "backlog", "Backlog"),
			testAgentNode("workflow_join_parameters", "node_split", "split", "Split", workflow.NodeFields{SubagentRole: "coder"}),
			testAgentNode("workflow_join_parameters", "node_branch_a", "branch_a", "Branch A", workflow.NodeFields{SubagentRole: "coder"}),
			testAgentNode("workflow_join_parameters", "node_branch_b", "branch_b", "Branch B", workflow.NodeFields{SubagentRole: "coder"}),
			testJoinNode("workflow_join_parameters", "node_join", "join", "Join"),
			testAgentNode("workflow_join_parameters", "node_consume", "consume", "Consume", workflow.NodeFields{SubagentRole: "coder"}),
			testTerminalNode("workflow_join_parameters", "node_done", "done", "Done"),
		},
		TransitionGroups: []workflow.TransitionGroup{
			{WorkflowID: "workflow_join_parameters", ID: "group_start", SourceNodeID: "node_start", TransitionID: "start", DisplayName: "Start"},
			{WorkflowID: "workflow_join_parameters", ID: "group_split", SourceNodeID: "node_split", TransitionID: "split", DisplayName: "Split"},
			{WorkflowID: "workflow_join_parameters", ID: "group_branch_a_join", SourceNodeID: "node_branch_a", TransitionID: "join_a", DisplayName: "Join A"},
			{WorkflowID: "workflow_join_parameters", ID: "group_branch_b_join", SourceNodeID: "node_branch_b", TransitionID: "join_b", DisplayName: "Join B"},
			{WorkflowID: "workflow_join_parameters", ID: "group_join_consume", SourceNodeID: "node_join", TransitionID: "consume", DisplayName: "Consume"},
			{WorkflowID: "workflow_join_parameters", ID: "group_consume_done", SourceNodeID: "node_consume", TransitionID: "done", DisplayName: "Done"},
		},
		Edges: []workflow.Edge{
			{WorkflowID: "workflow_join_parameters", ID: "edge_start_split", Key: "start", TransitionGroupID: "group_start", TargetNodeID: "node_split", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Split."},
			{WorkflowID: "workflow_join_parameters", ID: "edge_split_a", Key: "branch_a", TransitionGroupID: "group_split", TargetNodeID: "node_branch_a", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "A."},
			{WorkflowID: "workflow_join_parameters", ID: "edge_split_b", Key: "branch_b", TransitionGroupID: "group_split", TargetNodeID: "node_branch_b", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "B."},
			{WorkflowID: "workflow_join_parameters", ID: "edge_branch_a_join", Key: "join_a", TransitionGroupID: "group_branch_a_join", TargetNodeID: "node_join", ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "plan", Description: "Implementation plan."}}},
			{WorkflowID: "workflow_join_parameters", ID: "edge_branch_b_join", Key: "join_b", TransitionGroupID: "group_branch_b_join", TargetNodeID: "node_join", ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "risk", Description: "Known implementation risk."}}},
			{WorkflowID: "workflow_join_parameters", ID: "edge_join_consume", Key: "consume", TransitionGroupID: "group_join_consume", TargetNodeID: "node_consume", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Use {{.Params.plan}} and {{.Params.risk}}."},
			{WorkflowID: "workflow_join_parameters", ID: "edge_consume_done", Key: "done", TransitionGroupID: "group_consume_done", TargetNodeID: "node_done", ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "summary", Description: "Summary."}}},
		},
	}
}

func edgeByIDForDerivedTest(t *testing.T, def *workflow.Definition, id workflow.EdgeID) *workflow.Edge {
	t.Helper()
	for i := range def.Edges {
		if def.Edges[i].ID == id {
			return &def.Edges[i]
		}
	}
	t.Fatalf("edge %q not found", id)
	return nil
}

func assertInputBindings(t *testing.T, got []workflow.InputBinding, want []workflow.InputBinding) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("input bindings = %+v, want %+v", got, want)
	}
}

func assertOutputFields(t *testing.T, got []workflow.OutputField, want []workflow.OutputField) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output fields = %+v, want %+v", got, want)
	}
}

func assertDerivedDiagnosticCodes(t *testing.T, derived workflow.DerivedWiring, want ...workflow.ValidationErrorCode) {
	t.Helper()
	codes := make(map[workflow.ValidationErrorCode]bool, len(derived.Diagnostics))
	for _, diagnostic := range derived.Diagnostics {
		codes[diagnostic.Code] = true
	}
	for _, code := range want {
		if !codes[code] {
			t.Fatalf("missing diagnostic code %q in %+v", code, derived.Diagnostics)
		}
	}
}

func assertDerivedDiagnosticsBlock(t *testing.T, derived workflow.DerivedWiring) {
	t.Helper()
	for _, diagnostic := range derived.Diagnostics {
		if diagnostic.BlocksContext {
			return
		}
	}
	t.Fatalf("expected at least one blocking diagnostic in %+v", derived.Diagnostics)
}
