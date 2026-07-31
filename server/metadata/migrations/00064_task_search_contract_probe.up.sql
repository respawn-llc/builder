-- +goose Up

CREATE TABLE task_search_contract_probe (
    document_id INTEGER PRIMARY KEY CHECK (document_id = -1),
    title TEXT NOT NULL CHECK (title = 'ZÀBcQ')
);

INSERT INTO task_search_contract_probe (document_id, title)
VALUES (-1, 'ZÀBcQ');

DROP VIEW task_search_content;

CREATE VIEW task_search_content AS
SELECT
    document.document_id,
    CASE WHEN document.source_kind = 'title' THEN task.title END AS title,
    CASE WHEN document.source_kind = 'body' THEN task.body END AS body,
    CASE WHEN document.source_kind = 'comment' THEN comment.body END AS comment
FROM task_search_documents document
LEFT JOIN tasks task ON task.id = document.task_id
LEFT JOIN task_comments comment ON comment.id = document.comment_id
UNION ALL
SELECT document_id, title, NULL, NULL
FROM task_search_contract_probe;

INSERT INTO task_search_fts(task_search_fts) VALUES ('rebuild');
