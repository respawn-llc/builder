-- +goose Up
-- +goose NO TRANSACTION

PRAGMA legacy_alter_table = ON;
PRAGMA foreign_keys = OFF;

DROP VIEW IF EXISTS workflow_task_status_records;
DROP VIEW IF EXISTS workflow_task_current_run_records;
DROP VIEW IF EXISTS workflow_task_status_task_records;
DROP VIEW IF EXISTS workflow_task_status_run_records;
DROP VIEW IF EXISTS workflow_task_status_transition_records;
DROP VIEW IF EXISTS task_transition_edge_records;
DROP VIEW IF EXISTS task_transition_records;
DROP VIEW IF EXISTS task_run_records;

CREATE TABLE task_runs_new (
    id TEXT PRIMARY KEY,
    placement_id TEXT NOT NULL REFERENCES task_node_placements(id) ON DELETE CASCADE,
    session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    run_generation INTEGER NOT NULL DEFAULT 0 CHECK (run_generation >= 0),
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    automation_requested_at_unix_ms INTEGER CHECK (automation_requested_at_unix_ms > 0),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    started_at_unix_ms INTEGER CHECK (started_at_unix_ms > 0),
    completed_at_unix_ms INTEGER CHECK (completed_at_unix_ms > 0),
    interrupted_at_unix_ms INTEGER CHECK (interrupted_at_unix_ms > 0),
    interruption_reason TEXT CHECK (interruption_reason IS NULL OR length(trim(interruption_reason)) > 0),
    interruption_detail_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(interruption_detail_json)),
    waiting_ask_id TEXT CHECK (waiting_ask_id IS NULL OR length(trim(waiting_ask_id)) > 0),
    effective_completion_mode TEXT CHECK (effective_completion_mode IS NULL OR effective_completion_mode IN ('structured_output', 'tool', 'shell_command', 'unstructured_output')),
    invalid_completion_count INTEGER NOT NULL DEFAULT 0 CHECK (invalid_completion_count >= 0),
    run_start_snapshot_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(run_start_snapshot_json)),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json))
);

INSERT INTO task_runs_new (
    id,
    placement_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json,
    waiting_ask_id,
    effective_completion_mode,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
)
SELECT
    id,
    placement_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    NULLIF(automation_requested_at_unix_ms, 0),
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json,
    waiting_ask_id,
    effective_completion_mode,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
FROM task_runs;

DROP TABLE task_runs;
ALTER TABLE task_runs_new RENAME TO task_runs;

CREATE INDEX task_runs_placement_idx
    ON task_runs(placement_id);

CREATE INDEX task_runs_session_idx
    ON task_runs(session_id);

CREATE INDEX task_runs_runnable_idx
    ON task_runs(automation_requested_at_unix_ms, id)
    WHERE automation_requested_at_unix_ms IS NOT NULL
      AND completed_at_unix_ms IS NULL
      AND interrupted_at_unix_ms IS NULL;

CREATE INDEX task_runs_outcome_idx
    ON task_runs(started_at_unix_ms, completed_at_unix_ms, interrupted_at_unix_ms);

CREATE INDEX task_runs_placement_created_idx
    ON task_runs(placement_id, created_at_unix_ms DESC);

CREATE TABLE task_transitions_new (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    source_run_id TEXT REFERENCES task_runs(id) ON DELETE SET NULL,
    source_placement_id TEXT REFERENCES task_node_placements(id) ON DELETE SET NULL,
    source_node_key TEXT NOT NULL DEFAULT '',
    source_node_display_name TEXT NOT NULL DEFAULT '',
    transition_id TEXT NOT NULL,
    transition_display_name TEXT NOT NULL DEFAULT '',
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    actor TEXT NOT NULL CHECK (actor IN ('agent', 'script', 'user', 'system')),
    state TEXT NOT NULL CHECK (state IN ('pending_approval', 'approved', 'applied', 'rejected', 'invalid')),
    commentary TEXT NOT NULL DEFAULT '' CHECK (length(commentary) <= 65536),
    output_values_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_values_json)),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    applied_at_unix_ms INTEGER CHECK (applied_at_unix_ms > 0)
);

INSERT INTO task_transitions_new (
    id,
    task_id,
    source_run_id,
    source_placement_id,
    source_node_key,
    source_node_display_name,
    transition_id,
    transition_display_name,
    workflow_revision_seen,
    actor,
    state,
    commentary,
    output_values_json,
    created_at_unix_ms,
    applied_at_unix_ms
)
SELECT
    id,
    task_id,
    source_run_id,
    source_placement_id,
    source_node_key,
    source_node_display_name,
    transition_id,
    transition_display_name,
    workflow_revision_seen,
    actor,
    state,
    commentary,
    output_values_json,
    created_at_unix_ms,
    NULLIF(applied_at_unix_ms, 0)
FROM task_transitions;

DROP TABLE task_transitions;
ALTER TABLE task_transitions_new RENAME TO task_transitions;

CREATE INDEX task_transitions_task_created_idx
    ON task_transitions(task_id, created_at_unix_ms DESC);

