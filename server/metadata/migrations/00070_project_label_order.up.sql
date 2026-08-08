-- +goose Up

-- Project labels are capped at 100 records. Reordering first moves ordinals
-- into the temporary band 101..200, then rewrites them into 1..100.
-- The bound mirrors two times labelcontract.MaxProjectLabels for this migration.
ALTER TABLE project_labels
ADD COLUMN ordinal INTEGER NOT NULL DEFAULT 1
CHECK (ordinal BETWEEN 1 AND 200);

WITH ordered_labels AS (
    SELECT
        id,
        ROW_NUMBER() OVER (
            PARTITION BY project_id
            ORDER BY name COLLATE kent_label_casefold_v1 ASC, id ASC
        ) AS ordinal
    FROM project_labels
)
UPDATE project_labels
SET ordinal = (
    SELECT ordered_labels.ordinal
    FROM ordered_labels
    WHERE ordered_labels.id = project_labels.id
);

CREATE UNIQUE INDEX project_labels_project_ordinal_idx
    ON project_labels(project_id, ordinal);

DROP INDEX IF EXISTS tasks_project_workflow_link_updated_idx;

CREATE INDEX tasks_project_workflow_link_updated_idx
    ON tasks(project_workflow_link_id, updated_at_unix_ms DESC, id DESC);
