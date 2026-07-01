-- +goose Up
-- +goose NO TRANSACTION

PRAGMA legacy_alter_table = ON;
PRAGMA foreign_keys = OFF;

BEGIN IMMEDIATE;

DROP TRIGGER IF EXISTS task_node_placements_runtime_insert;
DROP TRIGGER IF EXISTS task_node_placements_runtime_update;
DROP TRIGGER IF EXISTS workflow_nodes_current_task_anchor_delete;
DROP TRIGGER IF EXISTS workflow_nodes_current_task_anchor_kind_update;
DROP TRIGGER IF EXISTS workflow_nodes_task_reference_kind_update;
DROP TRIGGER IF EXISTS workflow_nodes_terminal_state_kind_update;
DROP TRIGGER IF EXISTS task_node_placements_terminal_state_insert;
DROP TRIGGER IF EXISTS task_node_placements_terminal_state_update;

DROP VIEW IF EXISTS task_run_records;
DROP VIEW IF EXISTS task_transition_records;
DROP VIEW IF EXISTS task_node_placement_records;

CREATE TEMP TABLE migration_task_node_placement_order (
    id TEXT PRIMARY KEY,
    ordinal INTEGER NOT NULL
);

INSERT INTO migration_task_node_placement_order(id, ordinal)
SELECT id, rowid
FROM task_node_placements;

CREATE TABLE task_node_placements_new (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    node_id TEXT REFERENCES workflow_nodes(id) ON DELETE SET NULL,
    state TEXT NOT NULL CHECK (state IN ('active', 'waiting_approval', 'completed', 'superseded')),
    parallel_batch_transition_id TEXT,
    parallel_branch_edge_id TEXT REFERENCES workflow_edges(id) ON DELETE SET NULL,
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    CHECK (node_id IS NOT NULL OR state IN ('completed', 'superseded')),
    FOREIGN KEY (parallel_batch_transition_id) REFERENCES task_transitions(id) ON DELETE SET NULL
);

INSERT INTO task_node_placements_new (
    id,
    task_id,
    node_id,
    state,
    parallel_batch_transition_id,
    parallel_branch_edge_id,
    created_at_unix_ms,
    updated_at_unix_ms
)
SELECT
    id,
    task_id,
    node_id,
    state,
    parallel_batch_transition_id,
    parallel_branch_edge_id,
    created_at_unix_ms,
    updated_at_unix_ms
FROM task_node_placements;

DROP TABLE task_node_placements;
ALTER TABLE task_node_placements_new RENAME TO task_node_placements;

UPDATE task_node_placements
SET state = 'completed'
WHERE state = 'active'
  AND EXISTS (
      SELECT 1
      FROM workflow_nodes n
      WHERE n.id = task_node_placements.node_id
        AND n.kind = 'terminal'
  );

UPDATE task_node_placements
SET state = 'superseded'
WHERE state = 'completed'
  AND EXISTS (
      SELECT 1
      FROM workflow_nodes n
      WHERE n.id = task_node_placements.node_id
        AND n.kind = 'terminal'
  )
  AND (
      EXISTS (
          SELECT 1
          FROM task_node_placements later
          WHERE later.task_id = task_node_placements.task_id
            AND later.id != task_node_placements.id
            AND (
                later.created_at_unix_ms > task_node_placements.created_at_unix_ms
                OR (
                    later.created_at_unix_ms = task_node_placements.created_at_unix_ms
                    AND (
                        SELECT later_order.ordinal
                        FROM migration_task_node_placement_order later_order
                        WHERE later_order.id = later.id
                    ) > (
                        SELECT placement_order.ordinal
                        FROM migration_task_node_placement_order placement_order
                        WHERE placement_order.id = task_node_placements.id
                    )
                )
            )
      )
      OR EXISTS (
          SELECT 1
          FROM task_transitions transition
          WHERE transition.source_placement_id = task_node_placements.id
            AND transition.state IN ('pending_approval', 'approved', 'applied')
            AND (
                transition.created_at_unix_ms > task_node_placements.created_at_unix_ms
                OR transition.applied_at_unix_ms > task_node_placements.created_at_unix_ms
            )
      )
  );

CREATE INDEX task_node_placements_task_state_idx
    ON task_node_placements(task_id, state);

CREATE INDEX task_node_placements_node_state_idx
    ON task_node_placements(node_id, state);

CREATE INDEX task_node_placements_parallel_batch_idx
    ON task_node_placements(parallel_batch_transition_id, parallel_branch_edge_id, state);

CREATE VIEW task_node_placement_records AS
SELECT
    p.id,
    p.task_id,
    p.node_id AS node_id,
    p.state,
    CAST(COALESCE((
        SELECT te.task_transition_id
        FROM task_transition_edges te
        WHERE te.target_placement_id = p.id
        ORDER BY te.rowid ASC
        LIMIT 1
    ), '') AS TEXT) AS created_by_transition_id,
    p.parallel_batch_transition_id,
    p.parallel_branch_edge_id,
    p.created_at_unix_ms,
    p.updated_at_unix_ms
FROM task_node_placements p;

