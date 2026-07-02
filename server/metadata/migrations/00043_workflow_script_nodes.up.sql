-- +goose Up
-- +goose NO TRANSACTION

PRAGMA foreign_keys = OFF;

BEGIN IMMEDIATE;

DROP TRIGGER IF EXISTS workflow_nodes_group_workflow_insert;
DROP TRIGGER IF EXISTS workflow_nodes_group_workflow_update;
DROP TRIGGER IF EXISTS workflow_nodes_current_task_anchor_delete;
DROP TRIGGER IF EXISTS workflow_nodes_current_task_anchor_kind_update;
DROP TRIGGER IF EXISTS workflow_nodes_task_reference_kind_update;
DROP TRIGGER IF EXISTS workflow_nodes_terminal_state_kind_update;
DROP TRIGGER IF EXISTS workflow_edges_target_workflow_insert;
DROP TRIGGER IF EXISTS workflow_edges_target_workflow_update;
DROP TRIGGER IF EXISTS task_transitions_runtime_insert;
DROP TRIGGER IF EXISTS task_transitions_runtime_update;
DROP TRIGGER IF EXISTS task_transition_edges_runtime_insert;
DROP TRIGGER IF EXISTS task_transition_edges_runtime_update;
DROP TRIGGER IF EXISTS task_node_placements_runtime_insert;
DROP TRIGGER IF EXISTS task_node_placements_runtime_update;
DROP TRIGGER IF EXISTS task_node_placements_terminal_state_insert;
DROP TRIGGER IF EXISTS task_node_placements_terminal_state_update;

DROP VIEW IF EXISTS task_transition_edge_records;
DROP VIEW IF EXISTS task_transition_records;

CREATE TABLE workflow_nodes_new (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    node_key TEXT NOT NULL CHECK (length(node_key) BETWEEN 1 AND 64),
    kind TEXT NOT NULL CHECK (kind IN ('start', 'agent', 'script', 'join', 'terminal')),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    subagent_role TEXT NOT NULL DEFAULT '',
    prompt_template TEXT NOT NULL DEFAULT '',
    output_fields_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(output_fields_json)),
    group_id TEXT REFERENCES workflow_node_groups(id) ON DELETE SET NULL,
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    input_fields_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(input_fields_json)),
    join_input_providers_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(join_input_providers_json)),
    completion_mode TEXT NOT NULL DEFAULT ''
        CHECK (
            completion_mode IN ('', 'auto', 'structured_output', 'tool', 'shell_command', 'unstructured_output')
            AND (completion_mode = '' OR kind = 'agent')
        ),
    script_path TEXT
        CHECK (
            script_path IS NULL
            OR (
                kind = 'script'
                AND length(trim(script_path)) > 0
            )
        ),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, node_key)
);

INSERT INTO workflow_nodes_new (
    id,
    workflow_id,
    node_key,
    kind,
    display_name,
    subagent_role,
    prompt_template,
    output_fields_json,
    group_id,
    sort_order,
    input_fields_json,
    join_input_providers_json,
    completion_mode,
    script_path
)
SELECT
    id,
    workflow_id,
    node_key,
    kind,
    display_name,
    subagent_role,
    prompt_template,
    output_fields_json,
    group_id,
    sort_order,
    input_fields_json,
    join_input_providers_json,
    completion_mode,
    NULL
FROM workflow_nodes
ORDER BY rowid ASC;

DROP TABLE workflow_nodes;
ALTER TABLE workflow_nodes_new RENAME TO workflow_nodes;

CREATE UNIQUE INDEX workflow_nodes_one_start_idx
    ON workflow_nodes(workflow_id)
    WHERE kind = 'start';

CREATE INDEX workflow_nodes_workflow_sort_idx
    ON workflow_nodes(workflow_id, sort_order);

CREATE TABLE task_transitions_new (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    source_run_id TEXT REFERENCES task_runs(id) ON DELETE SET NULL,
    source_placement_id TEXT REFERENCES task_node_placements(id) ON DELETE SET NULL,
    source_node_key TEXT NOT NULL DEFAULT '',
    source_node_display_name TEXT NOT NULL DEFAULT '',
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
CREATE TRIGGER workflow_nodes_group_workflow_insert
BEFORE INSERT ON workflow_nodes
FOR EACH ROW
WHEN NEW.group_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM workflow_node_groups g
    WHERE g.id = NEW.group_id
      AND g.workflow_id = NEW.workflow_id
  )
BEGIN
    SELECT RAISE(ABORT, 'workflow_nodes.group_id must belong to node workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER workflow_edges_target_workflow_insert
BEFORE INSERT ON workflow_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM workflow_transition_groups tg
    JOIN workflow_nodes source ON source.id = tg.source_node_id
    JOIN workflow_nodes target ON target.id = NEW.target_node_id
    WHERE tg.id = NEW.transition_group_id
      AND target.workflow_id = source.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow edge target node must belong to transition group workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER workflow_edges_target_workflow_update
BEFORE UPDATE OF transition_group_id, target_node_id ON workflow_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM workflow_transition_groups tg
    JOIN workflow_nodes source ON source.id = tg.source_node_id
    JOIN workflow_nodes target ON target.id = NEW.target_node_id
    WHERE tg.id = NEW.transition_group_id
      AND target.workflow_id = source.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow edge target node must belong to transition group workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER workflow_nodes_group_workflow_update
BEFORE UPDATE OF workflow_id, group_id ON workflow_nodes
FOR EACH ROW
WHEN NEW.group_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM workflow_node_groups g
    WHERE g.id = NEW.group_id
      AND g.workflow_id = NEW.workflow_id
  )
BEGIN
    SELECT RAISE(ABORT, 'workflow_nodes.group_id must belong to node workflow');
END;
-- +goose StatementEnd

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
BEGIN
    SELECT RAISE(ABORT, 'task transition references must stay within one task workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_transitions_runtime_update
BEFORE UPDATE OF task_id, source_run_id, source_placement_id, transition_id ON task_transitions
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
        JOIN workflow_transition_groups tg ON tg.id = e.transition_group_id
        JOIN workflow_nodes source ON source.id = tg.source_node_id
        WHERE tt.id = NEW.task_transition_id
          AND source.workflow_id = t.workflow_id
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
        JOIN workflow_transition_groups tg ON tg.id = e.transition_group_id
        JOIN workflow_nodes source ON source.id = tg.source_node_id
        WHERE tt.id = NEW.task_transition_id
          AND source.workflow_id = t.workflow_id
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

CREATE TEMP TABLE migration_workflow_script_nodes_fk_check_zero(value INTEGER NOT NULL CHECK (value = 0));

INSERT INTO migration_workflow_script_nodes_fk_check_zero(value)
SELECT 1
WHERE EXISTS (
    SELECT 1
    FROM pragma_foreign_key_check
);

DROP TABLE migration_workflow_script_nodes_fk_check_zero;

COMMIT;

PRAGMA foreign_keys = ON;
