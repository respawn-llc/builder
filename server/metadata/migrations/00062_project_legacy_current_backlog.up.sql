-- +goose Up

CREATE TEMP TABLE migration_unfinished_current_node_errors (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    transition_branch_key TEXT,
    unfinished_run_count INTEGER NOT NULL
);

-- +goose StatementBegin
CREATE TEMP TRIGGER migration_unfinished_current_node_errors_abort
BEFORE INSERT ON migration_unfinished_current_node_errors
FOR EACH ROW
BEGIN
    SELECT RAISE(
        ABORT,
        'current node migration failure: task_id=' || NEW.task_id ||
        ', node_id=' || NEW.node_id ||
        ', transition_branch_key=' || COALESCE(NEW.transition_branch_key, 'serial') ||
        ', unfinished_run_count=' || NEW.unfinished_run_count
    );
END;
-- +goose StatementEnd

INSERT INTO migration_unfinished_current_node_errors (
    task_id,
    node_id,
    transition_branch_key,
    unfinished_run_count
)
SELECT
    placement.task_id,
    placement.node_id,
    branch_edge.edge_key,
    COUNT(run.id)
FROM task_node_placements placement
JOIN task_records task ON task.id = placement.task_id
JOIN workflow_nodes node ON node.id = placement.node_id
LEFT JOIN workflow_edges branch_edge ON branch_edge.id = placement.parallel_branch_edge_id
JOIN task_runs run ON run.placement_id = placement.id
WHERE task.canceled_at_unix_ms IS NULL
  AND placement.state = 'active'
  AND node.kind IN ('agent', 'script')
  AND run.completed_at_unix_ms IS NULL
GROUP BY placement.id
HAVING COUNT(run.id) > 1;

INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
)
SELECT
    placement.task_id,
    placement.node_id,
    NULL,
    '{}',
    '{}',
    NULL,
    NULL,
    NULL,
    NULL,
    NULL
FROM task_node_placements placement
JOIN workflow_nodes node ON node.id = placement.node_id
JOIN task_records task ON task.id = placement.task_id
WHERE placement.state = 'active'
  AND task.canceled_at_unix_ms IS NULL
  AND node.kind IN ('start', 'terminal')
  AND placement.parallel_batch_transition_id IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      WHERE current_node.task_id = placement.task_id
  );

INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
)
SELECT
    placement.task_id,
    placement.node_id,
    NULL,
    '{}',
    '{}',
    CASE
        WHEN node.kind = 'agent' THEN run.session_id
        ELSE NULL
    END,
    'interrupted',
    CASE
        WHEN run.interrupted_at_unix_ms IS NOT NULL THEN run.interruption_reason
        ELSE 'server_restart'
    END,
    CASE
        WHEN run.interrupted_at_unix_ms IS NOT NULL THEN run.interruption_detail_json
        ELSE json_object(
            'code', 'workflow.execution.restarted',
            'fields', json_object('operation', 'recovery')
        )
    END,
    CASE
        WHEN run.interrupted_at_unix_ms IS NOT NULL THEN run.interrupted_at_unix_ms
        ELSE run.updated_at_unix_ms
    END
FROM task_node_placements placement
JOIN workflow_nodes node ON node.id = placement.node_id
JOIN task_runs run ON run.placement_id = placement.id
JOIN task_records task ON task.id = placement.task_id
WHERE placement.state = 'active'
  AND task.canceled_at_unix_ms IS NULL
  AND node.kind IN ('agent', 'script')
  AND placement.parallel_batch_transition_id IS NULL
  AND run.completed_at_unix_ms IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      WHERE current_node.task_id = placement.task_id
  );

INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
)
SELECT
    placement.task_id,
    placement.node_id,
    NULL,
    '{}',
    '{}',
    NULL,
    'interrupted',
    'server_restart',
    json_object(
        'code', 'workflow.execution.restarted',
        'fields', json_object('operation', 'recovery')
    ),
    placement.updated_at_unix_ms
