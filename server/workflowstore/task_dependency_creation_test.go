package workflowstore

import (
	"context"
	"testing"

	"core/server/workflow"
	workflowlabel "core/server/workflow/label"
)

func TestCreateTaskWithDependencyIntentCommitsNewBlockerAndRelationshipTogether(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	existing := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &workflowID,
		Title:      "Existing blocked task",
		Body:       "Body",
	})

	created, err := store.CreateTask(ctx, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &workflowID,
		Title:      "New blocker task",
		Body:       "Body",
		DependencyIntent: &workflow.TaskDependencyCreateIntent{
			RelatedTaskID: existing.ID,
			NewTaskRole:   workflow.TaskDependencyRoleBlocker,
		},
	})
	if err != nil {
		t.Fatalf("CreateTask with dependency intent: %v", err)
	}
	assertTaskDependencyCount(t, store, created.ID, existing.ID, 1)
	if _, err := store.queries.GetTask(ctx, string(created.ID)); err != nil {
		t.Fatalf("created task missing: %v", err)
	}
}

func TestCreateTaskWithDependencyIntentCommitsNewBlockedTask(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	existing := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Existing blocker", Body: "Body"})

	created, err := store.CreateTask(ctx, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &workflowID,
		Title:      "New blocked task",
		Body:       "Body",
		DependencyIntent: &workflow.TaskDependencyCreateIntent{
			RelatedTaskID: existing.ID,
			NewTaskRole:   workflow.TaskDependencyRoleBlocked,
		},
	})
	if err != nil {
		t.Fatalf("CreateTask with blocked dependency intent: %v", err)
	}
	assertTaskDependencyCount(t, store, existing.ID, created.ID, 1)
}

