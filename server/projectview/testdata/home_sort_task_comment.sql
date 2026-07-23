INSERT INTO task_comments (
    id, task_id, body, author_kind, author_id, created_at_unix_ms,
    updated_at_unix_ms
) VALUES (?, ?, 'comment', 'user', 'nek', ?, ?)