FROM task_node_placements placement
JOIN workflow_nodes node ON node.id = placement.node_id
JOIN task_records task ON task.id = placement.task_id
WHERE placement.state = 'active'
  AND task.canceled_at_unix_ms IS NULL
  AND node.kind IN ('agent', 'script')
  AND placement.parallel_batch_transition_id IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM task_runs run
      WHERE run.placement_id = placement.id
        AND run.completed_at_unix_ms IS NULL
  )
  AND NOT EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      WHERE current_node.task_id = placement.task_id
  );

INSERT INTO task_active_fanouts (task_id)
SELECT DISTINCT placement.task_id
FROM task_node_placements placement
JOIN task_records task ON task.id = placement.task_id
WHERE placement.parallel_batch_transition_id IS NOT NULL
  AND task.canceled_at_unix_ms IS NULL
  AND EXISTS (
      SELECT 1
      FROM task_node_placements active_branch
      WHERE active_branch.task_id = placement.task_id
        AND active_branch.parallel_batch_transition_id = placement.parallel_batch_transition_id
        AND active_branch.state IN ('active', 'waiting_approval')
  )
  AND NOT EXISTS (
      SELECT 1
      FROM task_active_fanouts fanout
      WHERE fanout.task_id = placement.task_id
  );

INSERT INTO task_active_fanout_branches (
    task_id,
    transition_branch_key,
    arrival_state,
    arrival_values_json
)
SELECT
    placement.task_id,
    edge.edge_key,
    CASE
        WHEN placement.state = 'completed' THEN 'arrived'
        ELSE 'pending'
    END,
    CASE
        WHEN placement.state = 'completed' THEN (
            SELECT arrival.output_values_json
            FROM task_transitions arrival
            JOIN task_transition_edges arrival_edge
                ON arrival_edge.task_transition_id = arrival.id
            WHERE arrival.source_placement_id = placement.id
              AND arrival_edge.state = 'applied'
              AND arrival_edge.target_node_kind = 'join'
            ORDER BY arrival.created_at_unix_ms DESC, arrival.rowid DESC, arrival_edge.rowid DESC
            LIMIT 1
        )
        ELSE NULL
    END
FROM task_node_placements placement
JOIN workflow_edges edge ON edge.id = placement.parallel_branch_edge_id
JOIN task_records task ON task.id = placement.task_id
WHERE placement.parallel_batch_transition_id IS NOT NULL
  AND task.canceled_at_unix_ms IS NULL
  AND EXISTS (
      SELECT 1
      FROM task_node_placements active_branch
      WHERE active_branch.task_id = placement.task_id
        AND active_branch.parallel_batch_transition_id = placement.parallel_batch_transition_id
        AND active_branch.state IN ('active', 'waiting_approval')
  )
  AND (
      placement.state IN ('active', 'waiting_approval')
      OR (
          placement.state = 'completed'
          AND EXISTS (
              SELECT 1
              FROM task_transitions arrival
              JOIN task_transition_edges arrival_edge
                  ON arrival_edge.task_transition_id = arrival.id
              WHERE arrival.source_placement_id = placement.id
                AND arrival_edge.state = 'applied'
                AND arrival_edge.target_node_kind = 'join'
          )
      )
  );

INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
)
SELECT
    placement.task_id,
    placement.node_id,
    edge.edge_key,
    '{}',
    '{}',
    CASE
        WHEN node.kind = 'agent' THEN run.session_id
        ELSE NULL
    END,
    'interrupted',
    CASE
        WHEN run.interrupted_at_unix_ms IS NOT NULL THEN run.interruption_reason
        ELSE 'server_restart'
    END,
    CASE
        WHEN run.interrupted_at_unix_ms IS NOT NULL THEN run.interruption_detail_json
        ELSE json_object(
            'code', 'workflow.execution.restarted',
            'fields', json_object('operation', 'recovery')
        )
    END,
    CASE
        WHEN run.interrupted_at_unix_ms IS NOT NULL THEN run.interrupted_at_unix_ms
        ELSE run.updated_at_unix_ms
    END
