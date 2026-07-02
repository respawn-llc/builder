-- +goose Up
-- +goose NO TRANSACTION

PRAGMA foreign_keys = OFF;

BEGIN IMMEDIATE;

DROP TRIGGER IF EXISTS workflow_nodes_group_workflow_insert;
DROP TRIGGER IF EXISTS workflow_nodes_group_workflow_update;
DROP TRIGGER IF EXISTS task_transitions_runtime_insert;
DROP TRIGGER IF EXISTS task_transitions_runtime_update;
DROP TRIGGER IF EXISTS task_transition_edges_runtime_insert;
DROP TRIGGER IF EXISTS task_transition_edges_runtime_update;
DROP TRIGGER IF EXISTS task_node_placements_runtime_insert;
DROP TRIGGER IF EXISTS task_node_placements_runtime_update;

DROP VIEW IF EXISTS task_transition_records;

CREATE TABLE workflows_new (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    graph_revision INTEGER NOT NULL DEFAULT 1 CHECK (graph_revision >= 1),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0)
);

INSERT INTO workflows_new (
    id,
    name,
    description,
    graph_revision,
    created_at_unix_ms,
    updated_at_unix_ms
)
SELECT
    id,
    name,
    description,
    graph_revision,
    created_at_unix_ms,
    updated_at_unix_ms
FROM workflows
ORDER BY rowid ASC;

DROP TABLE workflows;
ALTER TABLE workflows_new RENAME TO workflows;

CREATE TABLE workflow_node_groups_new (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    group_key TEXT NOT NULL CHECK (length(group_key) BETWEEN 1 AND 64),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, group_key)
);

INSERT INTO workflow_node_groups_new (
    id,
    workflow_id,
    group_key,
    display_name,
    sort_order
)
SELECT
    id,
    workflow_id,
    group_key,
    display_name,
    sort_order
FROM workflow_node_groups
ORDER BY rowid ASC;

DROP TABLE workflow_node_groups;
ALTER TABLE workflow_node_groups_new RENAME TO workflow_node_groups;

CREATE INDEX workflow_node_groups_workflow_sort_idx
    ON workflow_node_groups(workflow_id, sort_order);

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
    sort_order
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
    sort_order
FROM workflow_nodes
ORDER BY rowid ASC;

DROP TABLE workflow_nodes;
ALTER TABLE workflow_nodes_new RENAME TO workflow_nodes;

CREATE UNIQUE INDEX workflow_nodes_one_start_idx
    ON workflow_nodes(workflow_id)
    WHERE kind = 'start';

CREATE INDEX workflow_nodes_workflow_sort_idx
    ON workflow_nodes(workflow_id, sort_order);

CREATE TABLE workflow_transition_groups_new (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    source_node_id TEXT NOT NULL,
    transition_id TEXT NOT NULL CHECK (length(transition_id) BETWEEN 1 AND 64),
    display_name TEXT NOT NULL DEFAULT '' CHECK (length(display_name) <= 120),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    UNIQUE (workflow_id, id),
    UNIQUE (source_node_id, transition_id),
    FOREIGN KEY (workflow_id, source_node_id) REFERENCES workflow_nodes(workflow_id, id) ON DELETE CASCADE
);

INSERT INTO workflow_transition_groups_new (
    id,
    workflow_id,
    source_node_id,
    transition_id,
    display_name,
    sort_order
)
SELECT
    id,
    workflow_id,
    source_node_id,
    transition_id,
    display_name,
    sort_order
FROM workflow_transition_groups
ORDER BY rowid ASC;

DROP TABLE workflow_transition_groups;
ALTER TABLE workflow_transition_groups_new RENAME TO workflow_transition_groups;

CREATE TABLE workflow_edges_new (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    transition_group_id TEXT NOT NULL,
    edge_key TEXT NOT NULL CHECK (length(edge_key) BETWEEN 1 AND 64),
    target_node_id TEXT NOT NULL,
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    context_mode TEXT NOT NULL CHECK (context_mode IN ('new_session', 'continue_session', 'compact_and_continue_session')),
    input_bindings_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_bindings_json)),
    output_requirements_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_requirements_json)),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    UNIQUE (workflow_id, id),
    UNIQUE (transition_group_id, edge_key),
    FOREIGN KEY (workflow_id, transition_group_id) REFERENCES workflow_transition_groups(workflow_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workflow_id, target_node_id) REFERENCES workflow_nodes(workflow_id, id) ON DELETE CASCADE
);

INSERT INTO workflow_edges_new (
    id,
    workflow_id,
    transition_group_id,
    edge_key,
    target_node_id,
    requires_approval,
    context_mode,
    input_bindings_json,
    output_requirements_json,
    sort_order
)
SELECT
    id,
    workflow_id,
    transition_group_id,
    edge_key,
    target_node_id,
    requires_approval,
    context_mode,
    input_bindings_json,
    output_requirements_json,
    sort_order
FROM workflow_edges
ORDER BY rowid ASC;

DROP TABLE workflow_edges;
ALTER TABLE workflow_edges_new RENAME TO workflow_edges;

CREATE INDEX workflow_edges_transition_group_sort_idx
    ON workflow_edges(transition_group_id, sort_order);

CREATE INDEX workflow_edges_target_node_idx
    ON workflow_edges(target_node_id);

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

CREATE TEMP TABLE migration_workflow_definition_metadata_check_zero(value INTEGER NOT NULL CHECK (value = 0));

INSERT INTO migration_workflow_definition_metadata_check_zero(value)
SELECT 1
WHERE EXISTS (
    SELECT 1
    FROM pragma_foreign_key_check
);

DROP TABLE migration_workflow_definition_metadata_check_zero;

COMMIT;

PRAGMA foreign_keys = ON;
