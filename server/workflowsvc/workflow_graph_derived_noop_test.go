package workflowsvc

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestServiceWorkflowGraphSaveIgnoresPersistedDerivedEdgeWiring(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	seed, record, err := service.store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition seed: %v", err)
	}
	seed.Edges[0].PromptTemplate = "Plan work with persisted wiring."
	seed.Edges[0].InputBindings = []workflow.InputBinding{{
		Name: "task_title", Source: workflow.BindingSourceTask, Field: "title",
	}}
	seed.Edges[0].OutputRequirements = []workflow.OutputRequirement{{FieldName: "summary"}}
	seeded, err := service.store.SaveWorkflowGraph(
		ctx,
		workflowstore.NewWorkflowGraphSaveRequest(seed, record.Version),
	)
	if err != nil || !seeded.Saved || !seeded.Changed {
		t.Fatalf("seed persisted derived wiring = %+v, err = %v", seeded, err)
	}
	persisted, _, err := service.store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	var derivedWiring bool
	for _, edge := range persisted.Edges {
		derivedWiring = derivedWiring || len(edge.InputBindings) > 0 || len(edge.OutputRequirements) > 0
	}
	if !derivedWiring {
		t.Fatalf("fixture has no persisted derived wiring: %+v", persisted.Edges)
	}
	current := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID: workflowID, ExpectedVersion: current.Workflow.Version,
		Graph: serverapi.WorkflowGraphDraftFromDefinition(current),
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if preview.Changed {
		t.Fatalf("preview = %+v, want unchanged authored graph", preview)
	}
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: workflowID, ExpectedVersion: current.Workflow.Version,
		Graph: serverapi.WorkflowGraphDraftFromDefinition(current),
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved || saved.Changed || saved.CurrentVersion != current.Workflow.Version {
		t.Fatalf("direct no-op save = %+v", saved)
	}
}
