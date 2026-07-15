-- +goose Up

ALTER TABLE sessions
ADD COLUMN category TEXT
    CHECK (category IS NULL OR category IN ('main', 'subagent'));

CREATE INDEX sessions_visible_category_recency_idx
ON sessions(project_id, category, updated_at_unix_ms DESC, id DESC)
WHERE launch_visible <> 0;
