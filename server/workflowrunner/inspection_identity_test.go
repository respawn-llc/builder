package workflowrunner

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
)

func TestCurrentSessionReconstructionPreservesBranchScopedPromptIdentity(t *testing.T) {
	implementation := workflow.TransitionBranchKey("implementation")
	review := workflow.TransitionBranchKey("review")
	identities := map[string]string{}
	for name, branchKey := range map[string]*workflow.TransitionBranchKey{
		"implementation": &implementation,
		"review":         &review,
	} {
		reference, err := workflow.NewCurrentNodeReference("task-1", "node-1", branchKey)
		if err != nil {
			t.Fatalf("NewCurrentNodeReference %s: %v", name, err)
		}
		instructions, err := BuildCurrentSessionTaskInstructions(workflowstore.CurrentNodeStartContext{
			Task:        workflowstore.TaskRecord{ID: "task-1"},
			Workflow:    workflowstore.WorkflowRecord{ID: "workflow-1"},
			Node:        workflowstore.NodeRecord{ID: "node-1"},
			CurrentNode: workflow.CurrentNode{Reference: reference},
		})
		if err != nil {
			t.Fatalf("reconstruct %s Current Node instructions: %v", name, err)
		}
		identities[name] = workflowruntime.CurrentNodePromptIdentity(instructions.CurrentNode)
	}
	if identities["implementation"] == identities["review"] {
		t.Fatal("reopened parallel Current Nodes at the same task/node must retain distinct prompt identities")
	}
}
