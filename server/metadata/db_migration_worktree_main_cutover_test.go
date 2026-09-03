package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestWorktreeMainIdentityCutoverNormalizesSameRootSessionAndTask(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 87)
	if err != nil {
		t.Fatalf("open version 87 database: %v", err)
	}
	now := int64(1000)
	workspaceRoot := filepath.Join(root, "workspace")
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-main-cutover', 'Project', ?, ?, '{}')`, now, now)
	execSeed(t, db, "workspace", `
INSERT INTO workspaces (
    id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES ('workspace-main-cutover', 'project-main-cutover', ?, '{}', ?, ?)`,
		workspaceRoot, now, now)
	execSeed(t, db, "same-root worktree", `
INSERT INTO worktrees (
    id, workspace_id, canonical_root_path, managed, created_branch,
    origin_session_id, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES ('worktree-same-root', 'workspace-main-cutover', ?, 1, 1, 'session-main-cutover', '{}', ?, ?)`,
		workspaceRoot, now, now)
	execSeed(t, db, "session", `
INSERT INTO sessions (
    id, project_id, workspace_id, worktree_id, artifact_relpath, cwd_relpath,
    metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'session-main-cutover', 'project-main-cutover', 'workspace-main-cutover',
    'worktree-same-root', 'sessions/session-main-cutover', 'pkg',
    '{"workspace_root":"`+workspaceRoot+`","workspace_container":"Project","worktree_reminder":{"kind":"enter","worktree_id":"worktree-same-root","worktree_path":"`+workspaceRoot+`"}}',
    ?, ?
)`, now, now)
	seedWorkflowGraph(t, db, "project-main-cutover", now)
	execSeed(t, db, "task", workflowSeedTaskSQL,
		"task-main-cutover", "link-1", 1, "CUT-1", now, now)
	execSeed(t, db, "task source and target", `
UPDATE tasks
SET source_workspace_id = 'workspace-main-cutover',
    managed_worktree_id = 'worktree-same-root',
    execution_target_mode = 'head',
    execution_target_requested_ref = 'HEAD',
    execution_target_resolved_ref = 'refs/heads/main',
    execution_target_commit_oid = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    execution_target_provenance = 'resolved'
WHERE id = 'task-main-cutover'`)
	execSeed(t, db, "session task owner", `
UPDATE sessions
SET task_id = 'task-main-cutover'
WHERE id = 'session-main-cutover'`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 87 database: %v", err)
	}

	store, err := OpenAtPath(root, dbPath)
	if err != nil {
		t.Fatalf("upgrade same-root data: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var worktreeCount int
	if err := store.DB().QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM worktrees
WHERE id = 'worktree-same-root'`).Scan(&worktreeCount); err != nil {
		t.Fatalf("count normalized Worktree registration: %v", err)
	}
	if worktreeCount != 0 {
		t.Fatalf("same-root Worktree registrations = %d, want 0", worktreeCount)
	}

	target, err := store.ResolveSessionExecutionTarget(t.Context(), "session-main-cutover")
	if err != nil {
		t.Fatalf("resolve converted Session target: %v", err)
	}
	if target.Worktree != nil ||
		target.WorkspaceID != "workspace-main-cutover" ||
		target.WorkspaceRoot != workspaceRoot ||
		target.CwdRelpath != "pkg" ||
		target.EffectiveWorkdir != filepath.Join(workspaceRoot, "pkg") {
		t.Fatalf("converted Session target = %+v, want source Workspace with preserved relative directory", target)
	}
	var reminderType sql.NullString
	if err := store.DB().QueryRowContext(t.Context(), `
SELECT json_type(metadata_json, '$.worktree_reminder')
FROM sessions
WHERE id = 'session-main-cutover'`).Scan(&reminderType); err != nil {
		t.Fatalf("read converted Session reminder: %v", err)
	}
	if reminderType.Valid {
		t.Fatalf("converted Session reminder type = %q, want absent", reminderType.String)
	}

	var managedID, mode, requestedRef, resolvedRef, commitOID, provenance sql.NullString
	var taskOwner string
	if err := store.DB().QueryRowContext(t.Context(), `
SELECT managed_worktree_id, execution_target_mode, execution_target_requested_ref,
       execution_target_resolved_ref, execution_target_commit_oid,
       execution_target_provenance
FROM tasks
WHERE id = 'task-main-cutover'`).Scan(
		&managedID, &mode, &requestedRef, &resolvedRef, &commitOID, &provenance,
	); err != nil {
		t.Fatalf("read converted Task target: %v", err)
	}
	if managedID.Valid ||
		!mode.Valid || mode.String != "none" ||
		requestedRef.Valid || resolvedRef.Valid || commitOID.Valid ||
		!provenance.Valid || provenance.String != "resolved" {
		t.Fatalf("converted Task target = managed=%+v mode=%+v requested=%+v resolved=%+v commit=%+v provenance=%+v",
			managedID, mode, requestedRef, resolvedRef, commitOID, provenance)
	}
	if err := store.DB().QueryRowContext(t.Context(), `
SELECT task_id
FROM sessions
WHERE id = 'session-main-cutover'`).Scan(&taskOwner); err != nil {
		t.Fatalf("read preserved Session Task association: %v", err)
	}
	if taskOwner != "task-main-cutover" {
		t.Fatalf("Session Task association = %q, want preserved Task", taskOwner)
	}
}
