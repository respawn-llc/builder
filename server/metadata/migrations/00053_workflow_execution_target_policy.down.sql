-- +goose Down
-- +goose NO TRANSACTION

PRAGMA legacy_alter_table = ON;
PRAGMA foreign_keys = OFF;

DROP TRIGGER IF EXISTS worktrees_creation_base_commit_oid_insert_conflict;
DROP TRIGGER IF EXISTS worktrees_creation_base_commit_oid_immutable;
DROP VIEW IF EXISTS task_records;

CREATE TABLE workflows_previous (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0)
);

INSERT INTO workflows_previous (
    id,
    name,
    description,
    version,
    created_at_unix_ms,
    updated_at_unix_ms
)
SELECT
    id,
    name,
    description,
    version,
    created_at_unix_ms,
    updated_at_unix_ms
FROM workflows;

DROP TABLE workflows;
ALTER TABLE workflows_previous RENAME TO workflows;

CREATE TABLE tasks_previous (
    id TEXT PRIMARY KEY,
    project_workflow_link_id TEXT NOT NULL REFERENCES project_workflow_links(id) ON DELETE RESTRICT,
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    task_seq INTEGER NOT NULL CHECK (task_seq >= 1),
    short_id TEXT NOT NULL,
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    body TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL DEFAULT '',
    source_workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    managed_worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    canceled_at_unix_ms INTEGER CHECK (canceled_at_unix_ms > 0),
    cancellation_reason TEXT CHECK (cancellation_reason IS NULL OR length(trim(cancellation_reason)) > 0),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json))
);

INSERT INTO tasks_previous (
    id,
    project_workflow_link_id,
    workflow_revision_seen,
    task_seq,
    short_id,
    title,
    body,
    source_url,
    source_workspace_id,
    managed_worktree_id,
    canceled_at_unix_ms,
    cancellation_reason,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
)
SELECT
    id,
    project_workflow_link_id,
    workflow_revision_seen,
    task_seq,
    short_id,
    title,
    body,
    source_url,
    source_workspace_id,
    managed_worktree_id,
    canceled_at_unix_ms,
    cancellation_reason,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
FROM tasks;

DROP TABLE tasks;
ALTER TABLE tasks_previous RENAME TO tasks;

CREATE INDEX tasks_project_workflow_link_idx
    ON tasks(project_workflow_link_id);

CREATE INDEX tasks_project_workflow_link_updated_idx
    ON tasks(project_workflow_link_id, updated_at_unix_ms DESC, id DESC);

CREATE INDEX tasks_short_id_idx
    ON tasks(short_id);

CREATE INDEX tasks_source_workspace_idx
    ON tasks(source_workspace_id);

CREATE INDEX tasks_managed_worktree_idx
    ON tasks(managed_worktree_id);

CREATE VIEW task_records AS
SELECT
    t.id,
    pwl.project_id,
    t.project_workflow_link_id,
    pwl.workflow_id,
    t.workflow_revision_seen,
    t.task_seq,
    t.short_id,
    t.title,
    t.body,
    t.source_url,
    t.source_workspace_id,
    t.managed_worktree_id,
    t.canceled_at_unix_ms,
    t.cancellation_reason,
    t.created_at_unix_ms,
    t.updated_at_unix_ms,
    t.metadata_json
FROM tasks t
JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id;

-- +goose StatementBegin
CREATE TRIGGER tasks_project_task_seq_insert
BEFORE INSERT ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM tasks existing
    JOIN project_workflow_links existing_link ON existing_link.id = existing.project_workflow_link_id
    JOIN project_workflow_links new_link ON new_link.id = NEW.project_workflow_link_id
    WHERE existing_link.project_id = new_link.project_id
      AND existing.task_seq = NEW.task_seq
)
BEGIN
    SELECT RAISE(ABORT, 'task sequence must be unique within project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tasks_project_task_seq_update
BEFORE UPDATE OF project_workflow_link_id, task_seq ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM tasks existing
    JOIN project_workflow_links existing_link ON existing_link.id = existing.project_workflow_link_id
    JOIN project_workflow_links new_link ON new_link.id = NEW.project_workflow_link_id
    WHERE existing.id != OLD.id
      AND existing_link.project_id = new_link.project_id
      AND existing.task_seq = NEW.task_seq
)
BEGIN
    SELECT RAISE(ABORT, 'task sequence must be unique within project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tasks_project_short_id_insert
BEFORE INSERT ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM tasks existing
    JOIN project_workflow_links existing_link ON existing_link.id = existing.project_workflow_link_id
    JOIN project_workflow_links new_link ON new_link.id = NEW.project_workflow_link_id
    WHERE existing_link.project_id = new_link.project_id
      AND existing.short_id = NEW.short_id
)
BEGIN
    SELECT RAISE(ABORT, 'task short id must be unique within project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tasks_project_short_id_update
BEFORE UPDATE OF project_workflow_link_id, short_id ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM tasks existing
    JOIN project_workflow_links existing_link ON existing_link.id = existing.project_workflow_link_id
    JOIN project_workflow_links new_link ON new_link.id = NEW.project_workflow_link_id
    WHERE existing.id != OLD.id
      AND existing_link.project_id = new_link.project_id
      AND existing.short_id = NEW.short_id
)
BEGIN
    SELECT RAISE(ABORT, 'task short id must be unique within project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tasks_source_workspace_project_insert
BEFORE INSERT ON tasks
FOR EACH ROW
WHEN NEW.source_workspace_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE w.id = NEW.source_workspace_id
      AND w.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'source workspace must belong to task project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tasks_source_workspace_project_update
BEFORE UPDATE OF project_workflow_link_id, source_workspace_id ON tasks
FOR EACH ROW
WHEN NEW.source_workspace_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE w.id = NEW.source_workspace_id
      AND w.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'source workspace must belong to task project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tasks_managed_worktree_context_insert
BEFORE INSERT ON tasks
FOR EACH ROW
WHEN NEW.managed_worktree_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM worktrees wt
    JOIN workspaces source_workspace ON source_workspace.id = NEW.source_workspace_id
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE wt.id = NEW.managed_worktree_id
      AND wt.workspace_id = NEW.source_workspace_id
      AND source_workspace.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'managed worktree must belong to task source workspace');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tasks_managed_worktree_context_update
BEFORE UPDATE OF project_workflow_link_id, source_workspace_id, managed_worktree_id ON tasks
FOR EACH ROW
WHEN NEW.managed_worktree_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM worktrees wt
    JOIN workspaces source_workspace ON source_workspace.id = NEW.source_workspace_id
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE wt.id = NEW.managed_worktree_id
      AND wt.workspace_id = NEW.source_workspace_id
      AND source_workspace.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'managed worktree must belong to task source workspace');
END;
-- +goose StatementEnd

ALTER TABLE worktrees DROP COLUMN creation_base_commit_oid;

PRAGMA foreign_keys = ON;
PRAGMA legacy_alter_table = OFF;
