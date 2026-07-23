-- +goose Up

ALTER TABLE sessions
ADD COLUMN task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL;

CREATE INDEX sessions_task_id_idx
    ON sessions(task_id)
    WHERE task_id IS NOT NULL;

CREATE TABLE session_workflow_node_associations (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE RESTRICT,
    transition_branch_key TEXT
        CHECK (transition_branch_key IS NULL OR length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    associated_at_unix_ms INTEGER NOT NULL CHECK (associated_at_unix_ms > 0)
);

CREATE UNIQUE INDEX session_workflow_node_associations_serial_unique_idx
    ON session_workflow_node_associations(session_id, node_id)
    WHERE transition_branch_key IS NULL;

CREATE UNIQUE INDEX session_workflow_node_associations_branch_unique_idx
    ON session_workflow_node_associations(session_id, node_id, transition_branch_key)
    WHERE transition_branch_key IS NOT NULL;

CREATE INDEX session_workflow_node_associations_lookup_idx
    ON session_workflow_node_associations(node_id, transition_branch_key, associated_at_unix_ms DESC, session_id DESC);

-- +goose StatementBegin
CREATE TRIGGER sessions_task_owner_insert
BEFORE INSERT ON sessions
FOR EACH ROW
WHEN NEW.task_id IS NOT NULL
AND NOT EXISTS (
    SELECT 1
    FROM task_records task
    WHERE task.id = NEW.task_id
      AND task.project_id = NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'session task owner must belong to session project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER sessions_task_owner_update
BEFORE UPDATE OF task_id, project_id ON sessions
FOR EACH ROW
WHEN NEW.task_id IS NOT NULL
AND NOT EXISTS (
    SELECT 1
    FROM task_records task
    WHERE task.id = NEW.task_id
      AND task.project_id = NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'session task owner must belong to session project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER sessions_task_owner_clear_associations
AFTER UPDATE OF task_id ON sessions
FOR EACH ROW
WHEN NEW.task_id IS NULL
BEGIN
    DELETE FROM session_workflow_node_associations
    WHERE session_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER session_workflow_node_associations_owner_insert
BEFORE INSERT ON session_workflow_node_associations
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM sessions session
    JOIN task_records task ON task.id = session.task_id
    JOIN workflow_nodes node ON node.id = NEW.node_id
    WHERE session.id = NEW.session_id
      AND node.workflow_id = task.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'session node association must belong to owning task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER session_workflow_node_associations_owner_update
BEFORE UPDATE OF session_id, node_id ON session_workflow_node_associations
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM sessions session
    JOIN task_records task ON task.id = session.task_id
    JOIN workflow_nodes node ON node.id = NEW.node_id
    WHERE session.id = NEW.session_id
      AND node.workflow_id = task.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'session node association must belong to owning task workflow');
END;
-- +goose StatementEnd
