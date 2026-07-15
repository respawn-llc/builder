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
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil || len(placements) != 1 || placements[0].State != "active" {
		t.Fatalf("placements after rejected start = %+v, want active Start only", placements)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 0 || len(runs) != 0 || target.Task.ExecutionTarget != nil || target.Task.ManagedWorktreeID != "" {
		t.Fatalf("rejected start mutated task: transitions=%+v runs=%+v task=%+v", transitions, runs, target.Task)
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
