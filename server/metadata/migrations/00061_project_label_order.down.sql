-- +goose Down

DROP INDEX IF EXISTS tasks_project_workflow_link_updated_idx;

DROP INDEX IF EXISTS project_labels_project_ordinal_idx;

ALTER TABLE project_labels
DROP COLUMN ordinal;

CREATE INDEX tasks_project_workflow_link_updated_idx
    ON tasks(project_workflow_link_id, updated_at_unix_ms DESC, id DESC);
