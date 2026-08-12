-- +goose Up

CREATE TEMP TABLE migration_graph_node_group_ids (
    old_id TEXT NOT NULL PRIMARY KEY,
    new_id BLOB NOT NULL UNIQUE
        CHECK (typeof(new_id) = 'blob' AND length(new_id) = 16 AND new_id != zeroblob(16))
) WITHOUT ROWID;

INSERT INTO migration_graph_node_group_ids (old_id, new_id)
SELECT old_id, kent_migration_graph_entity_id_blob_v1(old_id, 'Node Group')
FROM (
    SELECT id AS old_id FROM workflow_node_groups
    UNION
    SELECT group_id FROM workflow_nodes WHERE group_id IS NOT NULL
);

CREATE TEMP TABLE migration_graph_node_ids (
    old_id TEXT NOT NULL PRIMARY KEY,
    new_id BLOB NOT NULL UNIQUE
        CHECK (typeof(new_id) = 'blob' AND length(new_id) = 16 AND new_id != zeroblob(16))
) WITHOUT ROWID;

INSERT INTO migration_graph_node_ids (old_id, new_id)
SELECT old_id, kent_migration_graph_entity_id_blob_v1(old_id, 'Node')
FROM (
    SELECT id AS old_id FROM workflow_nodes
    UNION
    SELECT source_node_id FROM workflow_transition_groups
    UNION
    SELECT target_node_id FROM workflow_edges
    UNION
    SELECT node_id FROM task_current_nodes
    UNION
    SELECT source_node_id FROM task_pending_approvals
    UNION
    SELECT node_id FROM session_workflow_node_associations
    UNION
    SELECT json_extract(transition_snapshot_json, '$.source_node_id') FROM task_pending_approvals
    UNION
    SELECT json_extract(target_snapshot_json, '$.node_id') FROM task_pending_approval_branches
    UNION
    SELECT json_extract(effective_edge_configuration_json, '$.target_node_id')
    FROM task_pending_approval_branches
);

CREATE TEMP TABLE migration_graph_transition_ids (
    old_id TEXT NOT NULL PRIMARY KEY,
    new_id BLOB NOT NULL UNIQUE
        CHECK (typeof(new_id) = 'blob' AND length(new_id) = 16 AND new_id != zeroblob(16))
) WITHOUT ROWID;

INSERT INTO migration_graph_transition_ids (old_id, new_id)
SELECT old_id, kent_migration_graph_entity_id_blob_v1(old_id, 'Transition')
FROM (
    SELECT id AS old_id FROM workflow_transition_groups
    UNION
    SELECT transition_group_id FROM workflow_edges
    UNION
    SELECT json_extract(transition_snapshot_json, '$.id') FROM task_pending_approvals
    UNION
    SELECT json_extract(effective_edge_configuration_json, '$.transition_group_id')
    FROM task_pending_approval_branches
);

CREATE TEMP TABLE migration_graph_edge_ids (
    old_id TEXT NOT NULL PRIMARY KEY,
    new_id BLOB NOT NULL UNIQUE
        CHECK (typeof(new_id) = 'blob' AND length(new_id) = 16 AND new_id != zeroblob(16))
) WITHOUT ROWID;

INSERT INTO migration_graph_edge_ids (old_id, new_id)
SELECT old_id, kent_migration_graph_entity_id_blob_v1(old_id, 'Transition Branch')
FROM (
    SELECT id AS old_id FROM workflow_edges
    UNION
    SELECT entered_by_edge_id FROM task_current_nodes WHERE entered_by_edge_id IS NOT NULL
    UNION
    SELECT json_extract(provider.value, '$.provider_edge_id')
    FROM workflow_nodes node, json_each(node.join_input_providers_json) provider
    UNION
    SELECT json_extract(target_snapshot_json, '$.entered_by_edge_id')
    FROM task_pending_approval_branches
    WHERE json_type(target_snapshot_json, '$.entered_by_edge_id') IS NOT NULL
      AND json_type(target_snapshot_json, '$.entered_by_edge_id') != 'null'
    UNION
    SELECT json_extract(effective_edge_configuration_json, '$.id')
    FROM task_pending_approval_branches
);

