-- +goose Up

ALTER TABLE projects ADD COLUMN lifecycle_state TEXT NOT NULL DEFAULT 'active'
    CHECK (lifecycle_state IN ('active', 'deleting'));

ALTER TABLE projects ADD COLUMN lifecycle_generation INTEGER NOT NULL DEFAULT 1
    CHECK (lifecycle_generation > 0);

CREATE INDEX projects_lifecycle_state_idx
    ON projects(lifecycle_state, lifecycle_generation);
