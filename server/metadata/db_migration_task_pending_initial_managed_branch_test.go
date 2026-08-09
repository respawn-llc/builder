package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	sqlite3 "modernc.org/sqlite/lib"
)

func TestTaskPendingInitialManagedBranchMigrationBackfillsOnlyEligibleTasks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 76)
	if err != nil {
		t.Fatalf("open version 76 metadata database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const (
		projectID   = "project-pending-branch-migration"
		workspaceID = "workspace-pending-branch-migration"
		worktreeID  = "worktree-pending-branch-migration"
	)
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Pending branch migration', 1, 1, '{}')`, projectID)
	seedWorkflowGraph(t, db, projectID, 1)
	execSeed(t, db, "workspace", `
INSERT INTO workspaces (
    id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, '/workspace-pending-branch-migration', '{}', 1, 1)`, workspaceID, projectID)
	execSeed(t, db, "managed Worktree", `
INSERT INTO worktrees (
    id, workspace_id, canonical_root_path, managed, created_branch, origin_session_id,
    git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, '/worktree-pending-branch-migration', 1, 1, '', '{}', 1, 1)`, worktreeID, workspaceID)
	execSeed(t, db, "Tasks", `
INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body,
    source_workspace_id, managed_worktree_id, execution_target_mode, execution_target_provenance,
    created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES
    ('task-unlocked', 'link-1', 1, 1, 'PEN-1', 'Unlocked', '', ?, NULL, NULL, NULL, 1, 1, '{}'),
    ('task-managed', 'link-1', 1, 2, 'PEN-2', 'Managed', '', ?, ?, NULL, NULL, 1, 1, '{}'),
    ('task-none', 'link-1', 1, 3, 'PEN-3', 'None', '', ?, NULL, 'none', 'resolved', 1, 1, '{}')`,
		workspaceID,
		workspaceID, worktreeID,
		workspaceID,
	)

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 77); err != nil {
		t.Fatalf("apply pending initial managed branch migration: %v", err)
	}

	assertPendingInitialManagedBranch(t, db, "task-unlocked", sql.NullString{String: "PEN-1", Valid: true})
	assertPendingInitialManagedBranch(t, db, "task-managed", sql.NullString{})
	assertPendingInitialManagedBranch(t, db, "task-none", sql.NullString{})
}

func TestTaskPendingInitialManagedBranchSchemaConstraints(t *testing.T) {
	t.Parallel()

	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	execSeed(t, store.db, "managed Worktree", `
INSERT INTO worktrees (
    id, workspace_id, canonical_root_path, managed, created_branch, origin_session_id,
    git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES ('worktree-pending-branch-constraint', ?, ?, 1, 1, '', '{}', ?, ?)`,
		binding.WorkspaceID, t.TempDir(), now, now)

	execSeed(t, store.db, "eligible Task", `
INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body,
    source_workspace_id, pending_initial_managed_branch_name,
    created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES ('task-pending-branch-valid', 'link-1', 1, 1, 'PEN-1', 'Task', '', ?, 'feature/PEN-1', ?, ?, '{}')`,
		binding.WorkspaceID, now, now)

	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `
UPDATE tasks
SET pending_initial_managed_branch_name = ' '
WHERE id = 'task-pending-branch-valid'`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `
UPDATE tasks
SET pending_initial_managed_branch_name = ' feature/PEN-1 '
WHERE id = 'task-pending-branch-valid'`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `
UPDATE tasks
SET managed_worktree_id = 'worktree-pending-branch-constraint'
WHERE id = 'task-pending-branch-valid'`)
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `
UPDATE tasks
SET execution_target_mode = 'none',
    execution_target_provenance = 'resolved'
WHERE id = 'task-pending-branch-valid'`)
}

func assertPendingInitialManagedBranch(t *testing.T, db *sql.DB, taskID string, want sql.NullString) {
	t.Helper()
	var got sql.NullString
	if err := db.QueryRow(`
SELECT pending_initial_managed_branch_name
FROM tasks
WHERE id = ?`, taskID).Scan(&got); err != nil {
		t.Fatalf("read pending initial managed branch for %s: %v", taskID, err)
	}
	if got != want {
		t.Fatalf("pending initial managed branch for %s = %+v, want %+v", taskID, got, want)
	}
}