DROP VIEW workflow_task_status_records;
DROP TRIGGER sessions_task_owner_clear_associations;
DROP TRIGGER task_active_fanouts_serial_current_node_insert;
DROP TRIGGER task_active_fanouts_serial_current_node_update;

CREATE TABLE workflow_node_groups_v80 (
    id BLOB PRIMARY KEY
        CHECK (typeof(id) = 'blob' AND length(id) = 16 AND id != zeroblob(16)),
    workflow_id BLOB NOT NULL REFERENCES workflows(id) ON DELETE CASCADE
        CHECK (typeof(workflow_id) = 'blob' AND length(workflow_id) = 16),
    group_key TEXT NOT NULL CHECK (length(group_key) BETWEEN 1 AND 64),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, group_key)
);

CREATE TABLE workflow_nodes_v80 (
    id BLOB PRIMARY KEY
        CHECK (typeof(id) = 'blob' AND length(id) = 16 AND id != zeroblob(16)),
    workflow_id BLOB NOT NULL REFERENCES workflows(id) ON DELETE CASCADE
        CHECK (typeof(workflow_id) = 'blob' AND length(workflow_id) = 16),
    node_key TEXT NOT NULL CHECK (length(node_key) BETWEEN 1 AND 64),
    kind TEXT NOT NULL CHECK (kind IN ('start', 'agent', 'script', 'join', 'terminal')),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    subagent_role TEXT NOT NULL DEFAULT '',
    group_id BLOB REFERENCES workflow_node_groups_v80(id) ON DELETE SET NULL
        CHECK (
            group_id IS NULL
            OR (typeof(group_id) = 'blob' AND length(group_id) = 16 AND group_id != zeroblob(16))
        ),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    join_input_providers_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(join_input_providers_json)),
    completion_mode TEXT NOT NULL DEFAULT ''
        CHECK (
            completion_mode IN ('', 'auto', 'structured_output', 'tool', 'shell_command', 'unstructured_output')
            AND (completion_mode = '' OR kind = 'agent')
        ),
    script_path TEXT CHECK (
        script_path IS NULL OR (kind = 'script' AND length(trim(script_path)) > 0)
    ),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, node_key)
);

CREATE TABLE workflow_transition_groups_v80 (
    id BLOB PRIMARY KEY
        CHECK (typeof(id) = 'blob' AND length(id) = 16 AND id != zeroblob(16)),
    source_node_id BLOB NOT NULL REFERENCES workflow_nodes_v80(id) ON DELETE CASCADE
        CHECK (typeof(source_node_id) = 'blob' AND length(source_node_id) = 16 AND source_node_id != zeroblob(16)),
    transition_id TEXT NOT NULL CHECK (length(transition_id) BETWEEN 1 AND 64),
    display_name TEXT NOT NULL DEFAULT '' CHECK (length(display_name) <= 120),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    UNIQUE (source_node_id, transition_id)
);

CREATE TABLE workflow_edges_v80 (
    id BLOB PRIMARY KEY
        CHECK (typeof(id) = 'blob' AND length(id) = 16 AND id != zeroblob(16)),
    transition_group_id BLOB NOT NULL REFERENCES workflow_transition_groups_v80(id) ON DELETE CASCADE
        CHECK (
            typeof(transition_group_id) = 'blob'
            AND length(transition_group_id) = 16
            AND transition_group_id != zeroblob(16)
        ),
    edge_key TEXT NOT NULL CHECK (length(edge_key) BETWEEN 1 AND 64),
    target_node_id BLOB NOT NULL REFERENCES workflow_nodes_v80(id) ON DELETE CASCADE
        CHECK (typeof(target_node_id) = 'blob' AND length(target_node_id) = 16 AND target_node_id != zeroblob(16)),
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
    assignee_selection TEXT NOT NULL DEFAULT 'configured'
        CHECK (assignee_selection IN ('configured', 'previous_node')),
    thinking_selection TEXT NOT NULL DEFAULT 'configured'
        CHECK (thinking_selection IN ('configured', 'previous_node')),
    UNIQUE (transition_group_id, edge_key)
);

