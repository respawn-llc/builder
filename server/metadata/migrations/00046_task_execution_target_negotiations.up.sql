-- +goose Up

CREATE TABLE task_execution_target_negotiations (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    generation TEXT NOT NULL CHECK (length(trim(generation)) > 0),
    workflow_id TEXT NOT NULL CHECK (length(trim(workflow_id)) > 0),
    source_workspace_id TEXT NOT NULL CHECK (length(trim(source_workspace_id)) > 0),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('non_git', 'named_ref', 'detached_commit', 'unavailable')),
    source_named_ref TEXT
        CHECK (source_named_ref IS NULL OR length(trim(source_named_ref)) > 0),
    source_commit TEXT
        CHECK (source_commit IS NULL OR length(trim(source_commit)) > 0),
    recovery_cause TEXT
        CHECK (recovery_cause IS NULL OR length(trim(recovery_cause)) > 0),
    action_kind TEXT NOT NULL CHECK (action_kind IN ('start', 'manual_move', 'approval')),
    start_placement_id TEXT
        CHECK (start_placement_id IS NULL OR length(trim(start_placement_id)) > 0),
    move_source_placement_id TEXT
        CHECK (move_source_placement_id IS NULL OR length(trim(move_source_placement_id)) > 0),
    move_target_node_id TEXT
        CHECK (move_target_node_id IS NULL OR length(trim(move_target_node_id)) > 0),
    approval_transition_id TEXT
        CHECK (approval_transition_id IS NULL OR length(trim(approval_transition_id)) > 0),
    CHECK (
        (source_kind IN ('non_git', 'unavailable') AND source_named_ref IS NULL AND source_commit IS NULL)
        OR (source_kind = 'named_ref' AND source_named_ref IS NOT NULL AND source_commit IS NOT NULL)
        OR (source_kind = 'detached_commit' AND source_named_ref IS NULL AND source_commit IS NOT NULL)
    ),
    CHECK (
        (action_kind = 'start'
            AND start_placement_id IS NOT NULL
            AND move_source_placement_id IS NULL
            AND move_target_node_id IS NULL
            AND approval_transition_id IS NULL)
        OR (action_kind = 'manual_move'
            AND start_placement_id IS NULL
            AND move_source_placement_id IS NOT NULL
            AND move_target_node_id IS NOT NULL
            AND approval_transition_id IS NULL)
        OR (action_kind = 'approval'
            AND start_placement_id IS NULL
            AND move_source_placement_id IS NULL
            AND move_target_node_id IS NULL
            AND approval_transition_id IS NOT NULL)
    )
);

-- +goose StatementBegin
CREATE TRIGGER task_execution_target_negotiations_task_identity_insert
BEFORE INSERT ON task_execution_target_negotiations
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM tasks t
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    WHERE t.id = NEW.task_id
      AND pwl.workflow_id = NEW.workflow_id
      AND t.source_workspace_id = NEW.source_workspace_id
)
OR EXISTS (
    SELECT 1
    FROM task_execution_targets
    WHERE task_id = NEW.task_id
)
BEGIN
    SELECT RAISE(ABORT, 'execution target negotiation must match an unlocked task identity');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_execution_target_negotiations_task_identity_update
BEFORE UPDATE OF task_id, workflow_id, source_workspace_id ON task_execution_target_negotiations
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM tasks t
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    WHERE t.id = NEW.task_id
      AND pwl.workflow_id = NEW.workflow_id
      AND t.source_workspace_id = NEW.source_workspace_id
)
OR EXISTS (
    SELECT 1
    FROM task_execution_targets
    WHERE task_id = NEW.task_id
)
BEGIN
    SELECT RAISE(ABORT, 'execution target negotiation must match an unlocked task identity');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_execution_targets_no_active_negotiation_insert
BEFORE INSERT ON task_execution_targets
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_execution_target_negotiations
    WHERE task_id = NEW.task_id
)
BEGIN
    SELECT RAISE(ABORT, 'execution target negotiation must clear before target lock');
END;
-- +goose StatementEnd
