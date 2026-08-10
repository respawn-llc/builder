-- +goose Up

DROP INDEX IF EXISTS task_comments_task_updated_idx;
CREATE INDEX task_comments_task_activity_idx
    ON task_comments(task_id, updated_at_unix_ms DESC, CAST('comment:' || id AS TEXT) DESC);

DROP INDEX IF EXISTS sessions_task_id_idx;
CREATE INDEX sessions_task_activity_idx
    ON sessions(task_id, created_at_unix_ms DESC, CAST('session_started:' || id AS TEXT) DESC)
    WHERE task_id IS NOT NULL;