CREATE TABLE task_current_nodes_v80 (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    node_id BLOB NOT NULL REFERENCES workflow_nodes_v80(id) ON DELETE RESTRICT
        CHECK (typeof(node_id) = 'blob' AND length(node_id) = 16 AND node_id != zeroblob(16)),
    transition_branch_key TEXT
        CHECK (transition_branch_key IS NULL OR length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    current_input_values_json TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(current_input_values_json) AND json_type(current_input_values_json) = 'object'),
    prior_node_values_json TEXT NOT NULL DEFAULT '{"transition_parameters":{}}'
        CHECK (json_valid(prior_node_values_json) AND json_type(prior_node_values_json) = 'object'),
    session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    scheduling_state TEXT
        CHECK (scheduling_state IS NULL OR scheduling_state IN ('ready', 'admitted', 'interrupted', 'failed')),
    interruption_reason TEXT
        CHECK (interruption_reason IS NULL OR length(trim(interruption_reason)) > 0),
    interruption_detail_json TEXT
        CHECK (
            interruption_detail_json IS NULL
            OR (json_valid(interruption_detail_json) AND json_type(interruption_detail_json) = 'object')
        ),
    interrupted_at_unix_ms INTEGER
        CHECK (interrupted_at_unix_ms IS NULL OR interrupted_at_unix_ms > 0),
    entered_by_edge_id BLOB
        CHECK (
            entered_by_edge_id IS NULL
            OR (
                typeof(entered_by_edge_id) = 'blob'
                AND length(entered_by_edge_id) = 16
                AND entered_by_edge_id != zeroblob(16)
            )
        ),
    effective_assignee TEXT
        CHECK (effective_assignee IS NULL OR length(trim(effective_assignee)) > 0),
    effective_thinking TEXT
        CHECK (effective_thinking IS NULL OR length(trim(effective_thinking)) > 0),
    assignee_origin TEXT
        CHECK (assignee_origin IS NULL OR assignee_origin IN (
            'configured_fallback',
            'transition_selected',
            'retained_session'
        )),
    FOREIGN KEY (task_id, transition_branch_key)
        REFERENCES task_active_fanout_branches(task_id, transition_branch_key)
        ON DELETE RESTRICT,
    CHECK (
        (
            scheduling_state = 'interrupted'
            AND interruption_reason IS NOT NULL
            AND interruption_detail_json IS NOT NULL
            AND interrupted_at_unix_ms IS NOT NULL
        )
        OR (
            scheduling_state IS NULL OR scheduling_state != 'interrupted'
        )
        AND interruption_reason IS NULL
        AND interruption_detail_json IS NULL
        AND interrupted_at_unix_ms IS NULL
    )
);

