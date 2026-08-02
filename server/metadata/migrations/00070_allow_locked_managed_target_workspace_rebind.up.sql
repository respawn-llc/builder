-- +goose Up

-- A locked managed target retains the workspace captured by its preparation
-- attempt. A differing current source workspace is accepted only by the
-- atomic target-lock update that establishes the locked target facts.
DROP TRIGGER IF EXISTS tasks_managed_worktree_context_insert;
DROP TRIGGER IF EXISTS tasks_managed_worktree_context_update;

-- +goose StatementBegin
CREATE TRIGGER tasks_managed_worktree_context_insert
BEFORE INSERT ON tasks
FOR EACH ROW
WHEN NEW.managed_worktree_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM worktrees wt
    JOIN workspaces source_workspace ON source_workspace.id = NEW.source_workspace_id
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE wt.id = NEW.managed_worktree_id
      AND wt.workspace_id = NEW.source_workspace_id
      AND source_workspace.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'managed worktree must belong to task source workspace');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tasks_managed_worktree_context_update
BEFORE UPDATE OF
    project_workflow_link_id,
    source_workspace_id,
    managed_worktree_id,
    execution_target_mode,
    execution_target_requested_ref,
    execution_target_resolved_ref,
    execution_target_commit_oid,
    execution_target_provenance
ON tasks
FOR EACH ROW
WHEN NEW.managed_worktree_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM worktrees wt
    JOIN workspaces worktree_workspace ON worktree_workspace.id = wt.workspace_id
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE wt.id = NEW.managed_worktree_id
      AND (
          wt.workspace_id = NEW.source_workspace_id
          OR (
              OLD.managed_worktree_id IS NULL
              AND OLD.execution_target_mode IS NULL
              AND OLD.execution_target_requested_ref IS NULL
              AND OLD.execution_target_resolved_ref IS NULL
              AND OLD.execution_target_commit_oid IS NULL
              AND OLD.execution_target_provenance IS NULL
              AND NEW.execution_target_mode IN ('head', 'default_branch', 'custom_ref')
              AND NEW.execution_target_requested_ref IS NOT NULL
              AND NEW.execution_target_commit_oid IS NOT NULL
              AND NEW.execution_target_provenance IN ('resolved', 'legacy_observed')
          )
      )
      AND worktree_workspace.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'managed worktree must belong to task source workspace');
END;
-- +goose StatementEnd
