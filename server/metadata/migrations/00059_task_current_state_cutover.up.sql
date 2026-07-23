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
    prior_node_values_json TEXT NOT NULL DEFAULT '{}'
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