FROM task_node_placements placement
JOIN workflow_edges edge ON edge.id = placement.parallel_branch_edge_id
JOIN workflow_nodes node ON node.id = placement.node_id
JOIN task_runs run ON run.placement_id = placement.id
JOIN task_records task ON task.id = placement.task_id
WHERE placement.parallel_batch_transition_id IS NOT NULL
  AND task.canceled_at_unix_ms IS NULL
  AND placement.state = 'active'
  AND node.kind IN ('agent', 'script')
  AND run.completed_at_unix_ms IS NULL;

INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
)
SELECT
    placement.task_id,
    placement.node_id,
    edge.edge_key,
    '{}',
    '{}',
    NULL,
    'interrupted',
    'server_restart',
    json_object(
        'code', 'workflow.execution.restarted',
        'fields', json_object('operation', 'recovery')
    ),
    placement.updated_at_unix_ms
FROM task_node_placements placement
JOIN workflow_edges edge ON edge.id = placement.parallel_branch_edge_id
JOIN workflow_nodes node ON node.id = placement.node_id
JOIN task_records task ON task.id = placement.task_id
WHERE placement.parallel_batch_transition_id IS NOT NULL
  AND task.canceled_at_unix_ms IS NULL
  AND placement.state = 'active'
  AND node.kind IN ('agent', 'script')
  AND NOT EXISTS (
      SELECT 1
      FROM task_runs run
      WHERE run.placement_id = placement.id
        AND run.completed_at_unix_ms IS NULL
  )
  AND NOT EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      WHERE current_node.task_id = placement.task_id
        AND current_node.transition_branch_key = edge.edge_key
  );

INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
)
SELECT
    transition.task_id,
    placement.node_id,
    NULL,
    '{}',
    '{}',
    CASE
        WHEN node.kind = 'agent' THEN run.session_id
        ELSE NULL
    END,
    NULL,
    NULL,
    NULL,
    NULL
FROM task_transitions transition
JOIN task_node_placements placement ON placement.id = transition.source_placement_id
JOIN workflow_nodes node ON node.id = placement.node_id
LEFT JOIN task_runs run ON run.id = transition.source_run_id
JOIN task_records task ON task.id = transition.task_id
WHERE transition.state = 'pending_approval'
  AND task.canceled_at_unix_ms IS NULL
  AND placement.state IN ('active', 'waiting_approval')
  AND placement.parallel_batch_transition_id IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      WHERE current_node.task_id = transition.task_id
  );

INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
)
SELECT
    transition.task_id,
    placement.node_id,
    source_branch.edge_key,
    '{}',
    '{}',
    CASE
        WHEN node.kind = 'agent' THEN run.session_id
        ELSE NULL
    END,
    NULL,
    NULL,
    NULL,
    NULL
FROM task_transitions transition
JOIN task_node_placements placement ON placement.id = transition.source_placement_id
JOIN workflow_nodes node ON node.id = placement.node_id
JOIN workflow_edges source_branch ON source_branch.id = placement.parallel_branch_edge_id
LEFT JOIN task_runs run ON run.id = transition.source_run_id
JOIN task_records task ON task.id = transition.task_id
WHERE transition.state = 'pending_approval'
  AND task.canceled_at_unix_ms IS NULL
  AND placement.state IN ('active', 'waiting_approval')
  AND placement.parallel_batch_transition_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      WHERE current_node.task_id = transition.task_id
        AND current_node.node_id = placement.node_id
        AND current_node.transition_branch_key = source_branch.edge_key
  );

CREATE TEMP TABLE migration_pending_approval_ids (
    transition_id TEXT PRIMARY KEY,
    approval_id TEXT NOT NULL
);

CREATE TEMP TABLE migration_pending_approval_source_errors (
    task_id TEXT NOT NULL,
    transition_id TEXT NOT NULL
);

