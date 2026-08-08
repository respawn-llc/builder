-- +goose Up

DROP TRIGGER IF EXISTS sessions_task_owner_clear_associations;
DROP TRIGGER IF EXISTS session_workflow_node_associations_owner_insert;
DROP TRIGGER IF EXISTS session_workflow_node_associations_owner_update;

CREATE TABLE session_workflow_node_associations_rebuilt (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    transition_branch_key TEXT
        CHECK (transition_branch_key IS NULL OR length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    associated_at_unix_ms INTEGER NOT NULL CHECK (associated_at_unix_ms > 0)
);

INSERT INTO session_workflow_node_associations_rebuilt (
    session_id,
    node_id,
    transition_branch_key,
    associated_at_unix_ms
)
SELECT
    session_id,
    node_id,
    transition_branch_key,
    associated_at_unix_ms
FROM session_workflow_node_associations;

DROP TABLE session_workflow_node_associations;

ALTER TABLE session_workflow_node_associations_rebuilt
RENAME TO session_workflow_node_associations;

CREATE UNIQUE INDEX session_workflow_node_associations_serial_unique_idx
    ON session_workflow_node_associations(session_id, node_id)
    WHERE transition_branch_key IS NULL;

CREATE UNIQUE INDEX session_workflow_node_associations_branch_unique_idx
    ON session_workflow_node_associations(session_id, node_id, transition_branch_key)
    WHERE transition_branch_key IS NOT NULL;

CREATE INDEX session_workflow_node_associations_lookup_idx
    ON session_workflow_node_associations(node_id, transition_branch_key, associated_at_unix_ms DESC, session_id DESC);

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