CREATE VIEW task_run_records AS
SELECT
    r.id,
    p.task_id,
    r.placement_id,
    p.node_id,
    r.session_id,
    r.run_generation,
    r.workflow_revision_seen,
    r.automation_requested_at_unix_ms,
    r.created_at_unix_ms,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.interruption_reason,
    r.interruption_detail_json,
    r.waiting_ask_id,
    r.effective_completion_mode,
    r.invalid_completion_count,
    r.run_start_snapshot_json,
    r.metadata_json
FROM task_runs r
JOIN task_node_placements p ON p.id = r.placement_id;

CREATE VIEW task_transition_edge_records AS
SELECT
    te.id,
    te.task_transition_id,
    te.workflow_edge_id,
    te.edge_key,
    tt.workflow_revision_seen,
    te.target_node_id,
    te.target_node_key,
    te.target_node_display_name,
    te.target_node_kind,
    te.target_placement_id,
    te.state,
    te.context_mode,
    te.requires_approval,
    te.input_bindings_json,
    te.output_requirements_json,
    te.metadata_json
FROM task_transition_edges te
JOIN task_transitions tt ON tt.id = te.task_transition_id;

CREATE VIEW task_transition_records AS
SELECT
    tt.id,
    tt.task_id,
    tt.source_run_id,
    tt.source_placement_id,
    p.node_id AS source_node_id,
    tt.source_node_key,
    tt.source_node_display_name,
    derived_group_edge.transition_group_id,
    tt.transition_id,
    tt.transition_display_name,
    tt.workflow_revision_seen,
    tt.actor,
    tt.state,
    tt.commentary,
    tt.output_values_json,
    tt.created_at_unix_ms,
    tt.applied_at_unix_ms
FROM task_transitions tt
LEFT JOIN task_node_placements p ON p.id = tt.source_placement_id
LEFT JOIN task_transition_edges derived_transition_edge ON derived_transition_edge.id = (
    SELECT te.id
    FROM task_transition_edges te
    JOIN workflow_edges e ON e.id = te.workflow_edge_id
    WHERE te.task_transition_id = tt.id
      AND NOT EXISTS (
          SELECT 1
          FROM task_transition_edges other_te
          JOIN workflow_edges other_e ON other_e.id = other_te.workflow_edge_id
          WHERE other_te.task_transition_id = tt.id
            AND other_e.transition_group_id != e.transition_group_id
      )
    ORDER BY te.rowid ASC
    LIMIT 1
)
LEFT JOIN workflow_edges derived_group_edge ON derived_group_edge.id = derived_transition_edge.workflow_edge_id;

