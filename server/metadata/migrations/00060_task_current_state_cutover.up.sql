-- +goose Up

CREATE TABLE task_active_fanouts (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE task_active_fanout_branches (
    task_id TEXT NOT NULL REFERENCES task_active_fanouts(task_id) ON DELETE CASCADE,
    transition_branch_key TEXT NOT NULL CHECK (length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    arrival_state TEXT NOT NULL CHECK (arrival_state IN ('pending', 'arrived')),
    arrival_values_json TEXT
        CHECK (arrival_values_json IS NULL OR (json_valid(arrival_values_json) AND json_type(arrival_values_json) = 'object')),
    PRIMARY KEY (task_id, transition_branch_key),
    CHECK (
        (arrival_state = 'pending' AND arrival_values_json IS NULL)
        OR
        (arrival_state = 'arrived' AND arrival_values_json IS NOT NULL)
    )
);

CREATE TABLE task_current_nodes (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE RESTRICT,
    transition_branch_key TEXT
        CHECK (transition_branch_key IS NULL OR length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    current_input_values_json TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(current_input_values_json) AND json_type(current_input_values_json) = 'object'),
    prior_node_values_json TEXT NOT NULL DEFAULT '{"node_outputs":{},"transition_parameters":{}}'
        CHECK (json_valid(prior_node_values_json) AND json_type(prior_node_values_json) = 'object'),
    session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    scheduling_state TEXT
        CHECK (scheduling_state IS NULL OR scheduling_state IN ('ready', 'admitted', 'interrupted', 'failed')),
    interruption_reason TEXT
        CHECK (interruption_reason IS NULL OR length(trim(interruption_reason)) > 0),
    interruption_detail_json TEXT
        CHECK (interruption_detail_json IS NULL OR (json_valid(interruption_detail_json) AND json_type(interruption_detail_json) = 'object')),
    interrupted_at_unix_ms INTEGER
        CHECK (interrupted_at_unix_ms IS NULL OR interrupted_at_unix_ms > 0),
    FOREIGN KEY (task_id, transition_branch_key)
        REFERENCES task_active_fanout_branches(task_id, transition_branch_key)
        ON DELETE RESTRICT,
    CHECK (
        (scheduling_state = 'interrupted'
            AND interruption_reason IS NOT NULL
            AND interruption_detail_json IS NOT NULL
            AND interrupted_at_unix_ms IS NOT NULL)
        OR
        (scheduling_state IS NULL OR scheduling_state != 'interrupted')
            AND interruption_reason IS NULL
            AND interruption_detail_json IS NULL
            AND interrupted_at_unix_ms IS NULL
    )
);

CREATE UNIQUE INDEX task_current_nodes_serial_task_unique_idx
    ON task_current_nodes(task_id)
    WHERE transition_branch_key IS NULL;

CREATE UNIQUE INDEX task_current_nodes_parallel_branch_unique_idx
    ON task_current_nodes(task_id, transition_branch_key)
    WHERE transition_branch_key IS NOT NULL;

CREATE TABLE task_pending_approvals (
    id TEXT PRIMARY KEY,
    source_task_id TEXT NOT NULL,
    source_node_id TEXT NOT NULL,
    source_transition_branch_key TEXT
        CHECK (source_transition_branch_key IS NULL OR length(trim(source_transition_branch_key)) BETWEEN 1 AND 64),
    source_session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    workflow_version INTEGER NOT NULL CHECK (workflow_version >= 1),
    transition_snapshot_json TEXT NOT NULL
        CHECK (json_valid(transition_snapshot_json) AND json_type(transition_snapshot_json) = 'object'),
    materialized_values_json TEXT NOT NULL
        CHECK (json_valid(materialized_values_json) AND json_type(materialized_values_json) = 'object'),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms > 0)
);

CREATE TABLE task_pending_approval_branches (
    approval_id TEXT NOT NULL REFERENCES task_pending_approvals(id) ON DELETE CASCADE,
    transition_branch_key TEXT NOT NULL CHECK (length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    target_snapshot_json TEXT NOT NULL
        CHECK (json_valid(target_snapshot_json) AND json_type(target_snapshot_json) = 'object'),
    effective_edge_configuration_json TEXT NOT NULL
        CHECK (json_valid(effective_edge_configuration_json) AND json_type(effective_edge_configuration_json) = 'object'),
    context_source_resolution_json TEXT NOT NULL
        CHECK (json_valid(context_source_resolution_json) AND json_type(context_source_resolution_json) = 'object'),
    PRIMARY KEY (approval_id, transition_branch_key)
);

CREATE UNIQUE INDEX task_pending_approvals_serial_source_unique_idx
    ON task_pending_approvals(source_task_id, source_node_id)
    WHERE source_transition_branch_key IS NULL;

CREATE UNIQUE INDEX task_pending_approvals_parallel_source_unique_idx
    ON task_pending_approvals(source_task_id, source_node_id, source_transition_branch_key)
    WHERE source_transition_branch_key IS NOT NULL;

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
CREATE TRIGGER task_pending_approvals_source_current_insert
BEFORE INSERT ON task_pending_approvals
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_current_nodes current_node
    WHERE current_node.task_id = NEW.source_task_id
      AND current_node.node_id = NEW.source_node_id
      AND (
          (current_node.transition_branch_key IS NULL AND NEW.source_transition_branch_key IS NULL)
          OR current_node.transition_branch_key = NEW.source_transition_branch_key
      )
)
BEGIN
    SELECT RAISE(ABORT, 'pending approval source must be a current node');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_pending_approvals_source_current_update
BEFORE UPDATE OF source_task_id, source_node_id, source_transition_branch_key ON task_pending_approvals
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_current_nodes current_node
    WHERE current_node.task_id = NEW.source_task_id
      AND current_node.node_id = NEW.source_node_id
      AND (
          (current_node.transition_branch_key IS NULL AND NEW.source_transition_branch_key IS NULL)
          OR current_node.transition_branch_key = NEW.source_transition_branch_key
      )
)
BEGIN
    SELECT RAISE(ABORT, 'pending approval source must be a current node');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_pending_approval_delete