CREATE VIEW task_run_records AS
SELECT
    r.id,
    p.task_id,
    r.placement_id,
    p.node_id AS node_id,
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
CREATE TRIGGER task_node_placements_runtime_insert
BEFORE INSERT ON task_node_placements
FOR EACH ROW
WHEN (
    (NEW.node_id IS NULL OR trim(NEW.node_id) = '')
    AND NEW.state IN ('active', 'waiting_approval')
)
OR (
    NEW.node_id IS NOT NULL
    AND trim(NEW.node_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_records t
        JOIN workflow_nodes n ON n.id = NEW.node_id
        WHERE t.id = NEW.task_id
          AND n.workflow_id = t.workflow_id
    )
)
OR (
    NEW.parallel_batch_transition_id IS NOT NULL
    AND trim(NEW.parallel_batch_transition_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        WHERE tt.id = NEW.parallel_batch_transition_id
          AND tt.task_id = NEW.task_id
    )
)
OR (
    NEW.parallel_branch_edge_id IS NOT NULL
    AND trim(NEW.parallel_branch_edge_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_records t
        JOIN workflow_edges e ON e.id = NEW.parallel_branch_edge_id
        JOIN workflow_transition_groups tg ON tg.id = e.transition_group_id
        JOIN workflow_nodes source ON source.id = tg.source_node_id
        WHERE t.id = NEW.task_id
          AND source.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task node placement references must stay within one task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_node_placements_runtime_update
BEFORE UPDATE OF task_id, node_id, state, parallel_batch_transition_id, parallel_branch_edge_id ON task_node_placements
FOR EACH ROW
WHEN (
    (NEW.node_id IS NULL OR trim(NEW.node_id) = '')
    AND NEW.state IN ('active', 'waiting_approval')
)
OR (
    NEW.node_id IS NOT NULL
    AND trim(NEW.node_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_records t
        JOIN workflow_nodes n ON n.id = NEW.node_id
        WHERE t.id = NEW.task_id
          AND n.workflow_id = t.workflow_id
    )
)
OR (
    NEW.parallel_batch_transition_id IS NOT NULL
    AND trim(NEW.parallel_batch_transition_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        WHERE tt.id = NEW.parallel_batch_transition_id
          AND tt.task_id = NEW.task_id
    )
)
OR (
    NEW.parallel_branch_edge_id IS NOT NULL
    AND trim(NEW.parallel_branch_edge_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_records t
        JOIN workflow_edges e ON e.id = NEW.parallel_branch_edge_id
        JOIN workflow_transition_groups tg ON tg.id = e.transition_group_id
        JOIN workflow_nodes source ON source.id = tg.source_node_id
        WHERE t.id = NEW.task_id
          AND source.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task node placement references must stay within one task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_node_placements_terminal_state_insert
BEFORE INSERT ON task_node_placements
FOR EACH ROW
WHEN NEW.state IN ('active', 'waiting_approval')
AND EXISTS (
    SELECT 1
    FROM workflow_nodes n
    WHERE n.id = NEW.node_id
      AND n.kind = 'terminal'
)
BEGIN
    SELECT RAISE(ABORT, 'terminal task node placements must be completed');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_node_placements_terminal_state_update
BEFORE UPDATE OF node_id, state ON task_node_placements
FOR EACH ROW
WHEN NEW.state IN ('active', 'waiting_approval')
AND EXISTS (
    SELECT 1
    FROM workflow_nodes n
    WHERE n.id = NEW.node_id
      AND n.kind = 'terminal'
)
BEGIN
    SELECT RAISE(ABORT, 'terminal task node placements must be completed');
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
        FROM task_node_placements p
        WHERE p.node_id = OLD.id
    )
    OR EXISTS (
        SELECT 1
        FROM task_transition_edges te
        WHERE te.target_node_id = OLD.id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node kind changes are blocked for nodes referenced by existing tasks');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER workflow_nodes_current_task_anchor_delete
BEFORE DELETE ON workflow_nodes
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_node_placements p
    JOIN task_records t ON t.id = p.task_id
    WHERE p.node_id = OLD.id
      AND t.canceled_at_unix_ms = 0
      AND (
          (p.state IN ('active', 'waiting_approval') AND OLD.kind != 'terminal')
          OR (p.state = 'completed' AND OLD.kind = 'terminal')
      )
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node has current task references');
END;
-- +goose StatementEnd

CREATE TEMP TABLE migration_placement_fk_check_zero(value INTEGER NOT NULL CHECK (value = 0));

INSERT INTO migration_placement_fk_check_zero(value)
SELECT 1
WHERE EXISTS (
    SELECT 1
    FROM task_node_placements p
    LEFT JOIN tasks t ON t.id = p.task_id
    LEFT JOIN workflow_nodes n ON n.id = p.node_id
    LEFT JOIN workflow_edges e ON e.id = p.parallel_branch_edge_id
    LEFT JOIN task_transitions tt ON tt.id = p.parallel_batch_transition_id
    WHERE t.id IS NULL
       OR (p.node_id IS NOT NULL AND n.id IS NULL)
       OR (p.parallel_branch_edge_id IS NOT NULL AND e.id IS NULL)
       OR (p.parallel_batch_transition_id IS NOT NULL AND tt.id IS NULL)
);

INSERT INTO migration_placement_fk_check_zero(value)
SELECT 1
WHERE EXISTS (
    SELECT 1
    FROM task_runs r
    LEFT JOIN task_node_placements p ON p.id = r.placement_id
    WHERE p.id IS NULL
);

INSERT INTO migration_placement_fk_check_zero(value)
SELECT 1
WHERE EXISTS (
    SELECT 1
    FROM task_transitions tt
    LEFT JOIN task_node_placements p ON p.id = tt.source_placement_id
    WHERE tt.source_placement_id IS NOT NULL
      AND p.id IS NULL
);

INSERT INTO migration_placement_fk_check_zero(value)
SELECT 1
WHERE EXISTS (
    SELECT 1
    FROM task_transition_edges te
    LEFT JOIN task_node_placements p ON p.id = te.target_placement_id
    WHERE te.target_placement_id IS NOT NULL
      AND p.id IS NULL
);

DROP TABLE migration_placement_fk_check_zero;
DROP TABLE migration_task_node_placement_order;

COMMIT;

PRAGMA foreign_keys = ON;
PRAGMA legacy_alter_table = OFF;