-- +goose StatementBegin
CREATE TRIGGER task_transitions_runtime_insert
BEFORE INSERT ON task_transitions
FOR EACH ROW
WHEN (
    NEW.source_run_id IS NOT NULL
    AND trim(NEW.source_run_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_run_records r
        WHERE r.id = NEW.source_run_id
          AND r.task_id = NEW.task_id
    )
)
OR (
    NEW.source_placement_id IS NOT NULL
    AND trim(NEW.source_placement_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_node_placements p
        WHERE p.id = NEW.source_placement_id
          AND p.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task transition references must stay within one task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_transitions_runtime_update
BEFORE UPDATE OF task_id, source_run_id, source_placement_id, transition_id ON task_transitions
FOR EACH ROW
WHEN (
    NEW.source_run_id IS NOT NULL
    AND trim(NEW.source_run_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_run_records r
        WHERE r.id = NEW.source_run_id
          AND r.task_id = NEW.task_id
    )
)
OR (
    NEW.source_placement_id IS NOT NULL
    AND trim(NEW.source_placement_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_node_placements p
        WHERE p.id = NEW.source_placement_id
          AND p.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task transition references must stay within one task workflow');
END;
-- +goose StatementEnd

CREATE VIEW workflow_task_status_task_records AS
SELECT
    t.id,
    t.canceled_at_unix_ms
FROM task_records t;

CREATE VIEW workflow_task_status_run_records AS
SELECT
    r.id,
    r.task_id,
    r.placement_id,
    r.session_id,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.interruption_reason,
    r.waiting_ask_id
FROM task_run_records r;

CREATE VIEW workflow_task_status_transition_records AS
SELECT
    tt.task_id,
    tt.state,
    tt.source_node_id
FROM task_transition_records tt;

CREATE VIEW workflow_task_current_run_records AS
SELECT
    r.id,
    r.task_id,
    r.placement_id,
    r.session_id,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.waiting_ask_id
FROM workflow_task_status_run_records r
JOIN task_node_placements p ON p.id = r.placement_id
JOIN workflow_nodes n ON n.id = p.node_id
WHERE p.state IN ('active', 'waiting_approval')
  AND n.kind IN ('agent', 'script');

CREATE VIEW workflow_task_status_records AS
WITH current_positions AS (
    SELECT
        p.task_id,
        p.node_id,
        p.state
    FROM task_node_placements p
    WHERE p.state IN ('active', 'waiting_approval')

    UNION

    SELECT
        tt.task_id,
        tt.source_node_id AS node_id,
        'waiting_approval' AS state
    FROM workflow_task_status_transition_records tt
    WHERE tt.state = 'pending_approval'
      AND tt.source_node_id IS NOT NULL
),
unfinished_current_runs AS (
    SELECT
        r.id,
        r.task_id,
        r.started_at_unix_ms,
        r.interrupted_at_unix_ms,
        r.waiting_ask_id
    FROM workflow_task_current_run_records r
    WHERE r.completed_at_unix_ms IS NULL
),
status_inputs AS (
    SELECT
        t.id AS task_id,
        EXISTS (
            SELECT 1
            FROM current_positions position
            JOIN workflow_nodes n ON n.id = position.node_id
            WHERE position.task_id = t.id
              AND position.state = 'active'
              AND n.kind = 'terminal'
        ) AS has_done,
        EXISTS (
            SELECT 1
            FROM current_positions position
            WHERE position.task_id = t.id
              AND position.state = 'waiting_approval'
        ) AS has_waiting_approval,
        EXISTS (
            SELECT 1
            FROM unfinished_current_runs r
            WHERE r.task_id = t.id
              AND r.waiting_ask_id IS NOT NULL
        ) AS has_waiting_question,
        EXISTS (
            SELECT 1
            FROM unfinished_current_runs r
            WHERE r.task_id = t.id
              AND r.interrupted_at_unix_ms IS NOT NULL
        ) AS has_interrupted,
        EXISTS (
            SELECT 1
            FROM unfinished_current_runs r
            WHERE r.task_id = t.id
              AND r.started_at_unix_ms IS NOT NULL
              AND r.interrupted_at_unix_ms IS NULL
        ) AS has_running,
        EXISTS (
            SELECT 1
            FROM unfinished_current_runs r
            WHERE r.task_id = t.id
              AND r.started_at_unix_ms IS NULL
              AND r.interrupted_at_unix_ms IS NULL
        ) AS has_queued,
        EXISTS (
            SELECT 1
            FROM current_positions position
            JOIN workflow_nodes n ON n.id = position.node_id
            WHERE position.task_id = t.id
              AND position.state = 'active'
              AND n.kind = 'start'
        ) AS has_backlog
    FROM workflow_task_status_task_records t
)
SELECT
    t.id AS task_id,
    CAST(inputs.has_done AS INTEGER) AS is_done,
    CASE
        WHEN t.canceled_at_unix_ms IS NOT NULL THEN 'canceled'
        WHEN inputs.has_done THEN 'done'
        WHEN inputs.has_waiting_question THEN 'waiting_question'
        WHEN inputs.has_waiting_approval THEN 'waiting_approval'
        WHEN inputs.has_interrupted THEN 'interrupted'
        WHEN inputs.has_running THEN 'running'
        WHEN inputs.has_queued THEN 'queued'
        WHEN inputs.has_backlog THEN 'backlog'
        ELSE 'active'
    END AS kind,
    CASE
        WHEN t.canceled_at_unix_ms IS NOT NULL THEN 'canceled'
        WHEN inputs.has_done THEN 'terminal'
        WHEN inputs.has_waiting_question THEN 'waiting_ask'
        WHEN inputs.has_waiting_approval THEN 'waiting_approval'
        WHEN inputs.has_interrupted THEN 'interrupted'
        WHEN inputs.has_running THEN 'running'
        WHEN inputs.has_queued THEN 'queued'
        ELSE 'active'
    END AS native_state,
    CASE
        WHEN t.canceled_at_unix_ms IS NOT NULL THEN 0
        WHEN inputs.has_done THEN 1
        WHEN inputs.has_waiting_question THEN 2
        WHEN inputs.has_waiting_approval THEN 3
        WHEN inputs.has_interrupted THEN 4
        WHEN inputs.has_running THEN 5
        WHEN inputs.has_queued THEN 6
        WHEN inputs.has_backlog THEN 7
        ELSE 8
    END AS primary_status_rank,
    COALESCE((
        SELECT json_group_array(node_id)
        FROM (
            SELECT DISTINCT position.node_id
            FROM current_positions position
            WHERE position.task_id = t.id
            ORDER BY position.node_id ASC
        )
    ), '[]') AS node_ids_json,
    COALESCE((
        SELECT json_group_array(id)
        FROM (
            SELECT r.id
            FROM unfinished_current_runs r
            WHERE r.task_id = t.id
            ORDER BY r.id ASC
        )
    ), '[]') AS run_ids_json,
    COALESCE((
        SELECT json_group_array(attention_type)
        FROM (
            SELECT 'approval' AS attention_type
            WHERE inputs.has_waiting_approval

            UNION

            SELECT 'question' AS attention_type
            WHERE inputs.has_waiting_question

            UNION

            SELECT 'interrupted' AS attention_type
            WHERE inputs.has_interrupted

            ORDER BY attention_type ASC
        )
    ), '[]') AS attention_types_json
FROM workflow_task_status_task_records t
JOIN status_inputs inputs ON inputs.task_id = t.id;

PRAGMA foreign_keys = ON;
PRAGMA legacy_alter_table = OFF;
