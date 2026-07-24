-- +goose Up

DROP VIEW IF EXISTS workflow_attention_candidates;
DROP VIEW IF EXISTS workflow_task_current_run_records;
DROP VIEW IF EXISTS workflow_task_status_run_records;
DROP VIEW IF EXISTS workflow_task_status_transition_records;
DROP VIEW IF EXISTS workflow_task_status_task_records;
DROP VIEW IF EXISTS task_transition_edge_records;
DROP VIEW IF EXISTS task_transition_records;
DROP VIEW IF EXISTS task_run_records;
DROP VIEW IF EXISTS task_node_placement_records;
DROP VIEW IF EXISTS workflow_task_status_records;
DROP VIEW IF EXISTS task_records;

DROP TRIGGER IF EXISTS task_node_placements_runtime_insert;
DROP TRIGGER IF EXISTS task_node_placements_runtime_update;
DROP TRIGGER IF EXISTS task_node_placements_terminal_state_insert;
DROP TRIGGER IF EXISTS task_node_placements_terminal_state_update;
DROP TRIGGER IF EXISTS task_transition_edges_runtime_insert;
DROP TRIGGER IF EXISTS task_transition_edges_runtime_update;
DROP TRIGGER IF EXISTS task_transitions_runtime_insert;
DROP TRIGGER IF EXISTS task_transitions_runtime_update;
DROP TRIGGER IF EXISTS workflow_nodes_current_task_anchor_delete;
DROP TRIGGER IF EXISTS workflow_nodes_task_reference_kind_update;
DROP TRIGGER IF EXISTS task_current_nodes_task_workflow_insert;
DROP TRIGGER IF EXISTS task_current_nodes_task_workflow_update;
DROP TRIGGER IF EXISTS sessions_task_owner_insert;
DROP TRIGGER IF EXISTS sessions_task_owner_update;
DROP TRIGGER IF EXISTS session_workflow_node_associations_owner_insert;
DROP TRIGGER IF EXISTS session_workflow_node_associations_owner_update;

DROP TABLE task_transition_edges;
DROP TABLE task_transitions;
DROP TABLE task_runs;
DROP TABLE task_node_placements;

ALTER TABLE tasks DROP COLUMN canceled_at_unix_ms;
ALTER TABLE tasks DROP COLUMN cancellation_reason;

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

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_task_workflow_insert
BEFORE INSERT ON task_current_nodes
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_records task
    JOIN workflow_nodes node ON node.id = NEW.node_id
    WHERE task.id = NEW.task_id
      AND node.workflow_id = task.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'current node must belong to task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_task_workflow_update
BEFORE UPDATE OF task_id, node_id ON task_current_nodes
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_records task
    JOIN workflow_nodes node ON node.id = NEW.node_id
    WHERE task.id = NEW.task_id
      AND node.workflow_id = task.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'current node must belong to task workflow');
END;
-- +goose StatementEnd

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

CREATE VIEW workflow_task_status_records AS
WITH current_positions AS (
    SELECT
        current_node.task_id,
        current_node.node_id,
        current_node.scheduling_state,
        current_node.interruption_reason
    FROM task_current_nodes current_node
), status_inputs AS (
    SELECT
        task.id AS task_id,
        EXISTS (
            SELECT 1
            FROM current_positions position
            JOIN workflow_nodes node ON node.id = position.node_id
            WHERE position.task_id = task.id
              AND node.kind = 'terminal'
        ) AS has_done,
        EXISTS (
            SELECT 1
            FROM task_pending_approvals approval
            WHERE approval.source_task_id = task.id
        ) AS has_waiting_approval,
        EXISTS (
            SELECT 1
            FROM current_positions position
            WHERE position.task_id = task.id
              AND position.scheduling_state = 'interrupted'
        ) AS has_interrupted,
        EXISTS (
            SELECT 1
            FROM current_positions position
            WHERE position.task_id = task.id
              AND position.scheduling_state = 'interrupted'
              AND position.interruption_reason NOT IN ('user_interrupt', 'workflow_runtime_canceled')
        ) AS has_interrupted_attention,
        EXISTS (
            SELECT 1
            FROM current_positions position
            JOIN workflow_nodes node ON node.id = position.node_id
            WHERE position.task_id = task.id
              AND node.kind = 'start'
        ) AS has_backlog
    FROM task_records task
)
SELECT
    task.id AS task_id,
    CAST(input.has_done AS INTEGER) AS is_done,
    CASE
        WHEN input.has_done THEN 'done'
        WHEN input.has_waiting_approval THEN 'waiting_approval'
        WHEN input.has_interrupted THEN 'interrupted'
        WHEN input.has_backlog THEN 'backlog'
        ELSE 'active'
    END AS kind,
    CASE
        WHEN input.has_done THEN 1
        WHEN input.has_waiting_approval THEN 3
        WHEN input.has_interrupted THEN 4
        WHEN input.has_backlog THEN 7
        ELSE 8
    END AS primary_status_rank,
    COALESCE((
        SELECT json_group_array(node_id)
        FROM (
            SELECT position.node_id
            FROM current_positions position
            WHERE position.task_id = task.id
            ORDER BY position.node_id
        )
    ), '[]') AS node_ids_json,
    COALESCE((
        SELECT json_group_array(attention_type)
        FROM (
            SELECT 'approval' AS attention_type WHERE input.has_waiting_approval
            UNION
            SELECT 'interrupted' WHERE input.has_interrupted_attention
            ORDER BY attention_type
        )
    ), '[]') AS attention_types_json
FROM task_records task
JOIN status_inputs input ON input.task_id = task.id;

-- +goose StatementBegin
CREATE TRIGGER workflow_nodes_current_task_anchor_delete
BEFORE DELETE ON workflow_nodes
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_pending_approvals approval
    WHERE approval.source_node_id = OLD.id
)
OR EXISTS (
    SELECT 1
    FROM task_pending_approval_branches branch
    WHERE json_extract(branch.target_snapshot_json, '$.node_id') = OLD.id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node has current task references');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER workflow_nodes_task_reference_kind_update
BEFORE UPDATE OF kind ON workflow_nodes
FOR EACH ROW
WHEN NEW.kind != OLD.kind
AND (
    EXISTS (
        SELECT 1
        FROM task_current_nodes current_node
        WHERE current_node.node_id = OLD.id
    )
    OR EXISTS (
        SELECT 1
        FROM task_pending_approvals approval
        WHERE approval.source_node_id = OLD.id
    )
    OR EXISTS (
        SELECT 1
        FROM task_pending_approval_branches branch
        WHERE json_extract(branch.target_snapshot_json, '$.node_id') = OLD.id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node kind changes are blocked for nodes referenced by current task state');
END;
-- +goose StatementEnd

-- +goose Down
SELECT kent_workflow_run_history_cutover_is_irreversible();
