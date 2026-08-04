-- +goose Up
-- +goose NO TRANSACTION

PRAGMA legacy_alter_table = ON;
PRAGMA foreign_keys = OFF;

BEGIN IMMEDIATE;

DROP TRIGGER IF EXISTS workflow_nodes_current_task_anchor_delete;
DROP TRIGGER IF EXISTS workflow_nodes_group_workflow_insert;
DROP TRIGGER IF EXISTS workflow_nodes_group_workflow_update;
DROP TRIGGER IF EXISTS workflow_nodes_task_reference_kind_update;

DROP INDEX IF EXISTS workflow_nodes_one_start_idx;
DROP INDEX IF EXISTS workflow_nodes_workflow_sort_idx;

CREATE TABLE workflow_nodes_new (
    id TEXT PRIMARY KEY,
    workflow_id BLOB NOT NULL REFERENCES workflows(id) ON DELETE CASCADE
        CHECK (typeof(workflow_id) = 'blob' AND length(workflow_id) = 16),
    node_key TEXT NOT NULL CHECK (length(node_key) BETWEEN 1 AND 64),
    kind TEXT NOT NULL CHECK (kind IN ('start', 'agent', 'script', 'join', 'terminal')),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    subagent_role TEXT NOT NULL DEFAULT '',
    group_id TEXT REFERENCES workflow_node_groups(id) ON DELETE SET NULL,
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

INSERT INTO workflow_nodes_new (
    id,
    workflow_id,
    node_key,
    kind,
    display_name,
    subagent_role,
    group_id,
    sort_order,
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
    group_id,
    sort_order,
    join_input_providers_json,
    completion_mode,
    script_path
FROM workflow_nodes
ORDER BY rowid ASC;

DROP TABLE workflow_nodes;
ALTER TABLE workflow_nodes_new RENAME TO workflow_nodes;

CREATE UNIQUE INDEX workflow_nodes_one_start_idx
    ON workflow_nodes(workflow_id)
    WHERE kind = 'start';

CREATE INDEX workflow_nodes_workflow_sort_idx
    ON workflow_nodes(workflow_id, sort_order);

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

PRAGMA legacy_alter_table = OFF;

CREATE TEMP TABLE migration_workflow_node_contracts_fk_check_zero(
    value INTEGER NOT NULL CHECK (value = 0)
);

INSERT INTO migration_workflow_node_contracts_fk_check_zero(value)
SELECT 1
WHERE EXISTS (
    SELECT 1
    FROM pragma_foreign_key_check
);

DROP TABLE migration_workflow_node_contracts_fk_check_zero;

COMMIT;

PRAGMA foreign_keys = ON;
PRAGMA legacy_alter_table = OFF;
