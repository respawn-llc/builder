-- +goose Up

CREATE TABLE task_dependencies (
    blocker_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    blocked_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (blocker_task_id, blocked_task_id)
);

CREATE INDEX task_dependencies_reverse_idx
    ON task_dependencies(blocked_task_id, blocker_task_id);
