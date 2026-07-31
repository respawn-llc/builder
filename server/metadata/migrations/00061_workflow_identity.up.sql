-- +goose Up
-- +goose NO TRANSACTION

PRAGMA legacy_alter_table = ON;
PRAGMA foreign_keys = OFF;

BEGIN IMMEDIATE;

DROP VIEW IF EXISTS workflow_task_status_records;
DROP VIEW IF EXISTS task_records;
DROP VIEW IF EXISTS project_workflow_link_records;

DROP TRIGGER IF EXISTS project_workflow_links_default_delete;
DROP TRIGGER IF EXISTS projects_default_workflow_link_insert;
DROP TRIGGER IF EXISTS projects_default_workflow_link_update;
DROP TRIGGER IF EXISTS projects_primary_workspace_insert;
DROP TRIGGER IF EXISTS projects_primary_workspace_update;
DROP TRIGGER IF EXISTS workflow_nodes_current_task_anchor_delete;
DROP TRIGGER IF EXISTS workflow_nodes_group_workflow_insert;
DROP TRIGGER IF EXISTS workflow_nodes_group_workflow_update;
DROP TRIGGER IF EXISTS workflow_nodes_task_reference_kind_update;

DROP INDEX IF EXISTS projects_project_key_idx;
DROP INDEX IF EXISTS projects_primary_workspace_idx;

CREATE TABLE projects_new (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    project_key TEXT NOT NULL DEFAULT '',
    next_task_seq INTEGER NOT NULL DEFAULT 1 CHECK (next_task_seq >= 1),
    default_project_workflow_link_id TEXT,
    primary_workspace_id TEXT NOT NULL DEFAULT ''
);

INSERT INTO projects_new (
    id,
    display_name,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json,
    project_key,
    next_task_seq,
    default_project_workflow_link_id,
    primary_workspace_id
)
SELECT
    id,
    display_name,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json,
    project_key,
    next_task_seq,
    NULLIF(default_project_workflow_link_id, ''),
    primary_workspace_id
FROM projects;

DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;

CREATE UNIQUE INDEX projects_project_key_idx
    ON projects(project_key)
    WHERE project_key != '';

CREATE INDEX projects_primary_workspace_idx
    ON projects(primary_workspace_id)
    WHERE primary_workspace_id != '';

-- +goose StatementBegin
CREATE TRIGGER projects_default_workflow_link_insert
BEFORE INSERT ON projects
FOR EACH ROW
WHEN NEW.default_project_workflow_link_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM project_workflow_links pwl
    WHERE pwl.id = NEW.default_project_workflow_link_id
      AND pwl.project_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'default workflow link must belong to project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER projects_default_workflow_link_update
BEFORE UPDATE OF id, default_project_workflow_link_id ON projects
FOR EACH ROW
WHEN NEW.default_project_workflow_link_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM project_workflow_links pwl
    WHERE pwl.id = NEW.default_project_workflow_link_id
      AND pwl.project_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'default workflow link must belong to project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER projects_primary_workspace_insert
BEFORE INSERT ON projects
FOR EACH ROW
WHEN NEW.primary_workspace_id != ''
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    WHERE w.id = NEW.primary_workspace_id
      AND w.project_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'primary workspace must belong to project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER projects_primary_workspace_update
BEFORE UPDATE OF id, primary_workspace_id ON projects
FOR EACH ROW
WHEN NEW.primary_workspace_id != ''
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    WHERE w.id = NEW.primary_workspace_id
      AND w.project_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'primary workspace must belong to project');
END;
-- +goose StatementEnd

CREATE TABLE workflows_new (
    id BLOB NOT NULL PRIMARY KEY CHECK (typeof(id) = 'blob' AND length(id) = 16),
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    execution_target_policy TEXT NOT NULL DEFAULT 'ask_on_first_execution'
        CHECK (execution_target_policy IN ('none', 'head', 'default_branch', 'custom_ref', 'ask_on_first_execution')),
    execution_target_custom_ref TEXT
        CHECK (execution_target_custom_ref IS NULL OR length(trim(execution_target_custom_ref)) > 0),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    CHECK (execution_target_policy = 'custom_ref' OR execution_target_custom_ref IS NULL)
);

