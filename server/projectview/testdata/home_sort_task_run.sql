INSERT INTO task_runs (
    id, placement_id, session_id, run_generation, workflow_revision_seen,
    automation_requested_at_unix_ms, created_at_unix_ms, updated_at_unix_ms, started_at_unix_ms,
    completed_at_unix_ms, interrupted_at_unix_ms, interruption_reason, interruption_detail_json,
    waiting_ask_id, effective_completion_mode, invalid_completion_count, run_start_snapshot_json,
    metadata_json
) VALUES (?, ?, NULL, 0, 1, ?, ?, ?, NULL, NULL, NULL, NULL, '{}', NULL, NULL, 0, '{}', '{}')