BEFORE DELETE ON task_current_nodes
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_pending_approvals approval
    WHERE approval.source_task_id = OLD.task_id
      AND approval.source_node_id = OLD.node_id
      AND (
          (approval.source_transition_branch_key IS NULL AND OLD.transition_branch_key IS NULL)
          OR approval.source_transition_branch_key = OLD.transition_branch_key
      )
)
BEGIN
    SELECT RAISE(ABORT, 'current node with pending approval cannot be deleted');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_pending_approval_reference_update
BEFORE UPDATE OF task_id, node_id, transition_branch_key ON task_current_nodes
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_pending_approvals approval
    WHERE approval.source_task_id = OLD.task_id
      AND approval.source_node_id = OLD.node_id
      AND (
          (approval.source_transition_branch_key IS NULL AND OLD.transition_branch_key IS NULL)
          OR approval.source_transition_branch_key = OLD.transition_branch_key
      )
)
BEGIN
    SELECT RAISE(ABORT, 'current node with pending approval cannot change identity');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_active_fanouts_serial_current_node_insert
BEFORE INSERT ON task_active_fanouts
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_current_nodes current_node
    WHERE current_node.task_id = NEW.task_id
      AND current_node.transition_branch_key IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'active fan-out cannot coexist with serial current node');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_active_fanouts_serial_current_node_update
BEFORE UPDATE OF task_id ON task_active_fanouts
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_current_nodes current_node
    WHERE current_node.task_id = NEW.task_id
      AND current_node.transition_branch_key IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'active fan-out cannot coexist with serial current node');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_serial_active_fanout_insert
BEFORE INSERT ON task_current_nodes
FOR EACH ROW
WHEN NEW.transition_branch_key IS NULL
AND EXISTS (
    SELECT 1
    FROM task_active_fanouts fanout
    WHERE fanout.task_id = NEW.task_id
)
BEGIN
    SELECT RAISE(ABORT, 'serial current node cannot coexist with active fan-out');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_serial_active_fanout_update
BEFORE UPDATE OF task_id, transition_branch_key ON task_current_nodes
FOR EACH ROW
WHEN NEW.transition_branch_key IS NULL
AND EXISTS (
    SELECT 1
    FROM task_active_fanouts fanout
    WHERE fanout.task_id = NEW.task_id
)
BEGIN
    SELECT RAISE(ABORT, 'serial current node cannot coexist with active fan-out');
END;
-- +goose StatementEnd

ALTER TABLE sessions
ADD COLUMN task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL;

CREATE INDEX sessions_task_id_idx
    ON sessions(task_id)
    WHERE task_id IS NOT NULL;

