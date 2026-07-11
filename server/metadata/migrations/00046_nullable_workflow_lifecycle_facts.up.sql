-- +goose Up
-- +goose NO TRANSACTION

PRAGMA legacy_alter_table = ON;
PRAGMA foreign_keys = OFF;

DROP VIEW IF EXISTS workflow_task_status_task_records;
DROP VIEW IF EXISTS workflow_task_status_run_records;
DROP VIEW IF EXISTS workflow_task_status_transition_records;
DROP VIEW IF EXISTS task_run_records;
DROP VIEW IF EXISTS task_records;

CREATE TABLE tasks_new (
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

INSERT INTO tasks_new (
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
    NULLIF(canceled_at_unix_ms, 0),
    NULLIF(cancellation_reason, ''),
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
FROM tasks;

DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;

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

-- The task table reconstruction drops its task-scoped integrity triggers.
-- Recreate the current invariants against the rebuilt authoritative table.
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

CREATE TABLE task_runs_new (
    id TEXT PRIMARY KEY,
    placement_id TEXT NOT NULL REFERENCES task_node_placements(id) ON DELETE CASCADE,
    session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    run_generation INTEGER NOT NULL DEFAULT 0 CHECK (run_generation >= 0),
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    automation_requested_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (automation_requested_at_unix_ms >= 0),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    started_at_unix_ms INTEGER CHECK (started_at_unix_ms > 0),
    completed_at_unix_ms INTEGER CHECK (completed_at_unix_ms > 0),
    interrupted_at_unix_ms INTEGER CHECK (interrupted_at_unix_ms > 0),
    interruption_reason TEXT CHECK (interruption_reason IS NULL OR length(trim(interruption_reason)) > 0),
    interruption_detail_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(interruption_detail_json)),
    waiting_ask_id TEXT CHECK (waiting_ask_id IS NULL OR length(trim(waiting_ask_id)) > 0),
    effective_completion_mode TEXT CHECK (effective_completion_mode IS NULL OR effective_completion_mode IN ('structured_output', 'tool', 'shell_command', 'unstructured_output')),
    invalid_completion_count INTEGER NOT NULL DEFAULT 0 CHECK (invalid_completion_count >= 0),
    run_start_snapshot_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(run_start_snapshot_json)),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json))
);

INSERT INTO task_runs_new (
    id,
    placement_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json,
    waiting_ask_id,
    effective_completion_mode,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
)
SELECT
    id,
    placement_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    NULLIF(started_at_unix_ms, 0),
    NULLIF(completed_at_unix_ms, 0),
    NULLIF(interrupted_at_unix_ms, 0),
    NULLIF(interruption_reason, ''),
    interruption_detail_json,
    NULLIF(waiting_ask_id, ''),
    NULLIF(effective_completion_mode, ''),
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
FROM task_runs;

DROP TABLE task_runs;
ALTER TABLE task_runs_new RENAME TO task_runs;

CREATE INDEX task_runs_placement_idx
    ON task_runs(placement_id);

CREATE INDEX task_runs_session_idx
    ON task_runs(session_id);

CREATE INDEX task_runs_runnable_idx
    ON task_runs(automation_requested_at_unix_ms, id)
    WHERE automation_requested_at_unix_ms > 0
      AND completed_at_unix_ms IS NULL
      AND interrupted_at_unix_ms IS NULL;

CREATE INDEX task_runs_outcome_idx
    ON task_runs(started_at_unix_ms, completed_at_unix_ms, interrupted_at_unix_ms);

CREATE INDEX task_runs_placement_created_idx
    ON task_runs(placement_id, created_at_unix_ms DESC);

CREATE VIEW task_run_records AS
SELECT
    r.id,
    p.task_id,
    r.placement_id,
    p.node_id,
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

CREATE VIEW workflow_task_status_task_records AS
SELECT
    t.id,
    t.canceled_at_unix_ms
FROM task_records t;

CREATE VIEW workflow_task_status_run_records AS
SELECT
    r.id,
    r.task_id,
    r.placement_id,
    r.session_id,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.waiting_ask_id
FROM task_run_records r;

CREATE VIEW workflow_task_status_transition_records AS
SELECT
    tt.task_id,
    tt.state,
    tt.source_node_id
FROM task_transition_records tt;

PRAGMA foreign_keys = ON;
PRAGMA legacy_alter_table = OFF;
