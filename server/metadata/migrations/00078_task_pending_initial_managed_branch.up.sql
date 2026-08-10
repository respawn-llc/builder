-- +goose Up

DROP VIEW task_records;

ALTER TABLE tasks
ADD COLUMN pending_initial_managed_branch_name TEXT
    CHECK (
        pending_initial_managed_branch_name IS NULL
        OR (
            length(trim(pending_initial_managed_branch_name)) > 0
            AND pending_initial_managed_branch_name = trim(pending_initial_managed_branch_name)
            AND execution_target_mode IS NULL
            AND managed_worktree_id IS NULL
        )
    );

UPDATE tasks
SET pending_initial_managed_branch_name = short_id
WHERE execution_target_mode IS NULL
  AND managed_worktree_id IS NULL;

CREATE VIEW task_records AS
SELECT
    task.id,
    link.project_id,
    task.project_workflow_link_id,
    link.workflow_id,
    task.workflow_revision_seen,
    task.task_seq,
    task.short_id,
    task.title,
    task.body,
    task.source_url,
    task.source_workspace_id,
    task.managed_worktree_id,
    task.pending_initial_managed_branch_name,
    task.execution_target_mode,
    task.execution_target_requested_ref,
    task.execution_target_resolved_ref,
    task.execution_target_commit_oid,
    task.execution_target_provenance,
    task.created_at_unix_ms,
    task.updated_at_unix_ms,
    task.metadata_json
FROM tasks task
JOIN project_workflow_links link ON link.id = task.project_workflow_link_id;
