package workflowscript

import (
	"testing"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestDefinitionScriptPathErrorsOmitBlankNodeIdentity(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	node, err := workflow.NewNode(
		workflow.NodeIdentity{
			WorkflowID:  workflowID,
			ID:          "",
			Key:         "script",
			DisplayName: "Script",
		},
		workflow.NodeKindScript,
		workflow.NodeFields{},
	)
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}

	errors := definitionScriptPathErrors(workflow.Definition{
		ID:    workflowID,
		Nodes: []workflow.Node{node},
	}, nil)
	if len(errors) != 1 {
		t.Fatalf("script path error count = %d, want 1", len(errors))
	}
	if errors[0].NodeID != nil {
		t.Fatalf("script path error NodeID = %q, want absent", *errors[0].NodeID)
	}
}
