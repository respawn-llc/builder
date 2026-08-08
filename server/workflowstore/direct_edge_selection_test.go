package workflowstore

import (
	"context"
	"testing"

	"core/server/workflow"
)

func TestDirectEdgeMutationRejectsInapplicableProtectedSelection(t *testing.T) {
	ctx := context.Background()
	_, store, _ := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	request := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, definition)
	edge := workflowGraphSaveEdgeRecord(t, request.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
	edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
	edge.Parameters = []workflow.Parameter{{
		Key:     "role",
		Purpose: workflow.ParameterPurposeTargetAssignee,
	}}
	store.roleResolver = emptyTargetAgentCatalog{}

	if _, err := store.UpdateEdge(ctx, *edge); err == nil {
		t.Fatalf("UpdateEdge error = %v, want server-owned inapplicable selector rejection", err)
	}
}

func TestDirectEdgeMutationAcceptsSelectorEnabledAgentSelfLoop(t *testing.T) {
	ctx := context.Background()
	_, store, _ := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	request := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, definition)
	edge := workflowGraphSaveEdgeRecord(t, request.Edges, workflow.EdgeID("edge-audit-"+workflowID.String()))
	edge.TargetNodeID = workflow.NodeIDOf(nodeByKey(t, definition, "review"))
	edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
	edge.Parameters = []workflow.Parameter{{
		Key:     "role",
		Purpose: workflow.ParameterPurposeTargetAssignee,
	}}
	store.roleResolver = completionTargetCatalog{
		roles: map[string]workflow.TargetAgentRole{
			"coder": {Identity: "coder", QuestionsEnabled: true},
		},
		selectable: []workflow.TargetAgentRole{{
			Identity:              "coder",
			ExplicitAgentCallable: true,
			QuestionsEnabled:      true,
		}},
	}

	if _, err := store.UpdateEdge(ctx, *edge); err != nil {
		t.Fatalf("UpdateEdge self-loop: %v", err)
	}
}
