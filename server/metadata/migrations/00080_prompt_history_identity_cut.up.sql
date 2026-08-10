-- +goose Up

DROP TABLE session_prompt_history_entries;

CREATE TABLE session_prompt_history_entries (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    text TEXT NOT NULL CHECK (trim(text) <> ''),
    created_at_unix_ms INTEGER NOT NULL
);

CREATE INDEX session_prompt_history_entries_session_sequence_idx
    ON session_prompt_history_entries(session_id, sequence);

UPDATE sessions
SET metadata_json = json_remove(metadata_json, '$.input_draft_recovery_buffers')
WHERE json_valid(metadata_json) = 1
  AND json_type(metadata_json, '$.input_draft_recovery_buffers') IS NOT NULL;
