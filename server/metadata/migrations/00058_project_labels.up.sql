-- +goose Up

CREATE TABLE project_labels (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    UNIQUE (project_id, name COLLATE kent_label_casefold_v1)
);

CREATE TABLE task_label_assignments (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    label_id TEXT NOT NULL REFERENCES project_labels(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, label_id)
);

CREATE INDEX task_label_assignments_label_task_idx
    ON task_label_assignments(label_id, task_id);

-- +goose StatementBegin
CREATE TRIGGER task_label_assignments_project_insert
BEFORE INSERT ON task_label_assignments
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM tasks t
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    JOIN project_labels pl ON pl.id = NEW.label_id
    WHERE t.id = NEW.task_id
      AND pwl.project_id = pl.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'task label assignment must stay within one project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_label_assignments_project_update
BEFORE UPDATE OF task_id, label_id ON task_label_assignments
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM tasks t
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    JOIN project_labels pl ON pl.id = NEW.label_id
    WHERE t.id = NEW.task_id
      AND pwl.project_id = pl.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'task label assignment must stay within one project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER project_labels_assignment_project_update
BEFORE UPDATE OF project_id ON project_labels
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_label_assignments tla
    JOIN tasks t ON t.id = tla.task_id
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    WHERE tla.label_id = OLD.id
      AND pwl.project_id != NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'assigned label must stay within the task project');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tasks_label_assignment_project_update
BEFORE UPDATE OF project_workflow_link_id ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_label_assignments tla
    JOIN project_labels pl ON pl.id = tla.label_id
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE tla.task_id = OLD.id
      AND pl.project_id != pwl.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'task labels must stay within the task project');
END;
-- +goose StatementEnd