func TestCreateTaskWithDependencyIntentRollsBackProjectAndCardinalityFailures(t *testing.T) {
	t.Run("cross project", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createValidWorkflow(t, ctx, store)
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		otherBinding, err := store.metadata.RegisterWorkspaceBinding(ctx, t.TempDir())
		if err != nil {
			t.Fatalf("RegisterWorkspaceBinding: %v", err)
		}
		otherWorkflowID := createValidWorkflow(t, ctx, store)
		linkWorkflow(t, ctx, store, otherBinding.ProjectID, otherWorkflowID, true)
		related := createTask(t, ctx, store, CreateTaskRequest{ProjectID: otherBinding.ProjectID, WorkflowID: &otherWorkflowID, Title: "Other", Body: "Body"})
		label, err := store.CreateProjectLabel(ctx, binding.ProjectID, "rollback")
		if err != nil {
			t.Fatalf("CreateProjectLabel: %v", err)
		}
		beforeTasks := taskCountForProject(t, store, binding.ProjectID)
		beforeSequence := projectNextTaskSequence(t, ctx, store, binding.ProjectID)
		beforeAssignments := taskLabelAssignmentCount(t, store, binding.ProjectID)
		_, err = store.CreateTask(ctx, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: &workflowID,
			Title:      "Cross-project",
			Body:       "Body",
			LabelIDs:   []workflowlabel.ID{label.ID},
			DependencyIntent: &workflow.TaskDependencyCreateIntent{
				RelatedTaskID: related.ID,
				NewTaskRole:   workflow.TaskDependencyRoleBlocker,
			},
		})
		assertTaskDependencyPolicyError(t, err, workflow.TaskDependencyProjectMismatch)
		assertCreateTaskUnchanged(t, ctx, store, binding.ProjectID, beforeTasks, beforeSequence, beforeAssignments)
	})

	t.Run("existing blocked task incoming limit", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createValidWorkflow(t, ctx, store)
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		related := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Full blocked", Body: "Body"})
		for index := 0; index < workflow.MaxTaskDependencies; index++ {
			source := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Source", Body: "Body"})
			if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: source.ID, BlockedTaskID: related.ID}); err != nil {
				t.Fatalf("seed incoming dependency %d: %v", index, err)
			}
		}
		label, err := store.CreateProjectLabel(ctx, binding.ProjectID, "rollback-incoming")
		if err != nil {
			t.Fatalf("CreateProjectLabel: %v", err)
		}
		beforeTasks := taskCountForProject(t, store, binding.ProjectID)
		beforeSequence := projectNextTaskSequence(t, ctx, store, binding.ProjectID)
		beforeAssignments := taskLabelAssignmentCount(t, store, binding.ProjectID)
		_, err = store.CreateTask(ctx, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: &workflowID,
			Title:      "New blocker rejected",
			Body:       "Body",
			LabelIDs:   []workflowlabel.ID{label.ID},
			DependencyIntent: &workflow.TaskDependencyCreateIntent{
				RelatedTaskID: related.ID,
				NewTaskRole:   workflow.TaskDependencyRoleBlocker,
			},
		})
		assertTaskDependencyPolicyError(t, err, workflow.TaskDependencyBlockedLimit)
		assertCreateTaskUnchanged(t, ctx, store, binding.ProjectID, beforeTasks, beforeSequence, beforeAssignments)
	})

	t.Run("existing blocker outgoing limit", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createValidWorkflow(t, ctx, store)
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		related := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Full blocker", Body: "Body"})
		for index := 0; index < workflow.MaxTaskDependencies; index++ {
			target := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Target", Body: "Body"})
			if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: related.ID, BlockedTaskID: target.ID}); err != nil {
				t.Fatalf("seed outgoing dependency %d: %v", index, err)
			}
		}
		label, err := store.CreateProjectLabel(ctx, binding.ProjectID, "rollback-outgoing")
		if err != nil {
			t.Fatalf("CreateProjectLabel: %v", err)
		}
		beforeTasks := taskCountForProject(t, store, binding.ProjectID)
		beforeSequence := projectNextTaskSequence(t, ctx, store, binding.ProjectID)
		beforeAssignments := taskLabelAssignmentCount(t, store, binding.ProjectID)
		_, err = store.CreateTask(ctx, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: &workflowID,
			Title:      "New blocked rejected",
			Body:       "Body",
			LabelIDs:   []workflowlabel.ID{label.ID},
			DependencyIntent: &workflow.TaskDependencyCreateIntent{
				RelatedTaskID: related.ID,
				NewTaskRole:   workflow.TaskDependencyRoleBlocked,
			},
		})
		assertTaskDependencyPolicyError(t, err, workflow.TaskDependencyBlockerLimit)
		assertCreateTaskUnchanged(t, ctx, store, binding.ProjectID, beforeTasks, beforeSequence, beforeAssignments)
	})
}

func taskCountForProject(t *testing.T, store *Store, projectID string) int64 {
	t.Helper()
	var count int64
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_records WHERE project_id = ?`, projectID).Scan(&count); err != nil {
		t.Fatalf("count project tasks: %v", err)
	}
	return count
}

func taskLabelAssignmentCount(t *testing.T, store *Store, projectID string) int64 {
	t.Helper()
	var count int64
	if err := store.db.QueryRow(`
		SELECT COUNT(*)
		FROM task_label_assignments assignments
		JOIN task_records ON task_records.id = assignments.task_id
		WHERE task_records.project_id = ?
	`, projectID).Scan(&count); err != nil {
		t.Fatalf("count task label assignments: %v", err)
	}
	return count
}

func assertCreateTaskUnchanged(t *testing.T, ctx context.Context, store *Store, projectID string, tasks, nextSequence, assignments int64) {
	t.Helper()
	if got := taskCountForProject(t, store, projectID); got != tasks {
		t.Fatalf("project task count = %d, want %d", got, tasks)
	}
	if got := projectNextTaskSequence(t, ctx, store, projectID); got != nextSequence {
		t.Fatalf("project next task sequence = %d, want %d", got, nextSequence)
	}
	if got := taskLabelAssignmentCount(t, store, projectID); got != assignments {
		t.Fatalf("project task label assignments = %d, want %d", got, assignments)
	}
}
