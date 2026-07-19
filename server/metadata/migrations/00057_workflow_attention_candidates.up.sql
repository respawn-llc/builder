-- +goose Up

CREATE VIEW workflow_attention_candidates AS
-- The unreachable first branch declares the relation's required and nullable
-- column types explicitly for generated query bindings.
SELECT
    CAST('attention_kind' AS TEXT) AS kind,
    CAST('attention_id' AS TEXT) AS id,
    CAST('project_id' AS TEXT) AS project_id,
    CAST('workflow_id' AS TEXT) AS workflow_id,
    CAST(NULL AS TEXT) AS task_id,
    CAST(NULL AS TEXT) AS short_id,
    CAST(NULL AS TEXT) AS title,
    CAST(NULL AS TEXT) AS run_id,
    CAST(NULL AS TEXT) AS session_id,
    CAST(NULL AS TEXT) AS ask_id,
    CAST(NULL AS TEXT) AS task_transition_id,
    CAST(NULL AS TEXT) AS interruption_reason,
    CAST(NULL AS TEXT) AS interruption_detail_json,
    CAST(0 AS INTEGER) AS occurred_at_unix_ms
WHERE 0

UNION ALL

SELECT
    CAST('approval' AS TEXT) AS kind,
    CAST('approval:' || transition.id AS TEXT) AS id,
    task.project_id,
    task.workflow_id,
    task.id AS task_id,
    task.short_id,
    task.title,
    transition.source_run_id AS run_id,
    source_run.session_id,
    CAST(NULL AS TEXT) AS ask_id,
    transition.id AS task_transition_id,
    CAST(NULL AS TEXT) AS interruption_reason,
    CAST(NULL AS TEXT) AS interruption_detail_json,
    transition.created_at_unix_ms AS occurred_at_unix_ms
FROM task_transition_records transition
JOIN task_records task ON task.id = transition.task_id
LEFT JOIN task_run_records source_run ON source_run.id = transition.source_run_id
WHERE transition.state = 'pending_approval'
  AND task.canceled_at_unix_ms IS NULL

UNION ALL

SELECT
    CAST('question' AS TEXT) AS kind,
    CAST('question:' || run.id || ':' || run.waiting_ask_id AS TEXT) AS id,
    task.project_id,
    task.workflow_id,
    task.id AS task_id,
    task.short_id,
    task.title,
    run.id AS run_id,
    run.session_id,
    run.waiting_ask_id AS ask_id,
    CAST(NULL AS TEXT) AS task_transition_id,
    CAST(NULL AS TEXT) AS interruption_reason,
    CAST(NULL AS TEXT) AS interruption_detail_json,
    run.updated_at_unix_ms AS occurred_at_unix_ms
FROM workflow_task_current_run_records run
JOIN task_records task ON task.id = run.task_id
JOIN workflow_task_status_task_records status_task ON status_task.id = task.id
WHERE run.waiting_ask_id IS NOT NULL
  AND run.completed_at_unix_ms IS NULL
  AND run.interrupted_at_unix_ms IS NULL
  AND status_task.canceled_at_unix_ms IS NULL

UNION ALL

SELECT
    CAST('interrupted_run' AS TEXT) AS kind,
    CAST('interrupted_run:' || run.id AS TEXT) AS id,
    task.project_id,
    task.workflow_id,
    task.id AS task_id,
    task.short_id,
    task.title,
    run.id AS run_id,
    run.session_id,
    CAST(NULL AS TEXT) AS ask_id,
    CAST(NULL AS TEXT) AS task_transition_id,
    run.interruption_reason,
    run.interruption_detail_json,
    run.interrupted_at_unix_ms AS occurred_at_unix_ms
FROM task_run_records run
JOIN task_records task ON task.id = run.task_id
JOIN task_node_placements placement ON placement.id = run.placement_id
WHERE run.interrupted_at_unix_ms IS NOT NULL
  AND run.completed_at_unix_ms IS NULL
  AND trim(run.interruption_reason) != ''
  AND run.interruption_reason NOT IN ('user_interrupt', 'workflow_runtime_canceled')
  AND placement.state IN ('active', 'waiting_approval')
  AND task.canceled_at_unix_ms IS NULL

UNION ALL

SELECT
    CAST('validation_blocker' AS TEXT) AS kind,
    CAST('validation_blocker:' || link.project_id || ':' || link.workflow_id AS TEXT) AS id,
    link.project_id,
    link.workflow_id,
    CAST(NULL AS TEXT) AS task_id,
    CAST(NULL AS TEXT) AS short_id,
    CAST(NULL AS TEXT) AS title,
    CAST(NULL AS TEXT) AS run_id,
    CAST(NULL AS TEXT) AS session_id,
    CAST(NULL AS TEXT) AS ask_id,
    CAST(NULL AS TEXT) AS task_transition_id,
    CAST(NULL AS TEXT) AS interruption_reason,
    CAST(NULL AS TEXT) AS interruption_detail_json,
    link.updated_at_unix_ms AS occurred_at_unix_ms
FROM project_workflow_links link;
