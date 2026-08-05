package workflowview

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func TestProjectDefinitionPreservesNodeGroupSortOrder(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	definition := workflow.Definition{
		ID: workflowID,
		NodeGroups: []workflow.NodeGroup{{
			WorkflowID:  workflowID,
			ID:          "group-ordered",
			Key:         "ordered",
			DisplayName: "Ordered",
			SortOrder:   37,
		}},
	}

	projected, _ := ProjectDefinition(definition, workflowstore.WorkflowRecord{
		ID:   workflowID,
		Name: "Workflow",
	})

	if len(projected.NodeGroups) != 1 || projected.NodeGroups[0].SortOrder != 37 {
		t.Fatalf("projected node groups = %+v, want persisted sort order 37", projected.NodeGroups)
	}
}

func TestValidationErrorsInheritOnlyAnExplicitOptionalWorkflowID(t *testing.T) {
	inheritedID := runtimeids.NewWorkflowID()
	explicitID := runtimeids.NewWorkflowID()
	projected := ValidationErrors(&inheritedID, []workflow.ValidationError{
		{Code: workflow.CodeMissingNodeID},
		{Code: workflow.CodeMissingEdgeID, WorkflowID: &explicitID},
	})

	if projected[0].WorkflowID == nil || *projected[0].WorkflowID != inheritedID {
		t.Fatalf("inherited workflow id = %v, want %q", projected[0].WorkflowID, inheritedID)
	}
	if projected[1].WorkflowID == nil || *projected[1].WorkflowID != explicitID {
		t.Fatalf("explicit workflow id = %v, want %q", projected[1].WorkflowID, explicitID)
	}
}
