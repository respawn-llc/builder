-- +goose Up
DROP TRIGGER IF EXISTS sessions_task_owner_clear_associations;
DROP TRIGGER IF EXISTS session_workflow_node_associations_owner_insert;
DROP TRIGGER IF EXISTS session_workflow_node_associations_owner_update;
CREATE TABLE session_workflow_node_associations_rebuilt (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    transition_branch_key TEXT
        CHECK (transition_branch_key IS NULL OR length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    association_status TEXT NOT NULL CHECK (association_status IN ('current', 'historical')),
    source_session_id TEXT REFERENCES sessions(id) ON DELETE RESTRICT,
    associated_at_unix_ms INTEGER NOT NULL CHECK (associated_at_unix_ms > 0),
    CHECK (
        (association_status = 'current' AND source_session_id IS NOT NULL)
        OR association_status = 'historical'
    )
);
INSERT INTO session_workflow_node_associations_rebuilt (
    task_id,
    session_id,
    node_id,
    transition_branch_key,
    association_status,
    source_session_id,
    associated_at_unix_ms
)
SELECT
    session.task_id,
    association.session_id,
    association.node_id,
    association.transition_branch_key,
    'historical',
    NULL,
    association.associated_at_unix_ms
FROM session_workflow_node_associations association
JOIN sessions session ON session.id = association.session_id;
DROP TABLE session_workflow_node_associations;
ALTER TABLE session_workflow_node_associations_rebuilt
RENAME TO session_workflow_node_associations;
CREATE UNIQUE INDEX session_workflow_node_associations_serial_unique_idx
    ON session_workflow_node_associations(session_id, node_id)
    WHERE transition_branch_key IS NULL;
CREATE UNIQUE INDEX session_workflow_node_associations_branch_unique_idx
    ON session_workflow_node_associations(session_id, node_id, transition_branch_key)
    WHERE transition_branch_key IS NOT NULL;
CREATE UNIQUE INDEX session_workflow_node_associations_current_serial_unique_idx
    ON session_workflow_node_associations(task_id, node_id)
    WHERE association_status = 'current' AND transition_branch_key IS NULL;
CREATE UNIQUE INDEX session_workflow_node_associations_current_branch_unique_idx
    ON session_workflow_node_associations(task_id, node_id, transition_branch_key)
    WHERE association_status = 'current' AND transition_branch_key IS NOT NULL;
CREATE INDEX session_workflow_node_associations_history_lookup_idx
    ON session_workflow_node_associations(task_id, node_id, transition_branch_key)
    WHERE association_status = 'historical';
CREATE INDEX session_workflow_node_associations_session_recency_idx
    ON session_workflow_node_associations(session_id, associated_at_unix_ms DESC, node_id DESC);
-- +goose StatementBegin
CREATE TRIGGER sessions_task_owner_clear_associations
AFTER UPDATE OF task_id ON sessions
FOR EACH ROW
WHEN NEW.task_id IS NULL
BEGIN
    DELETE FROM session_workflow_node_associations
    WHERE session_id = NEW.id
       OR source_session_id = NEW.id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER session_workflow_node_associations_owner_insert
BEFORE INSERT ON session_workflow_node_associations
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM sessions retained_session
    JOIN task_records task ON task.id = NEW.task_id
    JOIN workflow_nodes node ON node.id = NEW.node_id
    WHERE retained_session.id = NEW.session_id
      AND retained_session.task_id = NEW.task_id
      AND node.workflow_id = task.workflow_id
)
OR (
    NEW.source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'session node association must belong to one task workflow');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER session_workflow_node_associations_owner_update
BEFORE UPDATE OF task_id, session_id, node_id, source_session_id ON session_workflow_node_associations
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM sessions retained_session
    JOIN task_records task ON task.id = NEW.task_id
    JOIN workflow_nodes node ON node.id = NEW.node_id
    WHERE retained_session.id = NEW.session_id
      AND retained_session.task_id = NEW.task_id
      AND node.workflow_id = task.workflow_id
)
OR (
    NEW.source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'session node association must belong to one task workflow');
END;
-- +goose StatementEnd
ALTER TABLE task_current_nodes
ADD COLUMN continuation_source_kind TEXT
    CHECK (continuation_source_kind IS NULL OR continuation_source_kind IN (
        'exact',
        'deferred_self',
        'absent'
    ));
ALTER TABLE task_current_nodes
ADD COLUMN continuation_source_session_id TEXT REFERENCES sessions(id) ON DELETE RESTRICT;
ALTER TABLE task_current_nodes
ADD COLUMN legacy_materialized INTEGER NOT NULL DEFAULT 1
    CHECK (legacy_materialized IN (0, 1));
UPDATE task_current_nodes
SET
    continuation_source_kind = CASE
        WHEN (
            SELECT node.kind
            FROM workflow_nodes node
            WHERE node.id = task_current_nodes.node_id
        ) = 'agent'
        AND task_current_nodes.session_id IS NULL
        THEN 'deferred_self'
        WHEN (
            SELECT node.kind
            FROM workflow_nodes node
            WHERE node.id = task_current_nodes.node_id
        ) IN ('start', 'terminal')
        THEN 'absent'
        ELSE NULL
    END,
    continuation_source_session_id = NULL,
    legacy_materialized = CASE
        WHEN (
            SELECT node.kind
            FROM workflow_nodes node
            WHERE node.id = task_current_nodes.node_id
        ) = 'agent'
        AND task_current_nodes.session_id IS NOT NULL
        THEN 1
        WHEN (
            SELECT node.kind
            FROM workflow_nodes node
            WHERE node.id = task_current_nodes.node_id
        ) IN ('script', 'join')
        THEN 1
        ELSE 0
    END;
