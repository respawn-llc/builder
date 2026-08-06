package workflow_test

import (
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
)

func TestEdgeSelectionContractRoundTripsAndValidatesProtectedParameters(t *testing.T) {
	def := validWorkflow(t)
	edge := edgeByIDForValidationTest(t, &def, "edge_done")
	edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
	edge.ThinkingSelection = workflow.ThinkingSelectionPreviousNode
	edge.Parameters = []workflow.Parameter{
		{
			Key:         "role",
			Description: "",
			Purpose:     workflow.ParameterPurposeTargetAssignee,
		},
		{
			Key:         "thinking",
			Description: "Thinking level.",
			Purpose:     workflow.ParameterPurposeTargetThinking,
		},
		{
			Key:         "summary",
			Description: "Summary.",
			Purpose:     workflow.ParameterPurposeOrdinary,
		},
	}

	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context:      workflow.ValidationContextDraft,
		RoleResolver: testsetup.QuestionsEnabled("coder"),
	})
	if result.HasBlockingErrors() {
		t.Fatalf("ValidateDefinition returned blocking errors: %#v", result.Errors)
	}
}

func TestEdgeSelectionContractRejectsHardShapeFailures(t *testing.T) {
	tests := []struct {
		name string
		edit func(*workflow.Definition)
		code workflow.ValidationErrorCode
	}{
		{
			name: "invalid assignee selection",
			edit: func(def *workflow.Definition) {
				edgeByIDForValidationTest(t, def, "edge_done").AssigneeSelection = workflow.AssigneeSelection("invalid")
			},
			code: workflow.CodeInvalidAssigneeSelection,
		},
		{
			name: "invalid thinking selection",
			edit: func(def *workflow.Definition) {
				edgeByIDForValidationTest(t, def, "edge_done").ThinkingSelection = workflow.ThinkingSelection("invalid")
			},
			code: workflow.CodeInvalidThinkingSelection,
		},
		{
			name: "enabled assignee selection without protected parameter",
			edit: func(def *workflow.Definition) {
				edge := edgeByIDForValidationTest(t, def, "edge_done")
				edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
				edge.Parameters = []workflow.Parameter{{Key: "summary", Description: "Summary.", Purpose: workflow.ParameterPurposeOrdinary}}
			},
			code: workflow.CodeMissingProtectedParameter,
		},
		{
			name: "enabled thinking selection without protected parameter",
			edit: func(def *workflow.Definition) {
				edge := edgeByIDForValidationTest(t, def, "edge_done")
				edge.ThinkingSelection = workflow.ThinkingSelectionPreviousNode
				edge.Parameters = []workflow.Parameter{{Key: "summary", Description: "Summary.", Purpose: workflow.ParameterPurposeOrdinary}}
			},
			code: workflow.CodeMissingProtectedParameter,
		},
		{
			name: "duplicate protected purpose",
			edit: func(def *workflow.Definition) {
				edge := edgeByIDForValidationTest(t, def, "edge_done")
				edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
				edge.Parameters = []workflow.Parameter{
					{Key: "role_a", Purpose: workflow.ParameterPurposeTargetAssignee},
					{Key: "role_b", Purpose: workflow.ParameterPurposeTargetAssignee},
				}
			},
			code: workflow.CodeDuplicateProtectedParameter,
		},
		{
			name: "invalid parameter purpose",
			edit: func(def *workflow.Definition) {
				edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{
					Key: "summary", Description: "Summary.", Purpose: workflow.ParameterPurpose("invalid"),
				}}
			},
			code: workflow.CodeInvalidParameterPurpose,
		},
		{
			name: "ordinary parameter description required",
			edit: func(def *workflow.Definition) {
				edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{
					Key: "summary", Purpose: workflow.ParameterPurposeOrdinary,
				}}
			},
			code: workflow.CodeParameterDescriptionRequired,
		},
		{
			name: "protected parameter may have blank description",
			edit: func(def *workflow.Definition) {
				edge := edgeByIDForValidationTest(t, def, "edge_done")
				edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
				edge.Parameters = []workflow.Parameter{{
					Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee,
				}}
			},
			code: "",
		},
		{
			name: "dormant protected key collision",
			edit: func(def *workflow.Definition) {
				edge := edgeByIDForValidationTest(t, def, "edge_done")
				edge.Parameters = []workflow.Parameter{
					{Key: "role", Purpose: workflow.ParameterPurposeTargetAssignee},
					{Key: "role", Description: "Ordinary collision.", Purpose: workflow.ParameterPurposeOrdinary},
				}
			},
			code: workflow.CodeDuplicateParameter,
		},
		{
			name: "malformed parameter key",
			edit: func(def *workflow.Definition) {
				edgeByIDForValidationTest(t, def, "edge_done").Parameters = []workflow.Parameter{{
					Key: "not a key", Description: "Description.", Purpose: workflow.ParameterPurposeOrdinary,
				}}
			},
			code: workflow.CodeInvalidParameter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := validWorkflow(t)
			tt.edit(&def)
			result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
				Context:      workflow.ValidationContextDraft,
				RoleResolver: testsetup.QuestionsEnabled("coder"),
			})
			if tt.code == "" {
				if result.HasBlockingErrors() {
					t.Fatalf("ValidateDefinition returned blocking errors: %#v", result.Errors)
				}
				return
			}
			assertHasCodes(t, result, tt.code)
		})
	}
}

