-- +goose Up

DROP VIEW workflow_task_status_records;

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
    JOIN workflow_task_status_task_records t ON t.id = tt.task_id
    WHERE tt.state = 'pending_approval'
      AND tt.source_node_id IS NOT NULL
      AND t.canceled_at_unix_ms IS NULL
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
        (
            t.canceled_at_unix_ms IS NULL
            AND EXISTS (
                SELECT 1
                FROM current_positions position
                WHERE position.task_id = t.id
                  AND position.state = 'waiting_approval'
            )
        ) AS has_waiting_approval,
        (
            t.canceled_at_unix_ms IS NULL
            AND EXISTS (
                SELECT 1
                FROM unfinished_current_runs r
                WHERE r.task_id = t.id
                  AND r.waiting_ask_id IS NOT NULL
            )
        ) AS has_waiting_question,
        (
            t.canceled_at_unix_ms IS NULL
            AND EXISTS (
                SELECT 1
                FROM unfinished_current_runs r
                WHERE r.task_id = t.id
                  AND r.interrupted_at_unix_ms IS NOT NULL
            )
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