-- +goose StatementBegin
CREATE TEMP TRIGGER migration_pending_approval_source_errors_abort
BEFORE INSERT ON migration_pending_approval_source_errors
FOR EACH ROW
BEGIN
    SELECT RAISE(
        ABORT,
        'pending approval migration failure: task_id=' || NEW.task_id ||
        ', transition_id=' || NEW.transition_id ||
        ', current source is absent'
    );
END;
-- +goose StatementEnd

INSERT INTO migration_pending_approval_source_errors (task_id, transition_id)
SELECT
    transition.task_id,
    transition.id
FROM task_transitions transition
JOIN task_records task ON task.id = transition.task_id
LEFT JOIN task_node_placements placement ON placement.id = transition.source_placement_id
LEFT JOIN workflow_edges source_branch ON source_branch.id = placement.parallel_branch_edge_id
LEFT JOIN task_current_nodes current_node
    ON current_node.task_id = transition.task_id
   AND current_node.node_id = placement.node_id
   AND (
       (source_branch.edge_key IS NULL AND current_node.transition_branch_key IS NULL)
       OR current_node.transition_branch_key = source_branch.edge_key
   )
WHERE transition.state = 'pending_approval'
  AND task.canceled_at_unix_ms IS NULL
  AND current_node.task_id IS NULL;

INSERT INTO migration_pending_approval_ids (transition_id, approval_id)
SELECT
    transition.id,
    lower(
        hex(randomblob(4)) || '-' ||
        hex(randomblob(2)) || '-4' ||
        substr(hex(randomblob(2)), 2) || '-8' ||
        substr(hex(randomblob(2)), 2) || '-' ||
        hex(randomblob(6))
    )
FROM task_transitions transition
JOIN task_node_placements placement ON placement.id = transition.source_placement_id
JOIN task_records task ON task.id = transition.task_id
WHERE transition.state = 'pending_approval'
  AND task.canceled_at_unix_ms IS NULL
  AND placement.parallel_batch_transition_id IS NULL
  AND EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      WHERE current_node.task_id = transition.task_id
        AND current_node.node_id = placement.node_id
        AND current_node.transition_branch_key IS NULL
  );

INSERT INTO migration_pending_approval_ids (transition_id, approval_id)
SELECT
    transition.id,
    lower(
        hex(randomblob(4)) || '-' ||
        hex(randomblob(2)) || '-4' ||
        substr(hex(randomblob(2)), 2) || '-8' ||
        substr(hex(randomblob(2)), 2) || '-' ||
        hex(randomblob(6))
    )
FROM task_transitions transition
JOIN task_node_placements placement ON placement.id = transition.source_placement_id
JOIN workflow_edges source_branch ON source_branch.id = placement.parallel_branch_edge_id
JOIN task_records task ON task.id = transition.task_id
WHERE transition.state = 'pending_approval'
  AND task.canceled_at_unix_ms IS NULL
  AND placement.parallel_batch_transition_id IS NOT NULL
  AND EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      WHERE current_node.task_id = transition.task_id
        AND current_node.node_id = placement.node_id
        AND current_node.transition_branch_key = source_branch.edge_key
  );

INSERT INTO task_pending_approvals (
    id,
    source_task_id,
    source_node_id,
    source_transition_branch_key,
    source_session_id,
    workflow_version,
    transition_snapshot_json,
    materialized_values_json,
    created_at_unix_ms
)
SELECT
    migration.approval_id,
    transition.task_id,
    placement.node_id,
    source_branch.edge_key,
    current_node.session_id,
    transition.workflow_revision_seen,
    json_object(
        'workflow_id', task.workflow_id,
        -- Legacy transition rows no longer retain a transition-group key.
        -- This frozen transition ID is snapshot metadata only; approval
        -- application never resolves it against the mutable Workflow graph.
        'id', transition.id,
        'source_node_id', placement.node_id,
        'transition_id', transition.transition_id,
        'display_name', COALESCE(NULLIF(transition.transition_display_name, ''), transition.transition_id),
        'description', '',
        'source_display_name', COALESCE(NULLIF(transition.source_node_display_name, ''), transition.source_node_key)
    ),
    transition.output_values_json,
    transition.created_at_unix_ms