func TestEdgeSelectionContractRejectsBlankModesAndPurposes(t *testing.T) {
	def := validWorkflow(t)
	edge := edgeByIDForValidationTest(t, &def, "edge_done")
	edge.AssigneeSelection = ""
	edge.ThinkingSelection = workflow.ThinkingSelectionConfigured
	edge.Parameters = []workflow.Parameter{{
		Key:         "summary",
		Description: "Summary.",
		Purpose:     "",
	}}

	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context:      workflow.ValidationContextDraft,
		RoleResolver: testsetup.QuestionsEnabled("coder"),
	})
	assertHasCodes(t, result, workflow.CodeInvalidAssigneeSelection, workflow.CodeInvalidParameterPurpose)
}

func TestEdgeSelectionContractDoesNotHardValidateTopology(t *testing.T) {
	def := validWorkflow(t)
	edge := edgeByIDForValidationTest(t, &def, "edge_done")
	edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
	edge.Parameters = []workflow.Parameter{{
		Key:     "role",
		Purpose: workflow.ParameterPurposeTargetAssignee,
	}}
	edge.TargetNodeID = "node_done"

	result := workflow.ValidateDefinition(def, workflow.ValidationOptions{
		Context:      workflow.ValidationContextDraft,
		RoleResolver: testsetup.QuestionsEnabled("coder"),
	})
	for _, err := range result.Errors {
		if strings.Contains(err.Message, "topology") {
			t.Fatalf("draft shape validation must not reject selector topology: %#v", err)
		}
	}
}

func TestEdgeCanonicalizationKeepsIncomingEdgeSelectionIndependent(t *testing.T) {
	edges := []workflow.Edge{
		{
			AssigneeSelection: workflow.AssigneeSelectionPreviousNode,
			Parameters: []workflow.Parameter{{
				Key:     "role",
				Purpose: workflow.ParameterPurposeTargetAssignee,
			}},
		},
		{
			AssigneeSelection: workflow.AssigneeSelectionConfigured,
		},
	}

	canonical := edges[0].Canonical()
	canonical.Parameters[0].Key = "renamed_role"
	if edges[0].Parameters[0].Key != "role" {
		t.Fatal("canonical edge shares parameter storage with its source")
	}
	if canonical.AssigneeSelection != workflow.AssigneeSelectionPreviousNode {
		t.Fatalf("canonical selected edge changed mode: %q", canonical.AssigneeSelection)
	}
	if edges[1].AssigneeSelection != workflow.AssigneeSelectionConfigured {
		t.Fatalf("incoming edge selection leaked across edges: %q", edges[1].AssigneeSelection)
	}
}
