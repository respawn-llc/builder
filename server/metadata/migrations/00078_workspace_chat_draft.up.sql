-- +goose Up

ALTER TABLE workspaces
ADD COLUMN chat_draft_json TEXT
CHECK (chat_draft_json IS NULL OR json_valid(chat_draft_json));
