package workflowstore

import (
	"context"
	"errors"
	"testing"

	"core/shared/serverapi"
)

func TestDeleteProjectAuthoritativeCurrentNodeBlockerWinsPreparationInvalidation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	expectedSessionIDs, err := store.metadata.ListProjectSessionIDs(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectSessionIDs: %v", err)
	}
	createTestSession(t, ctx, store, binding, cfg)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)

	blockers, err := store.DeleteProject(ctx, ProjectDeleteRequest{
		ProjectID:          binding.ProjectID,
		ExpectedSessionIDs: expectedSessionIDs,
	})

	if err != nil {
		t.Fatalf("DeleteProject error = %v, want Current Node blocker", err)
	}
	if errors.Is(err, ErrProjectDeletePreparationInvalidated) {
		t.Fatalf("DeleteProject error = %v, authoritative blocker must win preparation invalidation", err)
	}
	if len(blockers) != 1 || blockers[0].Code != "non_terminal_tasks" {
		t.Fatalf("DeleteProject blockers = %+v, want Current Node non-terminal task blocker", blockers)
	}
	assertProjectExists(t, ctx, store, binding.ProjectID)
}

func TestDeleteProjectRejectsPreparedSessionSetChangeBeforeCommit(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	expectedSessionIDs, err := store.metadata.ListProjectSessionIDs(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectSessionIDs: %v", err)
	}
	createTestSession(t, ctx, store, binding, cfg)

	_, err = store.DeleteProject(ctx, ProjectDeleteRequest{
		ProjectID:          binding.ProjectID,
		ExpectedSessionIDs: expectedSessionIDs,
	})

	if !errors.Is(err, ErrProjectDeletePreparationInvalidated) {
		t.Fatalf("DeleteProject error = %v, want %v", err, ErrProjectDeletePreparationInvalidated)
	}
	assertProjectExists(t, ctx, store, binding.ProjectID)
}

func TestDeleteProjectDatabaseFailureRollsBackMetadata(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_project_delete
BEFORE DELETE ON projects
BEGIN
    SELECT RAISE(FAIL, 'project delete rejected');
END`); err != nil {
		t.Fatalf("create project delete rejection trigger: %v", err)
	}
	_, err := store.DeleteProject(ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
	})

	if err == nil {
		t.Fatal("DeleteProject succeeded despite database rejection")
	}
	assertProjectExists(t, ctx, store, binding.ProjectID)
}

func TestDeleteProjectMissingReturnsNotFound(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	if _, err := store.DeleteProject(ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
	}); err != nil {
		t.Fatalf("initial DeleteProject: %v", err)
	}

	_, err := store.DeleteProject(ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
	})

	if !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("second DeleteProject error = %v, want project not found", err)
	}
}

func TestDeleteProjectCommitsMetadata(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)

	blockers, err := store.DeleteProject(ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
	})

	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if len(blockers) != 0 {
		t.Fatalf("DeleteProject blockers = %+v, want none", blockers)
	}
	assertProjectAbsent(t, ctx, store, binding.ProjectID)
}

func assertProjectExists(t *testing.T, ctx context.Context, store *Store, projectID string) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&count); err != nil {
		t.Fatalf("count project: %v", err)
	}
	if count != 1 {
		t.Fatalf("project count = %d, want 1", count)
	}
}

func assertProjectAbsent(t *testing.T, ctx context.Context, store *Store, projectID string) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&count); err != nil {
		t.Fatalf("count project: %v", err)
	}
	if count != 0 {
		t.Fatalf("project count = %d, want 0", count)
	}
}
