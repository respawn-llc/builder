INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id, source_node_key,
    source_node_display_name, transition_id, transition_display_name,
    workflow_revision_seen, actor, state, commentary, output_values_json, created_at_unix_ms,
    applied_at_unix_ms
) VALUES (?, ?, NULL, NULL, '', '', 'manual', 'Manual', 1, 'user', 'applied', '', '{}', ?, ?)
