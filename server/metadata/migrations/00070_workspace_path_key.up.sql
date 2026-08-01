-- +goose Up

ALTER TABLE workspaces
    ADD COLUMN managed_worktree_path_key TEXT;

CREATE UNIQUE INDEX workspaces_managed_worktree_path_key_idx
    ON workspaces(managed_worktree_path_key)
    WHERE managed_worktree_path_key IS NOT NULL;
