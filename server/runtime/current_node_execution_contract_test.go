package runtime

import (
	"testing"

	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

func TestCurrentNodeExecutionConfigurationCarriesOnlyLiveScopeAndNaturalNodeIdentity(t *testing.T) {
	t.Parallel()
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

func TestRuntimeRejectsCurrentNodeExecutionWithoutScope(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSessionAt(t, t.TempDir())
	reference := mustTestCurrentNodeReference(t, "task-zero-scope", "node-zero-scope", nil)

	engine, err := New(
		store,
		mustMaterializeTestEventLog(t, store),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			CurrentNodeExecution: &workflowruntime.CurrentNodeExecutionConfig{
				Instructions: workflowruntime.TaskInstructions{CurrentNode: reference},
			},
		},
	)
	if err == nil {
		_ = engine.Close()
		t.Fatal("runtime accepted a Current Node execution without an exact scope")
	}
}

func TestCurrentNodeExecutionBindingHasOneOwner(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSessionAt(t, t.TempDir())
	workflowID := runtimeids.NewWorkflowID()
	execution := &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID: runtimeids.NewExecutionScopeID(),
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: mustTestCurrentNodeReference(t, "task-binding-owner", "node-binding-owner", nil),
			WorkflowID:  workflowID,
		},
	}
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		CurrentNodeExecution: execution,
	})

	first, err := engine.BindCurrentNodeExecution(execution)
	if err != nil {
		t.Fatalf("claim configured Current Node execution: %v", err)
	}
	if duplicate, err := engine.BindCurrentNodeExecution(execution); err == nil {
		_ = duplicate.Close()
		t.Fatal("runtime granted a second Current Node execution binding")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close Current Node execution binding: %v", err)
	}
	retained, err := engine.WorkflowSessionState()
	if err != nil {
		t.Fatalf("retained Workflow Session state: %v", err)
	}
	if retained == nil || retained.TaskID != execution.Instructions.CurrentNode.TaskID || retained.WorkflowID != workflowID {
		t.Fatalf("retained Workflow Session state = %+v, want completion eligibility for prior assignment", retained)
	}
	successor := &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID: runtimeids.NewExecutionScopeID(),
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: mustTestCurrentNodeReference(t, "task-binding-owner", "node-binding-successor", nil),
			WorkflowID:  workflowID,
		},
	}
	rebound, err := engine.BindCurrentNodeExecution(successor)
	if err != nil {
		t.Fatalf("bind successor Current Node execution after owner close: %v", err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatalf("close rebound Current Node execution: %v", err)
	}
	if !engine.CurrentNodeExecutionConfigured() {
		t.Fatal("successor completion contract was discarded when exact scope ownership ended")
	}
}