CREATE TABLE project_workflow_links_new (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workflow_id BLOB NOT NULL REFERENCES workflows(id) ON DELETE RESTRICT
        CHECK (typeof(workflow_id) = 'blob' AND length(workflow_id) = 16),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    UNIQUE (project_id, id),
    UNIQUE (project_id, workflow_id)
);

CREATE TABLE workflow_node_groups_new (
    id TEXT PRIMARY KEY,
    workflow_id BLOB NOT NULL REFERENCES workflows(id) ON DELETE CASCADE
        CHECK (typeof(workflow_id) = 'blob' AND length(workflow_id) = 16),
    group_key TEXT NOT NULL CHECK (length(group_key) BETWEEN 1 AND 64),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, group_key)
);

CREATE TABLE workflow_nodes_new (
    id TEXT PRIMARY KEY,
    workflow_id BLOB NOT NULL REFERENCES workflows(id) ON DELETE CASCADE
        CHECK (typeof(workflow_id) = 'blob' AND length(workflow_id) = 16),
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
    script_path TEXT CHECK (
        script_path IS NULL OR (kind = 'script' AND length(trim(script_path)) > 0)
    ),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, node_key)
);

INSERT INTO workflows_new (
    id, name, description, version, execution_target_policy, execution_target_custom_ref,
    created_at_unix_ms, updated_at_unix_ms
)
SELECT
    kent_migration_workflow_id_blob_v1(id, 'workflows.id row=' || id),
    name, description, version, execution_target_policy, execution_target_custom_ref,
    created_at_unix_ms, updated_at_unix_ms
FROM workflows;

INSERT INTO project_workflow_links_new (
    id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms
)
SELECT
    id, project_id,
    kent_migration_workflow_id_blob_v1(workflow_id, 'project_workflow_links.workflow_id row=' || id),
    created_at_unix_ms, updated_at_unix_ms
FROM project_workflow_links;

INSERT INTO workflow_node_groups_new (
    id, workflow_id, group_key, display_name, sort_order
)
SELECT
    id,
    kent_migration_workflow_id_blob_v1(workflow_id, 'workflow_node_groups.workflow_id row=' || id),
    group_key, display_name, sort_order
FROM workflow_node_groups;

INSERT INTO workflow_nodes_new (
    id, workflow_id, node_key, kind, display_name, subagent_role, prompt_template,
    output_fields_json, group_id, sort_order, input_fields_json, join_input_providers_json,
    completion_mode, script_path
)
SELECT
    id,
    kent_migration_workflow_id_blob_v1(workflow_id, 'workflow_nodes.workflow_id row=' || id),
    node_key, kind, display_name, subagent_role, prompt_template, output_fields_json,
    group_id, sort_order, input_fields_json, join_input_providers_json, completion_mode, script_path
FROM workflow_nodes;

UPDATE task_pending_approvals
SET transition_snapshot_json = json_set(
    transition_snapshot_json,
    '$.workflow_id',
    kent_migration_workflow_id_text_v1(
        json_extract(transition_snapshot_json, '$.workflow_id'),
        'task_pending_approvals.transition_snapshot_json row=' || id
    )
);

UPDATE task_pending_approval_branches
SET effective_edge_configuration_json = json_set(
    effective_edge_configuration_json,
    '$.workflow_id',
    kent_migration_workflow_id_text_v1(
        json_extract(effective_edge_configuration_json, '$.workflow_id'),
        'task_pending_approval_branches.effective_edge_configuration_json row=' || approval_id || ':' || transition_branch_key
    )
);

DROP TABLE workflow_nodes;
DROP TABLE workflow_node_groups;
DROP TABLE project_workflow_links;
DROP TABLE workflows;

ALTER TABLE workflows_new RENAME TO workflows;
ALTER TABLE project_workflow_links_new RENAME TO project_workflow_links;
ALTER TABLE workflow_node_groups_new RENAME TO workflow_node_groups;
ALTER TABLE workflow_nodes_new RENAME TO workflow_nodes;

CREATE INDEX project_workflow_links_workflow_idx ON project_workflow_links(workflow_id);
CREATE INDEX workflow_node_groups_workflow_sort_idx ON workflow_node_groups(workflow_id, sort_order);
CREATE UNIQUE INDEX workflow_nodes_one_start_idx ON workflow_nodes(workflow_id) WHERE kind = 'start';
CREATE INDEX workflow_nodes_workflow_sort_idx ON workflow_nodes(workflow_id, sort_order);

