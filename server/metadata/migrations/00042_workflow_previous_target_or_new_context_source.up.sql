-- +goose Up

PRAGMA legacy_alter_table = ON;

DROP TRIGGER IF EXISTS workflow_edges_target_workflow_insert;
DROP TRIGGER IF EXISTS workflow_edges_target_workflow_update;

CREATE TABLE workflow_edges_new (
    id TEXT PRIMARY KEY,
    transition_group_id TEXT NOT NULL REFERENCES workflow_transition_groups(id) ON DELETE CASCADE,
    edge_key TEXT NOT NULL CHECK (length(edge_key) BETWEEN 1 AND 64),
    target_node_id TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE CASCADE,
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    context_mode TEXT NOT NULL CHECK (context_mode IN ('new_session', 'continue_session', 'compact_and_continue_session')),
    input_bindings_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_bindings_json)),
    output_requirements_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_requirements_json)),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    context_source_kind TEXT NOT NULL DEFAULT 'immediate_source'
        CHECK (context_source_kind IN ('immediate_source', 'selected_node', 'previous_target', 'previous_target_or_new')),
    context_source_node_key TEXT NOT NULL DEFAULT ''
        CHECK (
            (
                context_source_kind IN ('immediate_source', 'previous_target', 'previous_target_or_new')
                AND context_source_node_key = ''
            )
            OR (context_source_kind = 'selected_node' AND length(context_source_node_key) BETWEEN 1 AND 64)
        ),
    prompt_template TEXT NOT NULL DEFAULT '',
    parameters_json TEXT NOT NULL DEFAULT '[]'
        CHECK (json_valid(parameters_json) AND json_type(parameters_json) = 'array'),
    UNIQUE (transition_group_id, edge_key)
);

INSERT INTO workflow_edges_new (
    id,
    transition_group_id,
    edge_key,
    target_node_id,
    requires_approval,
    context_mode,
    input_bindings_json,
    output_requirements_json,
    sort_order,
    context_source_kind,
    context_source_node_key,
    prompt_template,
    parameters_json
)
SELECT
    id,
    transition_group_id,
    edge_key,
    target_node_id,
    requires_approval,
    context_mode,
    input_bindings_json,
    output_requirements_json,
    sort_order,
    context_source_kind,
    context_source_node_key,
    prompt_template,
    parameters_json
FROM workflow_edges
ORDER BY rowid ASC;

DROP TABLE workflow_edges;
ALTER TABLE workflow_edges_new RENAME TO workflow_edges;

PRAGMA legacy_alter_table = OFF;

CREATE INDEX workflow_edges_transition_group_sort_idx
    ON workflow_edges(transition_group_id, sort_order);

CREATE INDEX workflow_edges_target_node_idx
    ON workflow_edges(target_node_id);

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
