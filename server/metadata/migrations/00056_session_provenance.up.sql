-- +goose Up

ALTER TABLE sessions
ADD COLUMN previous_session_id TEXT
    CHECK (previous_session_id IS NULL OR length(trim(previous_session_id)) > 0);

ALTER TABLE sessions
ADD COLUMN parent_agent_session_id TEXT
    CHECK (parent_agent_session_id IS NULL OR length(trim(parent_agent_session_id)) > 0);

UPDATE sessions
SET previous_session_id = NULLIF(parent_session_id, '');

ALTER TABLE sessions DROP COLUMN parent_session_id;
