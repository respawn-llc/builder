-- +goose Up

PRAGMA defer_foreign_keys = ON;

DROP TRIGGER IF EXISTS workspaces_child_refs_delete_cleanup;
DROP TRIGGER IF EXISTS workspaces_session_project_update;
DROP TRIGGER IF EXISTS worktrees_session_workspace_update;
DROP TRIGGER IF EXISTS sessions_workspace_project_insert;
DROP TRIGGER IF EXISTS sessions_workspace_project_update;
DROP TRIGGER IF EXISTS sessions_worktree_workspace_insert;
DROP TRIGGER IF EXISTS sessions_worktree_workspace_update;

CREATE TABLE sessions_new (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    artifact_relpath TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    first_prompt_preview TEXT NOT NULL DEFAULT '',
    input_draft TEXT NOT NULL DEFAULT '',
    parent_session_id TEXT CHECK (parent_session_id IS NULL OR length(trim(parent_session_id)) > 0),
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL,
    last_sequence INTEGER NOT NULL DEFAULT 0,
    model_request_count INTEGER NOT NULL DEFAULT 0,
    launch_visible INTEGER NOT NULL DEFAULT 0,
    cwd_relpath TEXT NOT NULL DEFAULT '.',
    continuation_json TEXT NOT NULL DEFAULT '{}',
    locked_json TEXT NOT NULL DEFAULT '{}',
    usage_state_json TEXT NOT NULL DEFAULT '{}',
    metadata_json TEXT NOT NULL DEFAULT '{}'
);

INSERT INTO sessions_new (
    id,
    project_id,
    workspace_id,
    worktree_id,
    artifact_relpath,
    name,
    first_prompt_preview,
    input_draft,
    parent_session_id,
    created_at_unix_ms,
    updated_at_unix_ms,
    last_sequence,
    model_request_count,
    launch_visible,
    cwd_relpath,
    continuation_json,
    locked_json,
    usage_state_json,
    metadata_json
)
SELECT
    id,
    project_id,
    workspace_id,
    worktree_id,
    artifact_relpath,
    name,
    first_prompt_preview,
    input_draft,
    NULLIF(trim(parent_session_id), ''),
    created_at_unix_ms,
    updated_at_unix_ms,
    last_sequence,
    model_request_count,
    launch_visible,
    cwd_relpath,
    continuation_json,
    locked_json,
    usage_state_json,
    metadata_json
FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

CREATE INDEX sessions_project_idx ON sessions(project_id, updated_at_unix_ms DESC);
CREATE INDEX sessions_workspace_idx ON sessions(workspace_id, updated_at_unix_ms DESC);
CREATE UNIQUE INDEX sessions_artifact_relpath_idx ON sessions(artifact_relpath);

-- +goose StatementBegin
CREATE TRIGGER workspaces_child_refs_delete_cleanup
BEFORE DELETE ON workspaces
FOR EACH ROW
BEGIN
    UPDATE sessions
    SET worktree_id = NULL
    WHERE workspace_id = OLD.id
      AND worktree_id IN (
          SELECT wt.id
          FROM worktrees wt
          WHERE wt.workspace_id = OLD.id
      );

    UPDATE tasks
    SET managed_worktree_id = NULL
    WHERE source_workspace_id = OLD.id
      AND managed_worktree_id IN (
          SELECT wt.id
          FROM worktrees wt
          WHERE wt.workspace_id = OLD.id
      );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER workspaces_session_project_update
BEFORE UPDATE OF id, project_id ON workspaces
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM sessions s
    WHERE s.workspace_id = OLD.id
      AND (
          OLD.id != NEW.id
          OR s.project_id != NEW.project_id
      )
)
BEGIN
    SELECT RAISE(ABORT, 'session workspace must belong to project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER worktrees_session_workspace_update
BEFORE UPDATE OF id, workspace_id ON worktrees
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM sessions s
    WHERE s.worktree_id = OLD.id
      AND (
          OLD.id != NEW.id
          OR s.workspace_id IS NULL
          OR s.workspace_id != NEW.workspace_id
      )
)
BEGIN
    SELECT RAISE(ABORT, 'session worktree must belong to session workspace');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER sessions_workspace_project_insert
BEFORE INSERT ON sessions
FOR EACH ROW
WHEN NEW.workspace_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    WHERE w.id = NEW.workspace_id
      AND w.project_id = NEW.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'session workspace must belong to project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER sessions_workspace_project_update
BEFORE UPDATE OF project_id, workspace_id ON sessions
FOR EACH ROW
WHEN NEW.workspace_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    WHERE w.id = NEW.workspace_id
      AND w.project_id = NEW.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'session workspace must belong to project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER sessions_worktree_workspace_insert
BEFORE INSERT ON sessions
FOR EACH ROW
WHEN NEW.worktree_id IS NOT NULL
 AND (
    NEW.workspace_id IS NULL
    OR NOT EXISTS (
        SELECT 1
        FROM worktrees wt
        WHERE wt.id = NEW.worktree_id
          AND wt.workspace_id = NEW.workspace_id
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'session worktree must belong to session workspace');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER sessions_worktree_workspace_update
BEFORE UPDATE OF workspace_id, worktree_id ON sessions
FOR EACH ROW
WHEN NEW.worktree_id IS NOT NULL
 AND (
    NEW.workspace_id IS NULL
    OR NOT EXISTS (
        SELECT 1
        FROM worktrees wt
        WHERE wt.id = NEW.worktree_id
          AND wt.workspace_id = NEW.workspace_id
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'session worktree must belong to session workspace');
END;
-- +goose StatementEnd