FROM migration_pending_approval_ids migration
JOIN task_transitions transition ON transition.id = migration.transition_id
JOIN task_node_placements placement ON placement.id = transition.source_placement_id
JOIN task_records task ON task.id = transition.task_id
LEFT JOIN workflow_edges source_branch ON source_branch.id = placement.parallel_branch_edge_id
JOIN task_current_nodes current_node
    ON current_node.task_id = transition.task_id
   AND current_node.node_id = placement.node_id
   AND (
       (source_branch.edge_key IS NULL AND current_node.transition_branch_key IS NULL)
       OR current_node.transition_branch_key = source_branch.edge_key
   );

INSERT INTO task_pending_approval_branches (
    approval_id,
    transition_branch_key,
    target_snapshot_json,
    effective_edge_configuration_json,
    context_source_resolution_json
)
SELECT
    migration.approval_id,
    edge.edge_key,
    json_object(
        'node_id', edge.target_node_id,
        'transition_branch_key', CASE
            WHEN source_branch.edge_key IS NOT NULL THEN source_branch.edge_key
            ELSE edge.edge_key
        END,
        'display_name', edge.target_node_display_name,
        'current_input_values', json('{}'),
        'prior_node_values', json('{}'),
        'session_id', json_extract(edge.metadata_json, '$.source_session_id'),
        'scheduling_state', CASE
            WHEN edge.target_node_kind IN ('agent', 'script') THEN 'ready'
            ELSE NULL
        END
    ),
    json_object(
        'workflow_id', task.workflow_id,
        'id', COALESCE(NULLIF(edge.workflow_edge_id, ''), edge.id),
        'key', edge.edge_key,
        'transition_group_id', transition.id,
        'target_node_id', edge.target_node_id,
        'context_mode', edge.context_mode,
        'context_source', json(COALESCE(
            json_extract(edge.metadata_json, '$.context_source'),
            '{"kind":"immediate_source"}'
        )),
        'requires_approval', json(CASE
            WHEN edge.requires_approval != 0 THEN 'true'
            ELSE 'false'
        END),
        'prompt_template', COALESCE(json_extract(edge.metadata_json, '$.prompt_template'), ''),
        'parameters', json(COALESCE(
            json_extract(edge.metadata_json, '$.parameters'),
            '[]'
        )),
        'input_bindings', json(edge.input_bindings_json),
        'output_requirements', json(edge.output_requirements_json)
    ),
    json_object(
        'session_id', json_extract(edge.metadata_json, '$.source_session_id')
    )
FROM migration_pending_approval_ids migration
JOIN task_transitions transition ON transition.id = migration.transition_id
JOIN task_node_placements placement ON placement.id = transition.source_placement_id
JOIN task_records task ON task.id = transition.task_id
LEFT JOIN workflow_edges source_branch ON source_branch.id = placement.parallel_branch_edge_id
JOIN task_transition_edges edge ON edge.task_transition_id = transition.id
WHERE edge.state = 'pending';

CREATE TEMP TABLE migration_pending_approval_projection_errors (
    task_id TEXT NOT NULL,
    transition_id TEXT NOT NULL,
    expected_branch_count INTEGER NOT NULL,
    actual_header_count INTEGER NOT NULL,
    actual_branch_count INTEGER NOT NULL
);

-- +goose StatementBegin
CREATE TEMP TRIGGER migration_pending_approval_projection_errors_abort
BEFORE INSERT ON migration_pending_approval_projection_errors
FOR EACH ROW
BEGIN
    SELECT RAISE(
        ABORT,
        'pending approval migration failure: task_id=' || NEW.task_id ||
        ', transition_id=' || NEW.transition_id ||
        ', expected_branch_count=' || NEW.expected_branch_count ||
        ', actual_header_count=' || NEW.actual_header_count ||
        ', actual_branch_count=' || NEW.actual_branch_count
    );
