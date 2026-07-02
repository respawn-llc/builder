-- +goose Up
-- +goose NO TRANSACTION

PRAGMA foreign_keys = OFF;

DROP TRIGGER IF EXISTS task_node_placements_runtime_insert;
DROP TRIGGER IF EXISTS task_node_placements_runtime_update;
DROP TRIGGER IF EXISTS task_transitions_runtime_insert;
DROP TRIGGER IF EXISTS task_transitions_runtime_update;
DROP TRIGGER IF EXISTS task_transition_edges_runtime_insert;
DROP TRIGGER IF EXISTS task_transition_edges_runtime_update;

DROP VIEW IF EXISTS task_transition_edge_records;
DROP VIEW IF EXISTS task_transition_records;

CREATE TEMP TABLE migration_transition_source_node_check_zero(value INTEGER NOT NULL CHECK (value = 0));

INSERT INTO migration_transition_source_node_check_zero(value)
SELECT 1
WHERE EXISTS (
    SELECT 1
    FROM task_transitions tt
    LEFT JOIN task_node_placements p ON p.id = tt.source_placement_id
    WHERE tt.source_node_id IS NOT NULL
      AND trim(tt.source_node_id) != ''
      AND (
          tt.source_placement_id IS NULL
          OR trim(tt.source_placement_id) = ''
          OR p.id IS NULL
          OR p.node_id != tt.source_node_id
      )
);

CREATE TABLE task_transitions_new (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    source_run_id TEXT REFERENCES task_runs(id) ON DELETE SET NULL,
    source_placement_id TEXT REFERENCES task_node_placements(id) ON DELETE SET NULL,
    source_node_key TEXT NOT NULL DEFAULT '',
    source_node_display_name TEXT NOT NULL DEFAULT '',
    transition_group_id TEXT REFERENCES workflow_transition_groups(id) ON DELETE SET NULL,
    transition_id TEXT NOT NULL,
    transition_display_name TEXT NOT NULL DEFAULT '',
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    actor TEXT NOT NULL CHECK (actor IN ('agent', 'script', 'user', 'system')),
    state TEXT NOT NULL CHECK (state IN ('pending_approval', 'approved', 'applied', 'rejected', 'invalid')),
    commentary TEXT NOT NULL DEFAULT '' CHECK (length(commentary) <= 65536),
    output_values_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_values_json)),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    applied_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (applied_at_unix_ms >= 0)
);

