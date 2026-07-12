-- +goose Up

CREATE TABLE worktree_operations (
    operation_id TEXT PRIMARY KEY,
    payload_json TEXT NOT NULL,
    expected_target_json TEXT NOT NULL,
    execution_mode TEXT NOT NULL,
    lifecycle_state TEXT NOT NULL,
    lifecycle_version INTEGER NOT NULL,
    terminal_result_json TEXT,
    terminal_error_json TEXT,
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL
);

CREATE INDEX worktree_operations_recovery_idx
    ON worktree_operations(lifecycle_state, operation_id);
