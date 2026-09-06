-- +goose Up

UPDATE sessions
SET locked_json = json_remove(locked_json, '$.context_window', '$.context_percent')
WHERE json_type(locked_json, '$.context_window') IS NOT NULL
   OR json_type(locked_json, '$.context_percent') IS NOT NULL;
