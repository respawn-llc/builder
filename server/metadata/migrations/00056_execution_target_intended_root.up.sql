-- +goose Up

ALTER TABLE task_execution_targets
ADD COLUMN intended_worktree_root TEXT
    CHECK (intended_worktree_root IS NULL OR length(trim(intended_worktree_root)) > 0);

-- +goose StatementBegin
CREATE TRIGGER task_execution_targets_intended_root_insert
BEFORE INSERT ON task_execution_targets
FOR EACH ROW
WHEN (
    (NEW.state IN ('initial_provisioning', 'locked_reprovisioning')
        AND NEW.intended_worktree_root IS NULL)
    OR (NEW.state = 'locked'
        AND NEW.intended_worktree_root IS NOT NULL)
    OR (NEW.policy = 'none'
        AND NEW.intended_worktree_root IS NOT NULL)
)
BEGIN
    SELECT RAISE(ABORT, 'execution target intended root must match provisioning state');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_execution_targets_intended_root_update
BEFORE UPDATE OF policy, state, intended_worktree_root ON task_execution_targets
FOR EACH ROW
WHEN (
    (NEW.state IN ('initial_provisioning', 'locked_reprovisioning')
        AND NEW.intended_worktree_root IS NULL)
    OR (NEW.state = 'locked'
        AND NEW.intended_worktree_root IS NOT NULL)
    OR (NEW.policy = 'none'
        AND NEW.intended_worktree_root IS NOT NULL)
)
BEGIN
    SELECT RAISE(ABORT, 'execution target intended root must match provisioning state');
END;
-- +goose StatementEnd