CREATE TABLE session_workflow_node_associations (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE RESTRICT,
    transition_branch_key TEXT
        CHECK (transition_branch_key IS NULL OR length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    associated_at_unix_ms INTEGER NOT NULL CHECK (associated_at_unix_ms > 0)
);

CREATE UNIQUE INDEX session_workflow_node_associations_serial_unique_idx
    ON session_workflow_node_associations(session_id, node_id)
    WHERE transition_branch_key IS NULL;

CREATE UNIQUE INDEX session_workflow_node_associations_branch_unique_idx
    ON session_workflow_node_associations(session_id, node_id, transition_branch_key)
    WHERE transition_branch_key IS NOT NULL;

CREATE INDEX session_workflow_node_associations_lookup_idx
    ON session_workflow_node_associations(node_id, transition_branch_key, associated_at_unix_ms DESC, session_id DESC);

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

CREATE TEMP TABLE migration_current_node_value_source_errors (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    transition_branch_key TEXT,
    placement_id TEXT NOT NULL,
    entering_edge_count INTEGER NOT NULL
);

-- +goose StatementBegin
CREATE TEMP TRIGGER migration_current_node_value_source_errors_abort
BEFORE INSERT ON migration_current_node_value_source_errors
FOR EACH ROW
BEGIN
    SELECT RAISE(
        ABORT,
        'current node value migration failure: task_id=' || NEW.task_id ||
        ', node_id=' || NEW.node_id ||
        ', transition_branch_key=' || COALESCE(NEW.transition_branch_key, 'serial') ||
        ', placement_id=' || NEW.placement_id ||
        ', entering_edge_count=' || NEW.entering_edge_count
    );
END;
-- +goose StatementEnd

INSERT INTO migration_current_node_value_source_errors (
    task_id,
    node_id,
    transition_branch_key,
    placement_id,
    entering_edge_count
)
SELECT
    placement.task_id,
    placement.node_id,
    branch_edge.edge_key,
    placement.id,
    COUNT(current_edge.id)
FROM task_node_placements placement
JOIN task_records task ON task.id = placement.task_id
JOIN workflow_nodes node ON node.id = placement.node_id
LEFT JOIN workflow_edges branch_edge ON branch_edge.id = placement.parallel_branch_edge_id
LEFT JOIN task_transition_edges entering_edge
    ON entering_edge.target_placement_id = placement.id
   AND entering_edge.state = 'applied'
LEFT JOIN workflow_edges current_edge ON current_edge.id = entering_edge.workflow_edge_id
WHERE task.canceled_at_unix_ms IS NULL
  AND node.kind IN ('agent', 'script')
  AND (
      placement.state = 'active'
      OR EXISTS (
          SELECT 1
          FROM task_transitions pending
          WHERE pending.source_placement_id = placement.id
            AND pending.state = 'pending_approval'
      )
  )
GROUP BY placement.id
HAVING COUNT(current_edge.id) != 1;

CREATE TEMP TABLE migration_workflow_graph_edges (
    workflow_id TEXT NOT NULL,
    edge_id TEXT NOT NULL,
    transition_key TEXT NOT NULL,
    source_node_id TEXT NOT NULL,
    source_node_key TEXT NOT NULL,
    source_node_kind TEXT NOT NULL,
    target_node_id TEXT NOT NULL,
    target_node_key TEXT NOT NULL,
    target_node_kind TEXT NOT NULL,
    prompt_template TEXT NOT NULL,
    parameters_json TEXT NOT NULL
);

INSERT INTO migration_workflow_graph_edges (
    workflow_id,
    edge_id,
    transition_key,
    source_node_id,
    source_node_key,
    source_node_kind,
    target_node_id,
    target_node_key,
    target_node_kind,
    prompt_template,
    parameters_json
)
SELECT
    source_node.workflow_id,
    edge.id,
    transition_group.transition_id,
    transition_group.source_node_id,
    source_node.node_key,
    source_node.kind,
    edge.target_node_id,
    target_node.node_key,
    target_node.kind,
    edge.prompt_template,
    edge.parameters_json
FROM workflow_edges edge
JOIN workflow_transition_groups transition_group
    ON transition_group.id = edge.transition_group_id
JOIN workflow_nodes source_node
    ON source_node.id = transition_group.source_node_id
JOIN workflow_nodes target_node
    ON target_node.id = edge.target_node_id;

CREATE INDEX migration_workflow_graph_edges_workflow_idx
    ON migration_workflow_graph_edges(workflow_id, edge_id);

CREATE TEMP TABLE migration_prior_value_candidates (
    task_id TEXT NOT NULL,
    parallel_batch_transition_id TEXT,
    transition_branch_key TEXT,
    node_key TEXT NOT NULL,
    transition_key TEXT NOT NULL,
    output_values_json TEXT NOT NULL,
    applied_at_unix_ms INTEGER NOT NULL,
    created_at_unix_ms INTEGER NOT NULL,
    transition_record_id TEXT NOT NULL
);

INSERT INTO migration_prior_value_candidates (
    task_id,
    parallel_batch_transition_id,
    transition_branch_key,
    node_key,
    transition_key,
    output_values_json,
    applied_at_unix_ms,
    created_at_unix_ms,
    transition_record_id
)
SELECT
    transition.task_id,
    source_placement.parallel_batch_transition_id,
    source_branch.edge_key,
    transition.source_node_key,
    transition.transition_id,
    transition.output_values_json,
    transition.applied_at_unix_ms,
    transition.created_at_unix_ms,
    transition.id
FROM task_transitions transition
LEFT JOIN task_node_placements source_placement
    ON source_placement.id = transition.source_placement_id
LEFT JOIN workflow_edges source_branch
    ON source_branch.id = source_placement.parallel_branch_edge_id
WHERE transition.state IN ('approved', 'applied')
  AND transition.applied_at_unix_ms IS NOT NULL;

CREATE INDEX migration_prior_value_candidates_task_time_idx
    ON migration_prior_value_candidates(task_id, applied_at_unix_ms, created_at_unix_ms);

CREATE TEMP TABLE migration_current_node_value_environments (
    placement_id TEXT PRIMARY KEY,
    current_input_values_json TEXT NOT NULL,
    prior_node_values_json TEXT NOT NULL,
    entered_by_edge_id TEXT NOT NULL
);

INSERT INTO migration_current_node_value_environments (
    placement_id,
    current_input_values_json,
    prior_node_values_json,
    entered_by_edge_id
)
SELECT
    placement.id,
    kent_migration_current_input_values_v1(
        placement.task_id,
        placement.node_id,
        branch_edge.edge_key,
        entering_edge.input_bindings_json,
        entering_transition.output_values_json,
        entering_transition.commentary,
        task.short_id,
        task.title,
        task.body,
        task.source_url
    ),
    kent_migration_prior_node_values_v1(
        placement.task_id,
        placement.node_id,
        branch_edge.edge_key,
        placement.node_id,
        (
            SELECT COALESCE(json_group_array(json_object(
                'edge_id', graph.edge_id,
                'snapshot_priority', graph.snapshot_priority,
                'transition_key', graph.transition_key,
                'source_node_id', graph.source_node_id,
                'source_node_key', graph.source_node_key,
                'source_node_kind', graph.source_node_kind,
                'target_node_id', graph.target_node_id,
                'target_node_key', graph.target_node_key,
                'target_node_kind', graph.target_node_kind,
                'prompt_template', graph.prompt_template,
                'parameters_json', graph.parameters_json
            )), '[]')
            FROM (
                SELECT
                    edge_id,
                    transition_key,
                    source_node_id,
                    source_node_key,
                    source_node_kind,
                    target_node_id,
                    target_node_key,
                    target_node_kind,
                    prompt_template,
                    parameters_json,
                    0 AS snapshot_priority
                FROM migration_workflow_graph_edges
                WHERE workflow_id = task.workflow_id
                ORDER BY edge_id
            ) graph
        ),
        COALESCE(json_extract(value_run.metadata_json, '$.node_output_values'), '{}'),
        COALESCE(json_extract(value_run.metadata_json, '$.prior_parameter_values'), '{}'),
        (
            SELECT COALESCE(json_group_array(json_object(
                'scope', candidate.scope,
                'node_key', candidate.node_key,
                'transition_key', candidate.transition_key,
                'output_values_json', candidate.output_values_json,
                'applied_at_unix_ms', candidate.applied_at_unix_ms,
                'created_at_unix_ms', candidate.created_at_unix_ms,
                'transition_record_id', candidate.transition_record_id
            )), '[]')
            FROM (
                SELECT
                    CASE
                        WHEN placement.parallel_batch_transition_id IS NOT NULL
                         AND prior_transition.parallel_batch_transition_id = placement.parallel_batch_transition_id
                         AND prior_transition.transition_branch_key = branch_edge.edge_key
                        THEN 'branch'
                        ELSE 'task'
                    END AS scope,
                    prior_transition.node_key,
                    prior_transition.transition_key,
                    prior_transition.output_values_json,
                    prior_transition.applied_at_unix_ms,
                    prior_transition.created_at_unix_ms,
                    prior_transition.transition_record_id
                FROM migration_prior_value_candidates prior_transition
                WHERE prior_transition.task_id = placement.task_id
                  AND prior_transition.applied_at_unix_ms <= COALESCE(
                      value_run.created_at_unix_ms,
                      placement.created_at_unix_ms
                  )
            ) candidate
        )
    ),
    entering_edge.workflow_edge_id
FROM task_node_placements placement
JOIN task_records task ON task.id = placement.task_id
JOIN workflow_nodes node ON node.id = placement.node_id
LEFT JOIN workflow_edges branch_edge ON branch_edge.id = placement.parallel_branch_edge_id
LEFT JOIN task_transitions pending_transition
    ON pending_transition.id = (
        SELECT pending.id
        FROM task_transitions pending
        WHERE pending.source_placement_id = placement.id
          AND pending.state = 'pending_approval'
        ORDER BY pending.created_at_unix_ms DESC, pending.rowid DESC
        LIMIT 1
    )
LEFT JOIN task_runs unfinished_run
    ON unfinished_run.id = (
        SELECT unfinished.id
        FROM task_runs unfinished
        WHERE unfinished.placement_id = placement.id
          AND unfinished.completed_at_unix_ms IS NULL
        ORDER BY unfinished.created_at_unix_ms DESC, unfinished.rowid DESC
        LIMIT 1
    )
LEFT JOIN task_runs value_run
    ON value_run.id = COALESCE(pending_transition.source_run_id, unfinished_run.id)
JOIN task_transition_edges entering_edge
    ON entering_edge.target_placement_id = placement.id
   AND entering_edge.state = 'applied'
JOIN task_transitions entering_transition
    ON entering_transition.id = entering_edge.task_transition_id
WHERE task.canceled_at_unix_ms IS NULL
  AND node.kind IN ('agent', 'script')
  AND (
      placement.state = 'active'
      OR pending_transition.id IS NOT NULL
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
    value_environment.current_input_values_json,
    value_environment.prior_node_values_json,
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
JOIN migration_current_node_value_environments value_environment
    ON value_environment.placement_id = placement.id
WHERE transition.state = 'pending_approval'
  AND task.canceled_at_unix_ms IS NULL
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
    placement.task_id,
    placement.node_id,
    NULL,
    '{}',
    '{"node_outputs":{},"transition_parameters":{}}',
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
  )
  AND NOT EXISTS (
      SELECT 1
      FROM task_node_placements newer_placement
      JOIN workflow_nodes newer_node ON newer_node.id = newer_placement.node_id
      WHERE newer_placement.task_id = placement.task_id
        AND newer_placement.state = 'active'
        AND newer_placement.parallel_batch_transition_id IS NULL
        AND newer_node.kind IN ('start', 'terminal')
        AND (
            newer_placement.updated_at_unix_ms > placement.updated_at_unix_ms
            OR (
                newer_placement.updated_at_unix_ms = placement.updated_at_unix_ms
                AND newer_placement.created_at_unix_ms > placement.created_at_unix_ms
            )
            OR (
                newer_placement.updated_at_unix_ms = placement.updated_at_unix_ms
                AND newer_placement.created_at_unix_ms = placement.created_at_unix_ms
                AND newer_placement.id > placement.id
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
    NULL,
    value_environment.current_input_values_json,
    value_environment.prior_node_values_json,
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
JOIN migration_current_node_value_environments value_environment
    ON value_environment.placement_id = placement.id
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
    value_environment.current_input_values_json,
    value_environment.prior_node_values_json,
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
JOIN migration_current_node_value_environments value_environment
    ON value_environment.placement_id = placement.id
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

CREATE TEMP TABLE migration_parallel_projection_errors (
    task_id TEXT NOT NULL,
    node_id TEXT,
    transition_branch_key TEXT,
    error_kind TEXT NOT NULL,
    error_detail TEXT NOT NULL
);

-- +goose StatementBegin
CREATE TEMP TRIGGER migration_parallel_projection_errors_abort
BEFORE INSERT ON migration_parallel_projection_errors
FOR EACH ROW
BEGIN
    SELECT RAISE(
        ABORT,
        'parallel current node migration failure: task_id=' || NEW.task_id ||
        ', node_id=' || COALESCE(NEW.node_id, 'none') ||
        ', transition_branch_key=' || COALESCE(NEW.transition_branch_key, 'none') ||
        ', error_kind=' || NEW.error_kind ||
        ', detail=' || NEW.error_detail
    );
END;
-- +goose StatementEnd

INSERT INTO migration_parallel_projection_errors (
    task_id,
    error_kind,
    error_detail
)
SELECT
    placement.task_id,
    'active_fanout_ambiguity',
    'active_batch_count=' || COUNT(DISTINCT placement.parallel_batch_transition_id)
FROM task_node_placements placement
JOIN task_records task ON task.id = placement.task_id
WHERE task.canceled_at_unix_ms IS NULL
  AND placement.parallel_batch_transition_id IS NOT NULL
  AND placement.state IN ('active', 'waiting_approval')
GROUP BY placement.task_id
HAVING COUNT(DISTINCT placement.parallel_batch_transition_id) != 1;

INSERT INTO migration_parallel_projection_errors (
    task_id,
    node_id,
    transition_branch_key,
    error_kind,
    error_detail
)
SELECT
    placement.task_id,
    placement.node_id,
    branch.edge_key,
    'unresolved_branch',
    'placement_id=' || placement.id
FROM task_node_placements placement
JOIN task_records task ON task.id = placement.task_id
LEFT JOIN workflow_edges branch ON branch.id = placement.parallel_branch_edge_id
WHERE task.canceled_at_unix_ms IS NULL
  AND placement.parallel_batch_transition_id IS NOT NULL
  AND placement.state IN ('active', 'waiting_approval', 'completed')
  AND EXISTS (
      SELECT 1
      FROM task_node_placements active_branch
      WHERE active_branch.task_id = placement.task_id
        AND active_branch.parallel_batch_transition_id = placement.parallel_batch_transition_id
        AND active_branch.state IN ('active', 'waiting_approval')
  )
  AND (
      branch.id IS NULL
      OR length(trim(branch.edge_key)) = 0
  );

INSERT INTO migration_parallel_projection_errors (
    task_id,
    node_id,
    transition_branch_key,
    error_kind,
    error_detail
)
SELECT
    placement.task_id,
    MIN(placement.node_id),
    branch.edge_key,
    'duplicate_branch',
    'placement_count=' || COUNT(*)
FROM task_node_placements placement
JOIN task_records task ON task.id = placement.task_id
JOIN workflow_edges branch ON branch.id = placement.parallel_branch_edge_id
WHERE task.canceled_at_unix_ms IS NULL
  AND placement.parallel_batch_transition_id IS NOT NULL
  AND placement.state IN ('active', 'waiting_approval', 'completed')
  AND EXISTS (
      SELECT 1
      FROM task_node_placements active_branch
      WHERE active_branch.task_id = placement.task_id
        AND active_branch.parallel_batch_transition_id = placement.parallel_batch_transition_id
        AND active_branch.state IN ('active', 'waiting_approval')
  )
GROUP BY placement.task_id, placement.parallel_batch_transition_id, branch.edge_key
HAVING COUNT(*) > 1;

INSERT INTO migration_parallel_projection_errors (
    task_id,
    node_id,
    transition_branch_key,
    error_kind,
    error_detail
)
SELECT
    placement.task_id,
    placement.node_id,
    branch.edge_key,
    'join_arrival_ambiguity',
    'placement_id=' || placement.id ||
    ', arrival_count=' || COUNT(arrival_edge.id) ||
    ', valid_value_count=' || COALESCE(SUM(
        CASE
            WHEN json_valid(arrival.output_values_json)
             AND json_type(arrival.output_values_json) = 'object'
            THEN 1
            ELSE 0
        END
    ), 0)
FROM task_node_placements placement
JOIN task_records task ON task.id = placement.task_id
JOIN workflow_edges branch ON branch.id = placement.parallel_branch_edge_id
LEFT JOIN task_transitions arrival
    ON arrival.source_placement_id = placement.id
   AND arrival.state = 'applied'
LEFT JOIN task_transition_edges arrival_edge
    ON arrival_edge.task_transition_id = arrival.id
   AND arrival_edge.state = 'applied'
   AND arrival_edge.target_node_kind = 'join'
WHERE task.canceled_at_unix_ms IS NULL
  AND placement.parallel_batch_transition_id IS NOT NULL
  AND placement.state = 'completed'
  AND EXISTS (
      SELECT 1
      FROM task_node_placements active_branch
      WHERE active_branch.task_id = placement.task_id
        AND active_branch.parallel_batch_transition_id = placement.parallel_batch_transition_id
        AND active_branch.state IN ('active', 'waiting_approval')
  )
GROUP BY placement.id
HAVING COUNT(arrival_edge.id) != 1
    OR COALESCE(SUM(
        CASE
            WHEN json_valid(arrival.output_values_json)
             AND json_type(arrival.output_values_json) = 'object'
            THEN 1
            ELSE 0
        END
    ), 0) != 1;

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
    value_environment.current_input_values_json,
    value_environment.prior_node_values_json,
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
JOIN migration_current_node_value_environments value_environment
    ON value_environment.placement_id = placement.id
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
    value_environment.current_input_values_json,
    value_environment.prior_node_values_json,
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
JOIN migration_current_node_value_environments value_environment
    ON value_environment.placement_id = placement.id
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
    source_branch.edge_key,
    value_environment.current_input_values_json,
    value_environment.prior_node_values_json,
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
JOIN migration_current_node_value_environments value_environment
    ON value_environment.placement_id = placement.id
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

CREATE TEMP TABLE migration_pending_approval_target_value_environments (
    transition_edge_id TEXT PRIMARY KEY,
    target_transition_branch_key TEXT,
    current_input_values_json TEXT NOT NULL,
    prior_node_values_json TEXT NOT NULL,
    entered_by_edge_id TEXT NOT NULL
);

INSERT INTO migration_pending_approval_target_value_environments (
    transition_edge_id,
    target_transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    entered_by_edge_id
)
SELECT
    edge.id,
    CASE
        WHEN source_branch.edge_key IS NOT NULL THEN source_branch.edge_key
        WHEN (
            SELECT COUNT(*)
            FROM task_transition_edges sibling
            WHERE sibling.task_transition_id = transition.id
              AND sibling.state = 'pending'
        ) > 1 THEN edge.edge_key
        ELSE NULL
    END,
    kent_migration_current_input_values_v1(
        transition.task_id,
        edge.target_node_id,
        CASE
            WHEN source_branch.edge_key IS NOT NULL THEN source_branch.edge_key
            WHEN (
                SELECT COUNT(*)
                FROM task_transition_edges sibling
                WHERE sibling.task_transition_id = transition.id
                  AND sibling.state = 'pending'
            ) > 1 THEN edge.edge_key
            ELSE NULL
        END,
        edge.input_bindings_json,
        transition.output_values_json,
        transition.commentary,
        task.short_id,
        task.title,
        task.body,
        task.source_url
    ),
    kent_migration_prior_node_values_v1(
        transition.task_id,
        edge.target_node_id,
        CASE
            WHEN source_branch.edge_key IS NOT NULL THEN source_branch.edge_key
            WHEN (
                SELECT COUNT(*)
                FROM task_transition_edges sibling
                WHERE sibling.task_transition_id = transition.id
                  AND sibling.state = 'pending'
            ) > 1 THEN edge.edge_key
            ELSE NULL
        END,
        edge.target_node_id,
        (
            SELECT COALESCE(json_group_array(json_object(
                'edge_id', graph.edge_id,
                'snapshot_priority', graph.snapshot_priority,
                'transition_key', graph.transition_key,
                'source_node_id', graph.source_node_id,
                'source_node_key', graph.source_node_key,
                'source_node_kind', graph.source_node_kind,
                'target_node_id', graph.target_node_id,
                'target_node_key', graph.target_node_key,
                'target_node_kind', graph.target_node_kind,
                'prompt_template', graph.prompt_template,
                'parameters_json', graph.parameters_json
            )), '[]')
            FROM (
                SELECT
                    edge_id,
                    transition_key,
                    source_node_id,
                    source_node_key,
                    source_node_kind,
                    target_node_id,
                    target_node_key,
                    target_node_kind,
                    prompt_template,
                    parameters_json,
                    0 AS snapshot_priority
                FROM migration_workflow_graph_edges
                WHERE workflow_id = task.workflow_id
                UNION ALL
                SELECT
                    COALESCE(NULLIF(edge.workflow_edge_id, ''), edge.id),
                    transition.transition_id,
                    placement.node_id,
                    transition.source_node_key,
                    source_node.kind,
                    edge.target_node_id,
                    edge.target_node_key,
                    edge.target_node_kind,
                    COALESCE(json_extract(edge.metadata_json, '$.prompt_template'), ''),
                    COALESCE(json_extract(edge.metadata_json, '$.parameters'), '[]'),
                    1
                ORDER BY
                    source_node_id,
                    transition_key,
                    target_node_id,
                    snapshot_priority,
                    edge_id
            ) graph
        ),
        COALESCE(json_extract(edge.metadata_json, '$.node_output_values'), '{}'),
        COALESCE(json_extract(edge.metadata_json, '$.prior_parameter_values'), '{}'),
        (
            SELECT COALESCE(json_group_array(json_object(
                'scope', candidate.scope,
                'node_key', candidate.node_key,
                'transition_key', candidate.transition_key,
                'output_values_json', candidate.output_values_json,
                'applied_at_unix_ms', candidate.applied_at_unix_ms,
                'created_at_unix_ms', candidate.created_at_unix_ms,
                'transition_record_id', candidate.transition_record_id
            )), '[]')
            FROM (
                SELECT
                    CASE
                        WHEN placement.parallel_batch_transition_id IS NOT NULL
                         AND prior_transition.parallel_batch_transition_id = placement.parallel_batch_transition_id
                         AND prior_transition.transition_branch_key = source_branch.edge_key
                        THEN 'branch'
                        ELSE 'task'
                    END AS scope,
                    prior_transition.node_key,
                    prior_transition.transition_key,
                    prior_transition.output_values_json,
                    prior_transition.applied_at_unix_ms,
                    prior_transition.created_at_unix_ms,
                    prior_transition.transition_record_id
                FROM migration_prior_value_candidates prior_transition
                WHERE prior_transition.task_id = transition.task_id
                  AND prior_transition.applied_at_unix_ms <= transition.created_at_unix_ms
                UNION ALL
                SELECT
                    CASE
                        WHEN source_branch.edge_key IS NOT NULL THEN 'branch'
                        ELSE 'task'
                    END,
                    transition.source_node_key,
                    transition.transition_id,
                    transition.output_values_json,
                    transition.created_at_unix_ms,
                    transition.created_at_unix_ms,
                    transition.id
            ) candidate
        )
    ),
    COALESCE(NULLIF(edge.workflow_edge_id, ''), edge.id)
FROM migration_pending_approval_ids migration
JOIN task_transitions transition ON transition.id = migration.transition_id
JOIN task_node_placements placement ON placement.id = transition.source_placement_id
JOIN task_records task ON task.id = transition.task_id
JOIN workflow_nodes source_node ON source_node.id = placement.node_id
LEFT JOIN workflow_edges source_branch ON source_branch.id = placement.parallel_branch_edge_id
JOIN task_transition_edges edge ON edge.task_transition_id = transition.id
WHERE edge.state = 'pending';

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
        'transition_branch_key', value_environment.target_transition_branch_key,
        'entered_by_edge_id', value_environment.entered_by_edge_id,
        'display_name', edge.target_node_display_name,
        'current_input_values', json(value_environment.current_input_values_json),
        'prior_values', json(value_environment.prior_node_values_json),
        'session_id', CASE
            WHEN edge.context_mode = 'new_session' THEN NULL
            ELSE json_extract(edge.metadata_json, '$.source_session_id')
        END,
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
        'session_id', CASE
            WHEN edge.context_mode = 'new_session' THEN NULL
            ELSE json_extract(edge.metadata_json, '$.source_session_id')
        END
    )
FROM migration_pending_approval_ids migration
JOIN task_transitions transition ON transition.id = migration.transition_id
JOIN task_node_placements placement ON placement.id = transition.source_placement_id
JOIN task_records task ON task.id = transition.task_id
LEFT JOIN workflow_edges source_branch ON source_branch.id = placement.parallel_branch_edge_id
JOIN task_transition_edges edge ON edge.task_transition_id = transition.id
JOIN migration_pending_approval_target_value_environments value_environment
    ON value_environment.transition_edge_id = edge.id
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

CREATE TEMP TABLE migration_session_metadata_errors (
    session_id TEXT NOT NULL
);

-- +goose StatementBegin
CREATE TEMP TRIGGER migration_session_metadata_errors_abort
BEFORE INSERT ON migration_session_metadata_errors
FOR EACH ROW
BEGIN
    SELECT RAISE(
        ABORT,
        'workflow session metadata migration failure: session_id=' || NEW.session_id ||
        ', metadata_json is malformed'
    );
END;
-- +goose StatementEnd

INSERT INTO migration_session_metadata_errors (session_id)
SELECT id
FROM sessions
WHERE NOT json_valid(metadata_json);

UPDATE sessions
SET metadata_json = json_remove(metadata_json, '$.workflow_session');

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
    '{"node_outputs":{},"transition_parameters":{}}',
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
    '{"node_outputs":{},"transition_parameters":{}}',
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
    '{"node_outputs":{},"transition_parameters":{}}',
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

ALTER TABLE task_current_nodes
ADD COLUMN entered_by_edge_id TEXT;

UPDATE task_current_nodes
SET entered_by_edge_id = (
    SELECT transition_edge.workflow_edge_id
    FROM task_node_placements placement
    JOIN task_transition_edges transition_edge
      ON transition_edge.target_placement_id = placement.id
    WHERE placement.task_id = task_current_nodes.task_id
      AND placement.node_id = task_current_nodes.node_id
      AND (
          (placement.parallel_branch_edge_id IS NULL AND task_current_nodes.transition_branch_key IS NULL)
          OR (
              placement.parallel_branch_edge_id IS NOT NULL
              AND EXISTS (
                  SELECT 1
                  FROM workflow_edges branch
                  WHERE branch.id = placement.parallel_branch_edge_id
                    AND branch.edge_key = task_current_nodes.transition_branch_key
              )
          )
      )
    ORDER BY transition_edge.rowid DESC
    LIMIT 1
)
WHERE entered_by_edge_id IS NULL;

CREATE TABLE migration_current_node_entering_edge_errors (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER migration_current_node_entering_edge_fail
BEFORE INSERT ON migration_current_node_entering_edge_errors
BEGIN
    SELECT RAISE(ABORT, 'current node entering edge migration failure: executable current node has no resolvable entering transition edge');
END;
-- +goose StatementEnd

INSERT INTO migration_current_node_entering_edge_errors (task_id, node_id)
SELECT current_node.task_id, current_node.node_id
FROM task_current_nodes current_node
JOIN workflow_nodes node ON node.id = current_node.node_id
WHERE node.kind IN ('agent', 'script')
  AND current_node.entered_by_edge_id IS NULL;

DROP TRIGGER migration_current_node_entering_edge_fail;
DROP TABLE migration_current_node_entering_edge_errors;

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
