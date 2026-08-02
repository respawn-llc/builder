-- +goose Up

-- A locked managed target retains the workspace captured by its preparation
-- attempt. The Task's current source workspace remains independently editable
-- and may point at a later workspace in the same Project before the lock.
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
    JOIN workspaces worktree_workspace ON worktree_workspace.id = wt.workspace_id
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE wt.id = NEW.managed_worktree_id
      AND worktree_workspace.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'managed worktree must belong to task project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tasks_managed_worktree_context_update
BEFORE UPDATE OF project_workflow_link_id, managed_worktree_id ON tasks
FOR EACH ROW
WHEN NEW.managed_worktree_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM worktrees wt
    JOIN workspaces worktree_workspace ON worktree_workspace.id = wt.workspace_id
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE wt.id = NEW.managed_worktree_id
      AND worktree_workspace.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'managed worktree must belong to task project');
END;
-- +goose StatementEnd
