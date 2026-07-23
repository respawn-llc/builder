package workflowstore

import (
	"database/sql"
	"errors"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/toolspec"
)

func TestStartTaskRejectsUnsafeWorkflowWithoutMutation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	store.roleResolver = testsetup.RoleResolver{
		workflow.DefaultAgentRole: {toolspec.ToolAskQuestion: true},
		"coder":                   {toolspec.ToolAskQuestion: false},
	}

	_, err := store.StartTask(ctx, task.ID)
	var validationErr WorkflowValidationError
	if !errors.As(err, &validationErr) || !validationErr.HasCode(workflow.CodeAgentRoleRequiredToolDisabled) {
		t.Fatalf("StartTask error = %v, want workflow validation error", err)
	}
	definition, _, err := store.GetDefinition(ctx, task.WorkflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, definition, workflow.NodeKindStart)
	backlog, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(start), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after rejected start: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(backlog) ||
		currentNodes[0].Scheduling != nil ||
		currentNodes[0].SessionID != nil {
		t.Fatalf("current nodes after rejected start = %+v, want one unbound backlog node", currentNodes)
	}
	target, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Task.ExecutionTarget != nil || target.Task.ManagedWorktreeID != "" {
		t.Fatalf("rejected start mutated task: task=%+v", target.Task)
	}
}

func TestRepeatedStartAfterRoleToolDriftSkipsInitialExecutionPreflight(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	resolver := store.roleResolver.(testsetup.RoleResolver)
	resolver["coder"][toolspec.ToolAskQuestion] = false

	_, err := store.StartTask(ctx, task.ID)
	var validationErr WorkflowValidationError
	if errors.As(err, &validationErr) && validationErr.HasCode(workflow.CodeAgentRoleRequiredToolDisabled) {
		t.Fatalf("repeated StartTask returned post-start role-tool validation: %+v", validationErr.Diagnostics)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("repeated StartTask error = %v, want no active Start placement", err)
	}
}
