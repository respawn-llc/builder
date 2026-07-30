package workflowrunner

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
)

func TestBuildCurrentSessionTaskInstructionsSeparatesNodeOutputsFromTransitionParameters(t *testing.T) {
	reference, err := workflow.NewCurrentNodeReference("task-origin-collision", "node-consumer", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}

	instructions, err := BuildCurrentSessionTaskInstructions(workflowstore.CurrentNodeStartContext{
		Task: workflowstore.TaskRecord{
			ID:         "task-origin-collision",
			WorkflowID: "workflow-origin-collision",
			ShortID:    "ORG-1",
			Title:      "Keep prompt value origins separate",
		},
		Workflow: workflowstore.WorkflowRecord{
			ID:   "workflow-origin-collision",
			Name: "Origin collision",
		},
		Node: workflowstore.NodeRecord{
			ID:          "node-consumer",
			WorkflowID:  "workflow-origin-collision",
			Key:         "consumer",
			DisplayName: "Consumer",
		},
		CurrentNode: workflow.CurrentNode{
			Reference: reference,
			PriorValues: workflow.MaterializedPriorValues{
				NodeOutputs: map[workflow.ModelKey]map[string]string{
					"shared": {"value": "node output"},
				},
				TransitionParameters: map[workflow.ModelKey]map[string]string{
					"shared": {"value": "transition parameter"},
				},
			},
		},
		PromptTemplate: "{{.Nodes.shared.value}} / {{.Params.shared.value}}",
	})
	if err != nil {
		t.Fatalf("BuildCurrentSessionTaskInstructions: %v", err)
	}
	if instructions.NodePrompt != "node output / transition parameter" {
		t.Fatalf("NodePrompt = %q, want independent Node and Transition values", instructions.NodePrompt)
	}
}
