-- +goose Up

DROP VIEW workflow_task_status_records;

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
                  AND position.interruption_reason NOT IN ('user_interrupted', 'workflow_runtime_canceled')
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
