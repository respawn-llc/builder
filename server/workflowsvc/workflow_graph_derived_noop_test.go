package workflowsvc

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestServiceWorkflowGraphSaveIgnoresPersistedDerivedEdgeWiring(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	definition, _, err := service.store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	edge := definition.Edges[0]
	if _, err := service.store.UpdateEdge(ctx, workflowstore.EdgeRecord{
		ID: edge.ID, WorkflowID: edge.WorkflowID, TransitionGroupID: edge.TransitionGroupID,
		Key: edge.Key, TargetNodeID: edge.TargetNodeID,
		AssigneeSelection: edge.AssigneeSelection, ThinkingSelection: edge.ThinkingSelection,
		RequiresApproval: edge.RequiresApproval, ContextMode: edge.ContextMode,
		ContextSource: edge.ContextSource, PromptTemplate: edge.PromptTemplate,
		Parameters: edge.Parameters,
		InputBindings: []workflow.InputBinding{{
			Name: "task_title", Source: workflow.BindingSourceTask, Field: "title",
		}},
		OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}},
	}); err != nil {
		t.Fatalf("seed derived wiring: %v", err)
	}
	current := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID: workflowID, ExpectedVersion: current.Workflow.Version,
		Graph: workflowGraphDraftFromDefinition(current),
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if preview.Changed {
		t.Fatalf("preview = %+v, want unchanged authored graph", preview)
	}
}
