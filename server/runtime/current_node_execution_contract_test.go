package runtime

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

func TestCurrentNodeExecutionConfigurationCarriesOnlyLiveScopeAndNaturalNodeIdentity(t *testing.T) {
	branch := workflow.TransitionBranchKey("implementation")
	reference, err := workflow.NewCurrentNodeReference("task-1", "node-1", &branch)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	scopeID := runtimeids.NewExecutionScopeID()
	config := Config{CurrentNodeExecution: &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID: scopeID,
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: reference,
		},
	}}
	if config.CurrentNodeExecution == nil || config.CurrentNodeExecution.ScopeID != scopeID {
		t.Fatalf("live Current Node execution scope = %+v, want %s", config.CurrentNodeExecution, scopeID)
	}
	if !config.CurrentNodeExecution.Instructions.CurrentNode.Equal(reference) {
		t.Fatalf("Current Node execution identity = %+v, want %+v", config.CurrentNodeExecution.Instructions.CurrentNode, reference)
	}
}