CREATE TABLE task_pending_approvals_v80 (
    id TEXT PRIMARY KEY,
    source_task_id TEXT NOT NULL,
    source_node_id BLOB NOT NULL
        CHECK (
            typeof(source_node_id) = 'blob'
            AND length(source_node_id) = 16
            AND source_node_id != zeroblob(16)
        ),
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

CREATE TABLE task_pending_approval_branches_v80 (
    approval_id TEXT NOT NULL REFERENCES task_pending_approvals_v80(id) ON DELETE CASCADE,
    transition_branch_key TEXT NOT NULL CHECK (length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    target_snapshot_json TEXT NOT NULL
        CHECK (json_valid(target_snapshot_json) AND json_type(target_snapshot_json) = 'object'),
    effective_edge_configuration_json TEXT NOT NULL
        CHECK (json_valid(effective_edge_configuration_json) AND json_type(effective_edge_configuration_json) = 'object'),
    context_source_resolution_json TEXT NOT NULL
        CHECK (json_valid(context_source_resolution_json) AND json_type(context_source_resolution_json) = 'object'),
    PRIMARY KEY (approval_id, transition_branch_key)
);

CREATE TABLE session_workflow_node_associations_v80 (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    node_id BLOB NOT NULL
        CHECK (typeof(node_id) = 'blob' AND length(node_id) = 16 AND node_id != zeroblob(16)),
    transition_branch_key TEXT
        CHECK (transition_branch_key IS NULL OR length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    associated_at_unix_ms INTEGER NOT NULL CHECK (associated_at_unix_ms > 0)
);

INSERT INTO workflow_node_groups_v80 (
    id, workflow_id, group_key, display_name, sort_order
)
SELECT map.new_id, source.workflow_id, source.group_key, source.display_name, source.sort_order
FROM workflow_node_groups source
JOIN migration_graph_node_group_ids map ON map.old_id = source.id
ORDER BY source.rowid;

INSERT INTO workflow_nodes_v80 (
    id, workflow_id, node_key, kind, display_name, subagent_role, group_id,
    sort_order, join_input_providers_json, completion_mode, script_path
)
SELECT
    node_map.new_id,
    source.workflow_id,
    source.node_key,
    source.kind,
    source.display_name,
    source.subagent_role,
    group_map.new_id,
    source.sort_order,
    CASE
        WHEN json_array_length(source.join_input_providers_json) = 0 THEN '[]'
        ELSE (
            SELECT json_group_array(json(provider_json))
            FROM (
                SELECT json_set(
                    provider.value,
                    '$.provider_edge_id',
                    kent_graph_entity_id_text_v1(edge_map.new_id)
                ) AS provider_json
                FROM json_each(source.join_input_providers_json) provider
                JOIN migration_graph_edge_ids edge_map
                    ON edge_map.old_id = json_extract(provider.value, '$.provider_edge_id')
                ORDER BY CAST(provider.key AS INTEGER)
            )
        )
    END,
    source.completion_mode,
    source.script_path
FROM workflow_nodes source
JOIN migration_graph_node_ids node_map ON node_map.old_id = source.id
LEFT JOIN migration_graph_node_group_ids group_map ON group_map.old_id = source.group_id
ORDER BY source.rowid;

INSERT INTO workflow_transition_groups_v80 (
    id, source_node_id, transition_id, display_name, sort_order, description
)
SELECT
    transition_map.new_id,
    node_map.new_id,
    source.transition_id,
    source.display_name,
    source.sort_order,
    source.description
FROM workflow_transition_groups source
JOIN migration_graph_transition_ids transition_map ON transition_map.old_id = source.id
JOIN migration_graph_node_ids node_map ON node_map.old_id = source.source_node_id
ORDER BY source.rowid;

INSERT INTO workflow_edges_v80 (
    id, transition_group_id, edge_key, target_node_id, requires_approval,
    context_mode, input_bindings_json, output_requirements_json, sort_order,
    context_source_kind, context_source_node_key, prompt_template, parameters_json,
    assignee_selection, thinking_selection
)
SELECT
    edge_map.new_id,
    transition_map.new_id,
    source.edge_key,
    node_map.new_id,
    source.requires_approval,
    source.context_mode,
    source.input_bindings_json,
    source.output_requirements_json,
    source.sort_order,
    source.context_source_kind,
    source.context_source_node_key,
    source.prompt_template,
    source.parameters_json,
    source.assignee_selection,
    source.thinking_selection
FROM workflow_edges source
JOIN migration_graph_edge_ids edge_map ON edge_map.old_id = source.id
JOIN migration_graph_transition_ids transition_map ON transition_map.old_id = source.transition_group_id
JOIN migration_graph_node_ids node_map ON node_map.old_id = source.target_node_id
ORDER BY source.rowid;

INSERT INTO task_current_nodes_v80 (
    task_id, node_id, transition_branch_key, current_input_values_json,
    prior_node_values_json, session_id, scheduling_state, interruption_reason,
    interruption_detail_json, interrupted_at_unix_ms, entered_by_edge_id,
    effective_assignee, effective_thinking, assignee_origin
)
SELECT
    source.task_id,
    node_map.new_id,
    source.transition_branch_key,
    source.current_input_values_json,
    source.prior_node_values_json,
    source.session_id,
    source.scheduling_state,
    source.interruption_reason,
    source.interruption_detail_json,
    source.interrupted_at_unix_ms,
    edge_map.new_id,
    source.effective_assignee,
    source.effective_thinking,
    source.assignee_origin
FROM task_current_nodes source
JOIN migration_graph_node_ids node_map ON node_map.old_id = source.node_id
LEFT JOIN migration_graph_edge_ids edge_map ON edge_map.old_id = source.entered_by_edge_id
ORDER BY source.rowid;

INSERT INTO task_pending_approvals_v80 (
    id, source_task_id, source_node_id, source_transition_branch_key,
    source_session_id, workflow_version, transition_snapshot_json,
    materialized_values_json, created_at_unix_ms
)
SELECT
    source.id,
    source.source_task_id,
    node_map.new_id,
    source.source_transition_branch_key,
    source.source_session_id,
    source.workflow_version,
    json_set(
        source.transition_snapshot_json,
        '$.id',
        kent_graph_entity_id_text_v1(transition_map.new_id),
        '$.source_node_id',
        kent_graph_entity_id_text_v1(snapshot_node_map.new_id)
    ),
    source.materialized_values_json,
    source.created_at_unix_ms
FROM task_pending_approvals source
JOIN migration_graph_node_ids node_map ON node_map.old_id = source.source_node_id
JOIN migration_graph_transition_ids transition_map
    ON transition_map.old_id = json_extract(source.transition_snapshot_json, '$.id')
JOIN migration_graph_node_ids snapshot_node_map
    ON snapshot_node_map.old_id = json_extract(source.transition_snapshot_json, '$.source_node_id')
ORDER BY source.rowid;

INSERT INTO task_pending_approval_branches_v80 (
    approval_id, transition_branch_key, target_snapshot_json,
    effective_edge_configuration_json, context_source_resolution_json
)
SELECT
    source.approval_id,
    source.transition_branch_key,
    CASE
        WHEN json_type(source.target_snapshot_json, '$.entered_by_edge_id') IS NULL
          OR json_type(source.target_snapshot_json, '$.entered_by_edge_id') = 'null'
        THEN json_set(
            source.target_snapshot_json,
            '$.node_id',
            kent_graph_entity_id_text_v1(target_node_map.new_id)
        )
        ELSE json_set(
            source.target_snapshot_json,
            '$.node_id',
            kent_graph_entity_id_text_v1(target_node_map.new_id),
            '$.entered_by_edge_id',
            kent_graph_entity_id_text_v1(entering_edge_map.new_id)
        )
    END,
    json_set(
        source.effective_edge_configuration_json,
        '$.id',
        kent_graph_entity_id_text_v1(edge_map.new_id),
        '$.transition_group_id',
        kent_graph_entity_id_text_v1(transition_map.new_id),
        '$.target_node_id',
        kent_graph_entity_id_text_v1(edge_target_node_map.new_id)
    ),
    source.context_source_resolution_json
FROM task_pending_approval_branches source
JOIN migration_graph_node_ids target_node_map
    ON target_node_map.old_id = json_extract(source.target_snapshot_json, '$.node_id')
LEFT JOIN migration_graph_edge_ids entering_edge_map
    ON entering_edge_map.old_id = json_extract(source.target_snapshot_json, '$.entered_by_edge_id')
JOIN migration_graph_edge_ids edge_map
    ON edge_map.old_id = json_extract(source.effective_edge_configuration_json, '$.id')
JOIN migration_graph_transition_ids transition_map
    ON transition_map.old_id = json_extract(source.effective_edge_configuration_json, '$.transition_group_id')
JOIN migration_graph_node_ids edge_target_node_map
    ON edge_target_node_map.old_id = json_extract(source.effective_edge_configuration_json, '$.target_node_id')
ORDER BY source.rowid;

INSERT INTO session_workflow_node_associations_v80 (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
)
SELECT
    source.session_id,
    node_map.new_id,
    source.transition_branch_key,
    source.associated_at_unix_ms
FROM session_workflow_node_associations source
JOIN migration_graph_node_ids node_map ON node_map.old_id = source.node_id
ORDER BY source.rowid;

CREATE TEMP TABLE migration_graph_identity_postcondition (
    value INTEGER NOT NULL CHECK (value = 0)
);

INSERT INTO migration_graph_identity_postcondition (value)
SELECT 1
WHERE
    (SELECT count(*) FROM workflow_node_groups_v80) != (SELECT count(*) FROM workflow_node_groups)
    OR (SELECT count(*) FROM workflow_nodes_v80) != (SELECT count(*) FROM workflow_nodes)
    OR (SELECT count(*) FROM workflow_transition_groups_v80) != (SELECT count(*) FROM workflow_transition_groups)
    OR (SELECT count(*) FROM workflow_edges_v80) != (SELECT count(*) FROM workflow_edges)
    OR (SELECT count(*) FROM task_current_nodes_v80) != (SELECT count(*) FROM task_current_nodes)
    OR (SELECT count(*) FROM task_pending_approvals_v80) != (SELECT count(*) FROM task_pending_approvals)
    OR (
        SELECT count(*) FROM task_pending_approval_branches_v80
    ) != (
        SELECT count(*) FROM task_pending_approval_branches
    )
    OR (
        SELECT count(*) FROM session_workflow_node_associations_v80
    ) != (
        SELECT count(*) FROM session_workflow_node_associations
    )
    OR EXISTS (
        SELECT 1
        FROM pragma_foreign_key_check
        WHERE "table" LIKE '%_v80'
    );

DROP TABLE task_pending_approval_branches;
DROP TABLE task_pending_approvals;
DROP TABLE task_current_nodes;
DROP TABLE session_workflow_node_associations;
DROP TABLE workflow_edges;
DROP TABLE workflow_transition_groups;
DROP TABLE workflow_nodes;
DROP TABLE workflow_node_groups;

ALTER TABLE workflow_node_groups_v80 RENAME TO workflow_node_groups;
ALTER TABLE workflow_nodes_v80 RENAME TO workflow_nodes;
ALTER TABLE workflow_transition_groups_v80 RENAME TO workflow_transition_groups;
ALTER TABLE workflow_edges_v80 RENAME TO workflow_edges;
ALTER TABLE task_current_nodes_v80 RENAME TO task_current_nodes;
ALTER TABLE task_pending_approvals_v80 RENAME TO task_pending_approvals;
ALTER TABLE task_pending_approval_branches_v80 RENAME TO task_pending_approval_branches;
ALTER TABLE session_workflow_node_associations_v80 RENAME TO session_workflow_node_associations;

CREATE INDEX workflow_node_groups_workflow_sort_idx
    ON workflow_node_groups(workflow_id, sort_order);

CREATE UNIQUE INDEX workflow_nodes_one_start_idx
    ON workflow_nodes(workflow_id)
    WHERE kind = 'start';

CREATE INDEX workflow_nodes_workflow_sort_idx
    ON workflow_nodes(workflow_id, sort_order);

CREATE INDEX workflow_edges_target_node_idx
    ON workflow_edges(target_node_id);

CREATE INDEX workflow_edges_transition_group_sort_idx
    ON workflow_edges(transition_group_id, sort_order);

CREATE UNIQUE INDEX task_current_nodes_serial_task_unique_idx
    ON task_current_nodes(task_id)
    WHERE transition_branch_key IS NULL;

CREATE UNIQUE INDEX task_current_nodes_parallel_branch_unique_idx
    ON task_current_nodes(task_id, transition_branch_key)
    WHERE transition_branch_key IS NOT NULL;

CREATE UNIQUE INDEX task_pending_approvals_serial_source_unique_idx
    ON task_pending_approvals(source_task_id, source_node_id)
    WHERE source_transition_branch_key IS NULL;

CREATE UNIQUE INDEX task_pending_approvals_parallel_source_unique_idx
    ON task_pending_approvals(source_task_id, source_node_id, source_transition_branch_key)
    WHERE source_transition_branch_key IS NOT NULL;

CREATE UNIQUE INDEX session_workflow_node_associations_serial_unique_idx
    ON session_workflow_node_associations(session_id, node_id)
    WHERE transition_branch_key IS NULL;

CREATE UNIQUE INDEX session_workflow_node_associations_branch_unique_idx
    ON session_workflow_node_associations(session_id, node_id, transition_branch_key)
    WHERE transition_branch_key IS NOT NULL;

CREATE INDEX session_workflow_node_associations_lookup_idx
    ON session_workflow_node_associations(
        node_id,
        transition_branch_key,
        associated_at_unix_ms DESC,
        session_id DESC
    );

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
CREATE TRIGGER workflow_edges_target_workflow_insert
BEFORE INSERT ON workflow_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM workflow_transition_groups transition_group
    JOIN workflow_nodes source ON source.id = transition_group.source_node_id
    JOIN workflow_nodes target ON target.id = NEW.target_node_id
    WHERE transition_group.id = NEW.transition_group_id
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
    FROM workflow_transition_groups transition_group
    JOIN workflow_nodes source ON source.id = transition_group.source_node_id
    JOIN workflow_nodes target ON target.id = NEW.target_node_id
    WHERE transition_group.id = NEW.transition_group_id
      AND target.workflow_id = source.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow edge target node must belong to transition group workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER workflow_nodes_current_task_anchor_delete
BEFORE DELETE ON workflow_nodes
FOR EACH ROW
WHEN EXISTS (
    SELECT 1 FROM task_pending_approvals approval
    WHERE approval.source_node_id = OLD.id
) OR EXISTS (
    SELECT 1 FROM task_pending_approval_branches branch
    WHERE kent_graph_entity_id_blob_v1(
        json_extract(branch.target_snapshot_json, '$.node_id')
    ) = OLD.id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node has current task references');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER workflow_nodes_group_workflow_insert
BEFORE INSERT ON workflow_nodes
FOR EACH ROW
WHEN NEW.group_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM workflow_node_groups node_group
    WHERE node_group.id = NEW.group_id
      AND node_group.workflow_id = NEW.workflow_id
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
    SELECT 1 FROM workflow_node_groups node_group
    WHERE node_group.id = NEW.group_id
      AND node_group.workflow_id = NEW.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow_nodes.group_id must belong to node workflow');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER workflow_nodes_task_reference_kind_update
BEFORE UPDATE OF kind ON workflow_nodes
FOR EACH ROW
WHEN NEW.kind != OLD.kind
AND (
    EXISTS (
        SELECT 1 FROM task_current_nodes current_node
        WHERE current_node.node_id = OLD.id
    )
    OR EXISTS (
        SELECT 1 FROM task_pending_approvals approval
        WHERE approval.source_node_id = OLD.id
    )
    OR EXISTS (
        SELECT 1 FROM task_pending_approval_branches branch
        WHERE kent_graph_entity_id_blob_v1(
            json_extract(branch.target_snapshot_json, '$.node_id')
        ) = OLD.id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node kind changes are blocked for nodes referenced by current task state');
END;
-- +goose StatementEnd

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
          (
              current_node.transition_branch_key IS NULL
              AND NEW.source_transition_branch_key IS NULL
          )
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
          (
              current_node.transition_branch_key IS NULL
              AND NEW.source_transition_branch_key IS NULL
          )
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
          (
              approval.source_transition_branch_key IS NULL
              AND OLD.transition_branch_key IS NULL
          )
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
          (
              approval.source_transition_branch_key IS NULL
              AND OLD.transition_branch_key IS NULL
          )
          OR approval.source_transition_branch_key = OLD.transition_branch_key
      )
)
BEGIN
    SELECT RAISE(ABORT, 'current node with pending approval cannot change identity');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_prior_transition_parameters_insert
BEFORE INSERT ON task_current_nodes
FOR EACH ROW
WHEN json_type(NEW.prior_node_values_json, '$.transition_parameters') IS NOT 'object'
  OR json(json_remove(NEW.prior_node_values_json, '$.transition_parameters')) != '{}'
BEGIN
    SELECT RAISE(ABORT, 'current node prior values must contain exactly one Transition parameter object');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_prior_transition_parameters_update
BEFORE UPDATE OF prior_node_values_json ON task_current_nodes
FOR EACH ROW
WHEN json_type(NEW.prior_node_values_json, '$.transition_parameters') IS NOT 'object'
  OR json(json_remove(NEW.prior_node_values_json, '$.transition_parameters')) != '{}'
BEGIN
    SELECT RAISE(ABORT, 'current node prior values must contain exactly one Transition parameter object');
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

-- +goose StatementBegin
CREATE TRIGGER task_pending_approval_branches_prior_transition_parameters_insert
BEFORE INSERT ON task_pending_approval_branches
FOR EACH ROW
WHEN json_type(NEW.target_snapshot_json, '$.prior_values') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_values.transition_parameters') IS NOT 'object'
  OR json(
      json_remove(
          json_extract(NEW.target_snapshot_json, '$.prior_values'),
          '$.transition_parameters'
      )
  ) != '{}'
  OR json_type(NEW.target_snapshot_json, '$.prior_node_values') IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'pending approval target prior values must contain exactly one Transition parameter object');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_pending_approval_branches_prior_transition_parameters_update
BEFORE UPDATE OF target_snapshot_json ON task_pending_approval_branches
FOR EACH ROW
WHEN json_type(NEW.target_snapshot_json, '$.prior_values') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_values.transition_parameters') IS NOT 'object'
  OR json(
      json_remove(
          json_extract(NEW.target_snapshot_json, '$.prior_values'),
          '$.transition_parameters'
      )
  ) != '{}'
  OR json_type(NEW.target_snapshot_json, '$.prior_node_values') IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'pending approval target prior values must contain exactly one Transition parameter object');
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
        task_id,
        node_id,
        scheduling_state,
        interruption_reason
    FROM task_current_nodes
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
              AND position.interruption_reason NOT IN (
                  'user_interrupt',
                  'workflow_runtime_canceled'
              )
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
            SELECT kent_graph_entity_id_text_v1(position.node_id) AS node_id
            FROM current_positions position
            WHERE position.task_id = task.id
            ORDER BY position.node_id
        )
    ), '[]') AS node_ids_json,
    COALESCE((
        SELECT json_group_array(attention_type)
        FROM (
            SELECT 'approval' AS attention_type
            WHERE input.has_waiting_approval
            UNION
            SELECT 'interrupted'
            WHERE input.has_interrupted_attention
            ORDER BY attention_type
        )
    ), '[]') AS attention_types_json
