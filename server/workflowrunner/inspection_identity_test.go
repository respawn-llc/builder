package workflowrunner

import (
	"testing"

	"core/internal/testharness/testsetup"

	"core/server/sessionruntime"
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
			Workflow:    workflowstore.WorkflowRecord{ID: testsetup.WorkflowID(t, "workflowrunner-inspection")},
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

func TestCurrentNodeRuntimeConfigWiresAuthorityScopeAndNaturalNodeIdentity(t *testing.T) {
	branch := workflow.TransitionBranchKey("implementation")
	reference, err := workflow.NewCurrentNodeReference("task-1", "node-1", &branch)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{ProjectID: "project-1", WorkflowID: testsetup.WorkflowID(t, "workflowrunner-inspection"), CurrentNode: reference})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}

	awareness := &taskAwarenessSource{
		comments:     &taskCommentCountProbe{},
		dependencies: &taskDependencyCountProbe{},
	}
	runtimeConfig, err := BuildCurrentNodeRuntimeConfig(
		workflowstore.CurrentNodeStartContext{
			Task:        workflowstore.TaskRecord{ID: "task-1"},
			Workflow:    workflowstore.WorkflowRecord{ID: testsetup.WorkflowID(t, "workflowrunner-inspection")},
			Node:        workflowstore.NodeRecord{ID: "node-1"},
			CurrentNode: workflow.CurrentNode{Reference: reference},
			TransitionOptions: []workflowstore.TransitionOption{{
				ID:         "done",
				Parameters: []workflow.Parameter{{Key: "summary"}},
			}},
		},
		lease,
		workflowruntime.TaskPromptDeliveryAssignment,
		workflowruntime.CompletionModeTool,
		3,
		true,
		nil,
		awareness,
	)
	if err != nil {
		t.Fatalf("BuildCurrentNodeRuntimeConfig: %v", err)
	}
	if runtimeConfig.ScopeID != lease.ScopeID() {
		t.Fatalf("Current Node execution scope = %s, want authority scope %s", runtimeConfig.ScopeID, lease.ScopeID())
	}
	if runtimeConfig.TaskPromptDelivery != workflowruntime.TaskPromptDeliveryAssignment {
		t.Fatalf("Current Node task prompt delivery = %v, want Assignment", runtimeConfig.TaskPromptDelivery)
	}
	if !runtimeConfig.Instructions.CurrentNode.Equal(reference) {
		t.Fatalf("Current Node execution identity = %+v, want %+v", runtimeConfig.Instructions.CurrentNode, reference)
	}
	if len(runtimeConfig.Contract.Transitions) != 1 || runtimeConfig.Contract.Transitions[0].ID != "done" {
		t.Fatalf("Current Node completion contract = %+v", runtimeConfig.Contract)
	}
	if runtimeConfig.TaskAwarenessSource != awareness {
		t.Fatal("Current Node execution omitted the composed Task awareness source")
	}
}
