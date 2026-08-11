package workflowfixture

import (
	"context"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

type StoreEdit func(workflow.Definition, *workflowstore.WorkflowGraphSaveRequest)

func SaveStoreGraph(
	t testing.TB,
	ctx context.Context,
	store *workflowstore.Store,
	workflowID runtimeids.WorkflowID,
	edit StoreEdit,
) workflowstore.WorkflowGraphSaveResult {
	t.Helper()
	definition, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition workflow fixture: %v", err)
	}
	request := workflowstore.NewWorkflowGraphSaveRequest(definition, record.Version)
	edit(definition, &request)
	result, err := store.SaveWorkflowGraph(ctx, request)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph workflow fixture: %v", err)
	}
	if !result.Saved {
		t.Fatalf("SaveWorkflowGraph workflow fixture rejected: blockers=%+v validation=%+v", result.Blockers, result.ValidationErrors)
	}
	return result
}