FROM task_records task
JOIN status_inputs input ON input.task_id = task.id;

UPDATE workflows
SET version = version + 1
WHERE id IN (
    SELECT DISTINCT groups.workflow_id
    FROM workflow_node_groups groups
    JOIN migration_graph_node_group_ids map ON map.new_id = groups.id
    WHERE map.old_id != kent_graph_entity_id_text_v1(map.new_id)
    UNION
    SELECT DISTINCT nodes.workflow_id
    FROM workflow_nodes nodes
    JOIN migration_graph_node_ids map ON map.new_id = nodes.id
    WHERE map.old_id != kent_graph_entity_id_text_v1(map.new_id)
    UNION
    SELECT DISTINCT nodes.workflow_id
    FROM workflow_transition_groups groups
    JOIN workflow_nodes nodes ON nodes.id = groups.source_node_id
    JOIN migration_graph_transition_ids map ON map.new_id = groups.id
    WHERE map.old_id != kent_graph_entity_id_text_v1(map.new_id)
    UNION
    SELECT DISTINCT nodes.workflow_id
    FROM workflow_edges edges
    JOIN workflow_transition_groups groups ON groups.id = edges.transition_group_id
    JOIN workflow_nodes nodes ON nodes.id = groups.source_node_id
    JOIN migration_graph_edge_ids map ON map.new_id = edges.id
    WHERE map.old_id != kent_graph_entity_id_text_v1(map.new_id)
);

INSERT INTO migration_graph_identity_postcondition (value)
SELECT 1
WHERE EXISTS (
    SELECT 1 FROM pragma_foreign_key_check
);

DROP TABLE migration_graph_identity_postcondition;
DROP TABLE migration_graph_edge_ids;
DROP TABLE migration_graph_transition_ids;
DROP TABLE migration_graph_node_ids;
DROP TABLE migration_graph_node_group_ids;
