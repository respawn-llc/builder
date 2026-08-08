package workflowruntime

import (
	"testing"

	"core/server/workflow"
)

func TestCurrentNodePromptIdentityDistinguishesParallelBranches(t *testing.T) {
	implementation := workflow.TransitionBranchKey("implementation")
	review := workflow.TransitionBranchKey("review")
	implementationRef, err := workflow.NewCurrentNodeReference("task-1", "node-1", &implementation)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference implementation: %v", err)
	}
	reviewRef, err := workflow.NewCurrentNodeReference("task-1", "node-1", &review)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference review: %v", err)
	}
	if CurrentNodePromptIdentity(implementationRef) == CurrentNodePromptIdentity(reviewRef) {
		t.Fatal("parallel Current Nodes at the same task/node must have distinct prompt identities")
	}
}