CREATE VIEW project_workflow_link_records AS
SELECT
    pwl.id,
    pwl.project_id,
    pwl.workflow_id,
    CASE WHEN p.default_project_workflow_link_id = pwl.id THEN 1 ELSE 0 END AS is_default,
    pwl.created_at_unix_ms,
    pwl.updated_at_unix_ms
FROM project_workflow_links pwl
JOIN projects p ON p.id = pwl.project_id;

CREATE VIEW project_default_workflow_identity AS
SELECT
    CAST('project_id' AS TEXT) AS project_id,
    CAST(NULL AS BLOB) AS workflow_id,
    CAST(NULL AS TEXT) AS workflow_name
WHERE 0
UNION ALL
SELECT
    p.id AS project_id,
    default_workflow.id AS workflow_id,
    default_workflow.name AS workflow_name
FROM projects p
LEFT JOIN project_workflow_links default_link
    ON default_link.id = p.default_project_workflow_link_id
   AND default_link.project_id = p.id
LEFT JOIN workflows default_workflow ON default_workflow.id = default_link.workflow_id;

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

CREATE VIEW workflow_task_status_records AS
WITH current_positions AS (
    SELECT task_id, node_id, scheduling_state, interruption_reason
    FROM task_current_nodes
), status_inputs AS (
    SELECT
        task.id AS task_id,
        EXISTS (
            SELECT 1 FROM current_positions position
            JOIN workflow_nodes node ON node.id = position.node_id
            WHERE position.task_id = task.id AND node.kind = 'terminal'
        ) AS has_done,
        EXISTS (
            SELECT 1 FROM task_pending_approvals approval
            WHERE approval.source_task_id = task.id
        ) AS has_waiting_approval,
        EXISTS (
            SELECT 1 FROM current_positions position
            WHERE position.task_id = task.id AND position.scheduling_state = 'interrupted'
        ) AS has_interrupted,
        EXISTS (
            SELECT 1 FROM current_positions position
            WHERE position.task_id = task.id
              AND position.scheduling_state = 'interrupted'
              AND position.interruption_reason NOT IN ('user_interrupt', 'workflow_runtime_canceled')
        ) AS has_interrupted_attention,
        EXISTS (
            SELECT 1 FROM current_positions position
            JOIN workflow_nodes node ON node.id = position.node_id
            WHERE position.task_id = task.id AND node.kind = 'start'
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
CREATE TRIGGER project_workflow_links_default_delete
AFTER DELETE ON project_workflow_links
FOR EACH ROW
BEGIN
    UPDATE projects
    SET default_project_workflow_link_id = NULL
    WHERE id = OLD.project_id
      AND default_project_workflow_link_id = OLD.id;
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
    WHERE json_extract(branch.target_snapshot_json, '$.node_id') = OLD.id
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
    SELECT 1 FROM workflow_node_groups g
    WHERE g.id = NEW.group_id AND g.workflow_id = NEW.workflow_id
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
    SELECT 1 FROM workflow_node_groups g
    WHERE g.id = NEW.group_id AND g.workflow_id = NEW.workflow_id
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
    EXISTS (SELECT 1 FROM task_current_nodes current_node WHERE current_node.node_id = OLD.id)
    OR EXISTS (SELECT 1 FROM task_pending_approvals approval WHERE approval.source_node_id = OLD.id)
    OR EXISTS (
        SELECT 1 FROM task_pending_approval_branches branch
        WHERE json_extract(branch.target_snapshot_json, '$.node_id') = OLD.id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node kind changes are blocked for nodes referenced by current task state');
END;
-- +goose StatementEnd

CREATE TEMP TABLE migration_workflow_identity_fk_check(value INTEGER NOT NULL CHECK (value = 0));
INSERT INTO migration_workflow_identity_fk_check(value)
SELECT 1 FROM pragma_foreign_key_check LIMIT 1;
DROP TABLE migration_workflow_identity_fk_check;

COMMIT;

PRAGMA foreign_keys = ON;
PRAGMA legacy_alter_table = OFF;
