-- +goose Up

DROP INDEX sessions_visible_category_recency_idx;

CREATE INDEX sessions_visible_category_recency_idx
ON sessions(project_id, COALESCE(category, 'main'), updated_at_unix_ms DESC, id DESC)
WHERE launch_visible <> 0;
