-- +goose Up

CREATE TABLE session_prompt_history_entries_without_source (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    text TEXT NOT NULL CHECK (trim(text) <> ''),
    created_at_unix_ms INTEGER NOT NULL
);

INSERT INTO session_prompt_history_entries_without_source (
    sequence,
    session_id,
    text,
    created_at_unix_ms
)
SELECT sequence, session_id, text, created_at_unix_ms
FROM session_prompt_history_entries
ORDER BY sequence;

DROP TABLE session_prompt_history_entries;

ALTER TABLE session_prompt_history_entries_without_source
    RENAME TO session_prompt_history_entries;

CREATE INDEX session_prompt_history_entries_session_sequence_idx
    ON session_prompt_history_entries(session_id, sequence);