INSERT INTO task_transitions_new (
    id,
    task_id,
    source_run_id,
    source_placement_id,
    source_node_key,
    source_node_display_name,
    transition_group_id,
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
    transition_group_id,
    transition_id,
    transition_display_name,
    workflow_revision_seen,
    actor,
    state,
    commentary,
    output_values_json,
    created_at_unix_ms,
    applied_at_unix_ms
FROM task_transitions
ORDER BY rowid ASC;

DROP TABLE task_transitions;
ALTER TABLE task_transitions_new RENAME TO task_transitions;

CREATE INDEX task_transitions_task_created_idx
    ON task_transitions(task_id, created_at_unix_ms DESC);

CREATE VIEW task_transition_records AS
SELECT
    tt.id,
    tt.task_id,
    tt.source_run_id,
    tt.source_placement_id,
    p.node_id AS source_node_id,
    tt.source_node_key,
    tt.source_node_display_name,
    tt.transition_group_id,
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
LEFT JOIN task_node_placements p ON p.id = tt.source_placement_id;

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
OR (
    NEW.transition_group_id IS NOT NULL
    AND trim(NEW.transition_group_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_records t
        JOIN workflow_transition_groups tg ON tg.id = NEW.transition_group_id
        JOIN workflow_nodes source_node ON source_node.id = tg.source_node_id
        WHERE t.id = NEW.task_id
          AND source_node.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task transition references must stay within one task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_transitions_runtime_update
BEFORE UPDATE OF task_id, source_run_id, source_placement_id, transition_group_id, transition_id ON task_transitions
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
OR (
    NEW.transition_group_id IS NOT NULL
    AND trim(NEW.transition_group_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_records t
        JOIN workflow_transition_groups tg ON tg.id = NEW.transition_group_id
        JOIN workflow_nodes source_node ON source_node.id = tg.source_node_id
        WHERE t.id = NEW.task_id
          AND source_node.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task transition references must stay within one task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_transition_edges_runtime_insert
BEFORE INSERT ON task_transition_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_transitions tt
    WHERE tt.id = NEW.task_transition_id
)
OR (
    NEW.target_placement_id IS NOT NULL
    AND trim(NEW.target_placement_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_node_placements p ON p.id = NEW.target_placement_id
        WHERE tt.id = NEW.task_transition_id
          AND p.task_id = tt.task_id
          AND (
              NEW.target_node_id IS NULL
              OR trim(NEW.target_node_id) = ''
              OR p.node_id = NEW.target_node_id
          )
    )
)
OR (
    NEW.target_node_id IS NOT NULL
    AND trim(NEW.target_node_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_records t ON t.id = tt.task_id
        JOIN workflow_nodes n ON n.id = NEW.target_node_id
        WHERE tt.id = NEW.task_transition_id
          AND n.workflow_id = t.workflow_id
    )
)
OR (
    NEW.workflow_edge_id IS NOT NULL
    AND trim(NEW.workflow_edge_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_records t ON t.id = tt.task_id
        JOIN workflow_edges e ON e.id = NEW.workflow_edge_id
        WHERE tt.id = NEW.task_transition_id
          AND e.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task transition edge references must stay within one task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_transition_edges_runtime_update
BEFORE UPDATE OF task_transition_id, workflow_edge_id, target_node_id, target_placement_id ON task_transition_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_transitions tt
    WHERE tt.id = NEW.task_transition_id
)
OR (
    NEW.target_placement_id IS NOT NULL
    AND trim(NEW.target_placement_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_node_placements p ON p.id = NEW.target_placement_id
        WHERE tt.id = NEW.task_transition_id
          AND p.task_id = tt.task_id
          AND (
              NEW.target_node_id IS NULL
              OR trim(NEW.target_node_id) = ''
              OR p.node_id = NEW.target_node_id
          )
    )
)
OR (
    NEW.target_node_id IS NOT NULL
    AND trim(NEW.target_node_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_records t ON t.id = tt.task_id
        JOIN workflow_nodes n ON n.id = NEW.target_node_id
        WHERE tt.id = NEW.task_transition_id
          AND n.workflow_id = t.workflow_id
    )
)
OR (
    NEW.workflow_edge_id IS NOT NULL
    AND trim(NEW.workflow_edge_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_records t ON t.id = tt.task_id
        JOIN workflow_edges e ON e.id = NEW.workflow_edge_id
        WHERE tt.id = NEW.task_transition_id
          AND e.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task transition edge references must stay within one task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_node_placements_runtime_insert
BEFORE INSERT ON task_node_placements
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_records t
    JOIN workflow_nodes n ON n.id = NEW.node_id
    WHERE t.id = NEW.task_id
      AND n.workflow_id = t.workflow_id
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
        WHERE t.id = NEW.task_id
          AND e.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task node placement references must stay within one task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_node_placements_runtime_update
BEFORE UPDATE OF task_id, node_id, parallel_batch_transition_id, parallel_branch_edge_id ON task_node_placements
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_records t
    JOIN workflow_nodes n ON n.id = NEW.node_id
    WHERE t.id = NEW.task_id
      AND n.workflow_id = t.workflow_id
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
        WHERE t.id = NEW.task_id
          AND e.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task node placement references must stay within one task workflow');
END;
-- +goose StatementEnd

CREATE TEMP TABLE migration_transition_fk_check_zero(value INTEGER NOT NULL CHECK (value = 0));

INSERT INTO migration_transition_fk_check_zero(value)
SELECT 1
WHERE EXISTS (
    SELECT 1
    FROM pragma_foreign_key_check
);

DROP TABLE migration_transition_fk_check_zero;
DROP TABLE migration_transition_source_node_check_zero;

PRAGMA foreign_keys = ON;
