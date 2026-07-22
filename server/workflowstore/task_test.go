package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"core/server/metadata"
	"core/server/workflow"
	"core/shared/config"
	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func openConcurrentWorkflowStores(t *testing.T, cfg config.App) (*Store, *Store) {
	t.Helper()
	open := func() *Store {
		metadataStore, err := metadata.Open(cfg.PersistenceRoot)
		if err != nil {
			t.Fatalf("metadata.Open concurrent store: %v", err)
		}
		t.Cleanup(func() {
			if err := metadataStore.Close(); err != nil {
				t.Errorf("close concurrent metadata store: %v", err)
			}
		})
		store, err := New(metadataStore)
		if err != nil {
			t.Fatalf("workflowstore.New concurrent store: %v", err)
		}
		return store
	}
	return open(), open()
}

func raceTaskCreateWithMutation(
	ctx context.Context,
	createStore *Store,
	req CreateTaskRequest,
	mutate func() error,
) (TaskRecord, error, error) {
	start := make(chan struct{})
	type createResult struct {
		task TaskRecord
		err  error
	}
	createResults := make(chan createResult, 1)
	mutationResults := make(chan error, 1)
	go func() {
		<-start
		task, err := createStore.CreateTask(ctx, req)
		createResults <- createResult{task: task, err: err}
	}()
	go func() {
		<-start
		mutationResults <- mutate()
	}()
	close(start)
	created := <-createResults
	return created.task, created.err, <-mutationResults
}

func assertConcurrentTaskCreateOutcome(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID string,
	beforeSequence int64,
	task TaskRecord,
	createErr error,
	mutationErr error,
	allowedLinks map[workflow.WorkflowID]string,
) {
	t.Helper()
	if mutationErr != nil && !isRetryableSQLiteConflict(mutationErr) {
		t.Fatalf("concurrent mutation error = %v, want success or retryable SQLite conflict", mutationErr)
	}
	if createErr != nil {
		if !isRetryableSQLiteConflict(createErr) {
			t.Fatalf("CreateTask concurrent error = %v, want retryable SQLite conflict", createErr)
		}
		assertTaskCreationUnchanged(t, ctx, store, projectID, beforeSequence)
		return
	}
	wantLinkID, ok := allowedLinks[task.WorkflowID]
	if !ok || task.LinkID != wantLinkID {
		t.Fatalf("created task selection = %+v, allowed links = %+v", task, allowedLinks)
	}
	link, err := store.GetProjectWorkflowLink(ctx, task.LinkID)
	if err != nil {
		t.Fatalf("GetProjectWorkflowLink created task: %v", err)
	}
	if link.ProjectID != projectID || link.WorkflowID != task.WorkflowID {
		t.Fatalf("created task link = %+v, task = %+v", link, task)
	}
	def, record, err := store.GetDefinition(ctx, task.WorkflowID)
	if err != nil {
		t.Fatalf("GetDefinition created task workflow: %v", err)
	}
	if task.Version <= 0 || task.Version > record.Version {
		t.Fatalf("created task workflow version = %d, current workflow version = %d", task.Version, record.Version)
	}
	start, err := startNode(def)
	if err != nil {
		t.Fatalf("startNode created task workflow: %v", err)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements created task: %v", err)
	}
	if len(placements) != 1 ||
		placements[0].TaskID != task.ID ||
		placements[0].NodeID != workflow.NodeIDOf(start) ||
		placements[0].State != "active" {
		t.Fatalf("created task placements = %+v, want one active placement at %q", placements, workflow.NodeIDOf(start))
	}
	if got := projectNextTaskSequence(t, ctx, store, projectID); got != beforeSequence+1 {
		t.Fatalf("project next task sequence = %d, want %d after committed create", got, beforeSequence+1)
	}
}

func isRetryableSQLiteConflict(err error) bool {
	var sqliteErr *sqlitedriver.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	default:
		return false
	}
}

func projectNextTaskSequence(t *testing.T, ctx context.Context, store *Store, projectID string) int64 {
	t.Helper()
	var sequence int64
	if err := store.db.QueryRowContext(ctx, `SELECT next_task_seq FROM projects WHERE id = ?`, projectID).Scan(&sequence); err != nil {
		t.Fatalf("query project next task sequence: %v", err)
	}
	return sequence
}

func assertTaskCreationUnchanged(t *testing.T, ctx context.Context, store *Store, projectID string, wantSequence int64) {
	t.Helper()
	if got := projectNextTaskSequence(t, ctx, store, projectID); got != wantSequence {
		t.Fatalf("project next task sequence = %d, want unchanged %d", got, wantSequence)
	}
	var taskCount int64
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_records WHERE project_id = ?`, projectID).Scan(&taskCount); err != nil {
		t.Fatalf("count project tasks: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("project task count = %d, want 0", taskCount)
	}
	var placementCount int64
	if err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM task_node_placements placement
JOIN task_records task ON task.id = placement.task_id
WHERE task.project_id = ?`, projectID).Scan(&placementCount); err != nil {
		t.Fatalf("count project task placements: %v", err)
	}
	if placementCount != 0 {
		t.Fatalf("project task placement count = %d, want 0", placementCount)
	}
}

// A newer comment arriving between page reads must not shift the cursor:
// an offset would now return the already-seen comment, a keyset must not.

func TestDeleteTaskHardDeletesAssociatedRecords(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Delete me", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.AddComment(ctx, task.ID, "note", "user", "nek"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	projectLabel, err := store.CreateProjectLabel(ctx, binding.ProjectID, "delete cleanup")
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}
	if _, err := store.UpdateTaskLabels(ctx, TaskLabelUpdateRequest{
		TaskID:      task.ID,
		AddLabelIDs: []string{projectLabel.ID.String()},
	}); err != nil {
		t.Fatalf("UpdateTaskLabels: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER task_delete_requires_explicit_label_cleanup
BEFORE DELETE ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_label_assignments
    WHERE task_id = OLD.id
)
BEGIN
    SELECT RAISE(ABORT, 'task label assignments must be deleted first');
END;
`); err != nil {
		t.Fatalf("create explicit label cleanup guard: %v", err)
	}

	deleted, err := store.DeleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if deleted.ID != task.ID || deleted.ProjectID != binding.ProjectID {
		t.Fatalf("deleted task identity = %+v, want task %q project %q", deleted, task.ID, binding.ProjectID)
	}
	if len(deleted.ResolvedApprovalTransitionProjections) != 0 || len(deleted.ResolvedInterruptedRunProjections) != 0 {
		t.Fatalf("deleted task resolution projections = %+v", deleted.TaskAttentionResolution)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTask after DeleteTask = %v, want sql.ErrNoRows", err)
	}
	assertZeroTaskRows(t, store, "task_node_placements", string(task.ID))
	assertZeroTaskRows(t, store, "task_transitions", string(task.ID))
	assertZeroTaskRows(t, store, "task_comments", string(task.ID))
	assertZeroTaskRows(t, store, "task_label_assignments", string(task.ID))
	if _, err := store.queries.GetTaskRun(ctx, string(started.RunID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTaskRun after DeleteTask = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.DeleteTask(ctx, task.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DeleteTask missing = %v, want sql.ErrNoRows", err)
	}
}

// Fan-out leaves placements carrying parallel_batch_transition_id and
// transition rows carrying source_placement_id/source_run_id. These are the
// ON DELETE SET NULL cross-links whose runtime validation triggers previously
// aborted a cascading task delete; deletion must remove them cleanly.