-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_continuation_source_insert
BEFORE INSERT ON task_current_nodes
FOR EACH ROW
WHEN NOT (
    (
        NEW.legacy_materialized = 1
        AND NEW.continuation_source_kind IS NULL
        AND NEW.continuation_source_session_id IS NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND (
            (
                NEW.continuation_source_kind = 'exact'
                AND NEW.continuation_source_session_id IS NOT NULL
            )
            OR (
                NEW.continuation_source_kind IN ('deferred_self', 'absent')
                AND NEW.continuation_source_session_id IS NULL
            )
        )
    )
)
OR (
    NEW.continuation_source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.continuation_source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'current node continuation source is invalid');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_continuation_source_update
BEFORE UPDATE OF task_id, continuation_source_kind, continuation_source_session_id, legacy_materialized ON task_current_nodes
FOR EACH ROW
WHEN NOT (
    (
        NEW.legacy_materialized = 1
        AND NEW.continuation_source_kind IS NULL
        AND NEW.continuation_source_session_id IS NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND (
            (
                NEW.continuation_source_kind = 'exact'
                AND NEW.continuation_source_session_id IS NOT NULL
            )
            OR (
                NEW.continuation_source_kind IN ('deferred_self', 'absent')
                AND NEW.continuation_source_session_id IS NULL
            )
        )
    )
)
OR (
    NEW.continuation_source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.continuation_source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'current node continuation source is invalid');
END;
-- +goose StatementEnd
UPDATE task_pending_approval_branches
SET context_source_resolution_json = (
    SELECT json_object(
        'target_session',
        json(
            CASE
                WHEN node.kind = 'agent'
                 AND json_extract(task_pending_approval_branches.target_snapshot_json, '$.session_id') IS NOT NULL
                THEN json_object(
                    'kind', 'reuse',
                    'session_id', json_extract(task_pending_approval_branches.target_snapshot_json, '$.session_id')
                )
                WHEN node.kind = 'agent'
                THEN json_object('kind', 'create')
                ELSE json_object('kind', 'no_agent')
            END
        ),
        'active_source',
        json(
            CASE
                WHEN node.kind = 'agent'
                 AND json_extract(task_pending_approval_branches.target_snapshot_json, '$.session_id') IS NULL
                THEN json_object('kind', 'deferred_self')
                WHEN node.kind IN ('agent', 'script', 'join')
                THEN json_object('kind', 'legacy')
                ELSE json_object('kind', 'absent')
            END
        )
    )
    FROM workflow_nodes node
    WHERE node.id = json_extract(task_pending_approval_branches.target_snapshot_json, '$.node_id')
);
ALTER TABLE task_active_fanout_branches
ADD COLUMN continuation_source_kind TEXT
    CHECK (continuation_source_kind IS NULL OR continuation_source_kind IN (
        'exact',
        'deferred_self',
        'absent'
    ));
ALTER TABLE task_active_fanout_branches
ADD COLUMN continuation_source_session_id TEXT REFERENCES sessions(id) ON DELETE RESTRICT;
ALTER TABLE task_active_fanout_branches
ADD COLUMN legacy_materialized INTEGER NOT NULL DEFAULT 1
    CHECK (legacy_materialized IN (0, 1));
UPDATE task_active_fanout_branches
SET
    continuation_source_kind = NULL,
    continuation_source_session_id = NULL,
    legacy_materialized = 1;
-- +goose StatementBegin
CREATE TRIGGER task_active_fanout_branches_continuation_source_insert
BEFORE INSERT ON task_active_fanout_branches
FOR EACH ROW
WHEN NOT (
    (
        NEW.legacy_materialized = 1
        AND NEW.continuation_source_kind IS NULL
        AND NEW.continuation_source_session_id IS NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND NEW.continuation_source_kind = 'exact'
        AND NEW.continuation_source_session_id IS NOT NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND NEW.continuation_source_kind IN ('deferred_self', 'absent')
        AND NEW.continuation_source_session_id IS NULL
    )
)
OR (
    NEW.continuation_source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.continuation_source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'Fan-Out branch continuation source is invalid');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER task_active_fanout_branches_continuation_source_update
BEFORE UPDATE OF task_id, continuation_source_kind, continuation_source_session_id, legacy_materialized ON task_active_fanout_branches
FOR EACH ROW
WHEN NOT (
    (
        NEW.legacy_materialized = 1
        AND NEW.continuation_source_kind IS NULL
        AND NEW.continuation_source_session_id IS NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND NEW.continuation_source_kind = 'exact'
        AND NEW.continuation_source_session_id IS NOT NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND NEW.continuation_source_kind IN ('deferred_self', 'absent')
        AND NEW.continuation_source_session_id IS NULL
    )
)
OR (
    NEW.continuation_source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.continuation_source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'Fan-Out branch continuation source is invalid');
END;
-- +goose StatementEnd