END;
-- +goose StatementEnd

INSERT INTO migration_pending_approval_projection_errors (
    task_id,
    transition_id,
    expected_branch_count,
    actual_header_count,
    actual_branch_count
)
SELECT
    transition.task_id,
    transition.id,
    (
        SELECT COUNT(*)
        FROM task_transition_edges expected_branch
        WHERE expected_branch.task_transition_id = transition.id
          AND expected_branch.state = 'pending'
    ),
    (
        SELECT COUNT(*)
        FROM task_pending_approvals approval
        WHERE approval.id = migration.approval_id
    ),
    (
        SELECT COUNT(*)
        FROM task_pending_approval_branches branch
        WHERE branch.approval_id = migration.approval_id
    )
FROM migration_pending_approval_ids migration
JOIN task_transitions transition ON transition.id = migration.transition_id
WHERE (
    SELECT COUNT(*)
    FROM task_pending_approvals approval
    WHERE approval.id = migration.approval_id
) != 1
OR (
    SELECT COUNT(*)
    FROM task_transition_edges expected_branch
    WHERE expected_branch.task_transition_id = transition.id
      AND expected_branch.state = 'pending'
) = 0
OR (
    SELECT COUNT(*)
    FROM task_transition_edges expected_branch
    WHERE expected_branch.task_transition_id = transition.id
      AND expected_branch.state = 'pending'
) != (
    SELECT COUNT(*)
    FROM task_pending_approval_branches branch
    WHERE branch.approval_id = migration.approval_id
);

CREATE TEMP TABLE migration_invalid_canceled_tasks (
    task_id TEXT PRIMARY KEY
);

INSERT INTO migration_invalid_canceled_tasks (task_id)
SELECT task.id
FROM task_records task
WHERE task.canceled_at_unix_ms IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM workflow_nodes terminal_node
      WHERE terminal_node.workflow_id = task.workflow_id
        AND terminal_node.kind = 'terminal'
  );

DELETE FROM task_pending_approvals
WHERE source_task_id IN (
    SELECT task_id
    FROM migration_invalid_canceled_tasks
);

DELETE FROM task_current_nodes
WHERE task_id IN (
    SELECT task_id
    FROM migration_invalid_canceled_tasks
);

DELETE FROM task_active_fanouts
WHERE task_id IN (
    SELECT task_id
    FROM migration_invalid_canceled_tasks
);

UPDATE sessions
SET task_id = NULL
WHERE task_id IN (
    SELECT task_id
    FROM migration_invalid_canceled_tasks
);

DELETE FROM tasks
WHERE id IN (
    SELECT task_id
    FROM migration_invalid_canceled_tasks
);

CREATE TEMP TABLE migration_session_task_errors (
    session_id TEXT NOT NULL,
    task_count INTEGER NOT NULL
);

-- +goose StatementBegin
CREATE TEMP TRIGGER migration_session_task_errors_abort
BEFORE INSERT ON migration_session_task_errors
FOR EACH ROW
BEGIN
    SELECT RAISE(
        ABORT,
        'workflow session migration failure: session_id=' || NEW.session_id ||
        ', retained_task_count=' || NEW.task_count
    );
END;
-- +goose StatementEnd

INSERT INTO migration_session_task_errors (session_id, task_count)
SELECT
    run.session_id,
    COUNT(DISTINCT placement.task_id)
FROM task_runs run
JOIN task_node_placements placement ON placement.id = run.placement_id
JOIN workflow_nodes node ON node.id = placement.node_id
WHERE run.session_id IS NOT NULL
  AND node.kind = 'agent'
GROUP BY run.session_id
HAVING COUNT(DISTINCT placement.task_id) > 1;

