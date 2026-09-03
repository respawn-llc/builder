-- +goose Up

UPDATE sessions
SET
    worktree_id = NULL,
    metadata_json = json_remove(metadata_json, '$.worktree_reminder')
WHERE EXISTS (
    SELECT 1
    FROM worktrees wt
    JOIN workspaces ws ON ws.id = wt.workspace_id
    WHERE wt.canonical_root_path = ws.canonical_root_path
      AND (
          sessions.worktree_id = wt.id
          OR json_extract(sessions.metadata_json, '$.worktree_reminder.worktree_path') =
             wt.canonical_root_path
      )
);

UPDATE tasks
SET
    managed_worktree_id = NULL,
    execution_target_mode = 'none',
    execution_target_requested_ref = NULL,
    execution_target_resolved_ref = NULL,
    execution_target_commit_oid = NULL,
    execution_target_provenance = 'resolved'
WHERE managed_worktree_id IN (
    SELECT wt.id
    FROM worktrees wt
    JOIN workspaces ws ON ws.id = wt.workspace_id
    WHERE wt.canonical_root_path = ws.canonical_root_path
);

DELETE FROM worktrees
WHERE id IN (
    SELECT wt.id
    FROM worktrees wt
    JOIN workspaces ws ON ws.id = wt.workspace_id
    WHERE wt.canonical_root_path = ws.canonical_root_path
);
