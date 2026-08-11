-- +goose Down

ALTER TABLE workspaces
DROP COLUMN chat_draft_json;