UPDATE sessions
SET task_id = (
    SELECT placement.task_id
    FROM task_runs run
    JOIN task_node_placements placement ON placement.id = run.placement_id
    JOIN workflow_nodes node ON node.id = placement.node_id
    WHERE run.session_id = sessions.id
      AND node.kind = 'agent'
    ORDER BY run.updated_at_unix_ms DESC, run.id DESC
    LIMIT 1
)
WHERE EXISTS (
    SELECT 1
    FROM task_runs run
    JOIN task_node_placements placement ON placement.id = run.placement_id
    JOIN workflow_nodes node ON node.id = placement.node_id
    WHERE run.session_id = sessions.id
      AND node.kind = 'agent'
);

INSERT INTO session_workflow_node_associations (
    session_id,
    node_id,
    transition_branch_key,
    associated_at_unix_ms
)
SELECT
    run.session_id,
    placement.node_id,
    branch_edge.edge_key,
    run.updated_at_unix_ms
FROM task_runs run
JOIN task_node_placements placement ON placement.id = run.placement_id
JOIN workflow_nodes node ON node.id = placement.node_id
LEFT JOIN workflow_edges branch_edge ON branch_edge.id = placement.parallel_branch_edge_id
WHERE run.session_id IS NOT NULL
  AND node.kind = 'agent'
ON CONFLICT DO UPDATE SET
    associated_at_unix_ms = CASE
        WHEN excluded.associated_at_unix_ms > session_workflow_node_associations.associated_at_unix_ms
        THEN excluded.associated_at_unix_ms
        ELSE session_workflow_node_associations.associated_at_unix_ms
    END;

DELETE FROM task_pending_approvals
WHERE source_task_id IN (
    SELECT id
    FROM task_records
    WHERE canceled_at_unix_ms IS NOT NULL
);

DELETE FROM task_current_nodes
WHERE task_id IN (
    SELECT id
    FROM task_records
    WHERE canceled_at_unix_ms IS NOT NULL
);

DELETE FROM task_active_fanouts
WHERE task_id IN (
    SELECT id
    FROM task_records
    WHERE canceled_at_unix_ms IS NOT NULL
);

INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
)
SELECT
    task.id,
    node.id,
    NULL,
    '{}',
    '{}',
    NULL,
    NULL,
    NULL,
    NULL,
    NULL
FROM task_records task
JOIN workflow_nodes node
    ON node.workflow_id = task.workflow_id
   AND node.node_key = 'done'
   AND node.kind = 'terminal'
WHERE task.canceled_at_unix_ms IS NOT NULL;

INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
)
SELECT
    candidate.task_id,
    candidate.node_id,
    NULL,
    '{}',
    '{}',
    NULL,
    NULL,
    NULL,
    NULL,
    NULL
FROM (
    SELECT
        placement.task_id,
        placement.node_id
    FROM task_node_placements placement
    JOIN task_records task ON task.id = placement.task_id
    JOIN workflow_nodes node ON node.id = placement.node_id
    WHERE task.canceled_at_unix_ms IS NOT NULL
      AND placement.state = 'active'
      AND node.kind = 'terminal'
      AND node.workflow_id = task.workflow_id
      AND node.workflow_id = task.workflow_id
      AND NOT EXISTS (
          SELECT 1
          FROM workflow_nodes canonical_done
          WHERE canonical_done.workflow_id = task.workflow_id
            AND canonical_done.node_key = 'done'
            AND canonical_done.kind = 'terminal'
      )
    GROUP BY placement.task_id
    HAVING COUNT(*) = 1
) candidate;

INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
)
SELECT
    task.id,
    terminal.id,
    NULL,
    '{}',
    '{}',
    NULL,
    NULL,
    NULL,
    NULL,
    NULL
FROM task_records task
JOIN workflow_nodes terminal ON terminal.id = (
    SELECT candidate.id
    FROM workflow_nodes candidate
    WHERE candidate.workflow_id = task.workflow_id
      AND candidate.kind = 'terminal'
    ORDER BY candidate.id
    LIMIT 1
)
WHERE task.canceled_at_unix_ms IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      WHERE current_node.task_id = task.id
  );
