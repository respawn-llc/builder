-- +goose Up

CREATE TABLE task_search_documents (
    document_id INTEGER PRIMARY KEY,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('title', 'body', 'comment')),
    task_id TEXT REFERENCES tasks(id) ON DELETE CASCADE,
    comment_id TEXT REFERENCES task_comments(id) ON DELETE CASCADE,
    CHECK (
        (source_kind IN ('title', 'body') AND task_id IS NOT NULL AND comment_id IS NULL)
        OR
        (source_kind = 'comment' AND task_id IS NULL AND comment_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX task_search_documents_task_title_unique
    ON task_search_documents(task_id)
    WHERE source_kind = 'title';

CREATE UNIQUE INDEX task_search_documents_task_body_unique
    ON task_search_documents(task_id)
    WHERE source_kind = 'body';

CREATE UNIQUE INDEX task_search_documents_comment_unique
    ON task_search_documents(comment_id)
    WHERE source_kind = 'comment';

CREATE VIEW task_search_content AS
SELECT
    document.document_id,
    CASE WHEN document.source_kind = 'title' THEN task.title END AS title,
    CASE WHEN document.source_kind = 'body' THEN task.body END AS body,
    CASE WHEN document.source_kind = 'comment' THEN comment.body END AS comment
FROM task_search_documents document
LEFT JOIN tasks task ON task.id = document.task_id
LEFT JOIN task_comments comment ON comment.id = document.comment_id;

CREATE VIRTUAL TABLE task_search_fts
USING fts5(
    title,
    body,
    comment,
    content = 'task_search_content',
    content_rowid = 'document_id',
    tokenize = 'trigram case_sensitive 0 remove_diacritics 1'
);

INSERT INTO task_search_documents (source_kind, task_id)
SELECT 'title', id
FROM tasks;

INSERT INTO task_search_documents (source_kind, task_id)
SELECT 'body', id
FROM tasks;

INSERT INTO task_search_documents (source_kind, comment_id)
SELECT 'comment', id
FROM task_comments;

INSERT INTO task_search_fts(task_search_fts) VALUES ('rebuild');
