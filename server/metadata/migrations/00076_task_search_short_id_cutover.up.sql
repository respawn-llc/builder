-- +goose Up

DROP TRIGGER task_search_comment_body_after_update;
DROP TRIGGER task_search_comment_body_before_update;
DROP TRIGGER task_search_comment_delete;
DROP TRIGGER task_search_comment_insert;
DROP TRIGGER task_search_document_delete;
DROP TRIGGER task_search_document_insert;
DROP TRIGGER task_search_task_body_after_update;
DROP TRIGGER task_search_task_body_before_update;
DROP TRIGGER task_search_task_delete;
DROP TRIGGER task_search_task_insert;
DROP TRIGGER task_search_task_title_after_update;
DROP TRIGGER task_search_task_title_before_update;

DROP TABLE task_search_fts;
DROP VIEW task_search_content;
DROP TABLE task_search_documents;

CREATE TABLE task_search_documents (
    document_id INTEGER PRIMARY KEY,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('short_id', 'title', 'body', 'comment')),
    task_id TEXT REFERENCES tasks(id) ON DELETE CASCADE,
    comment_id TEXT REFERENCES task_comments(id) ON DELETE CASCADE,
    CHECK (
        (source_kind IN ('short_id', 'title', 'body') AND task_id IS NOT NULL AND comment_id IS NULL)
        OR
        (source_kind = 'comment' AND task_id IS NULL AND comment_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX task_search_documents_task_short_id_unique
    ON task_search_documents(task_id)
    WHERE source_kind = 'short_id';

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
    CASE WHEN document.source_kind = 'short_id' THEN task.short_id END AS short_id,
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

CREATE VIRTUAL TABLE task_search_short_id_fts
USING fts5(
    short_id,
    content = 'task_search_content',
    content_rowid = 'document_id',
    tokenize = 'trigram case_sensitive 0 remove_diacritics 1'
);

INSERT INTO task_search_documents (source_kind, task_id)
SELECT 'short_id', id
FROM tasks;

INSERT INTO task_search_documents (source_kind, task_id)
SELECT 'title', id
FROM tasks;

INSERT INTO task_search_documents (source_kind, task_id)
SELECT 'body', id
FROM tasks;

INSERT INTO task_search_documents (source_kind, comment_id)
SELECT 'comment', id
FROM task_comments;

INSERT INTO task_search_fts(rowid, title, body, comment)
SELECT content.document_id, content.title, content.body, content.comment
FROM task_search_content content
JOIN task_search_documents document
  ON document.document_id = content.document_id
WHERE document.source_kind != 'short_id';

INSERT INTO task_search_short_id_fts(rowid, short_id)
SELECT content.document_id, content.short_id
FROM task_search_content content
JOIN task_search_documents document
  ON document.document_id = content.document_id
WHERE document.source_kind = 'short_id';

-- +goose StatementBegin
CREATE TRIGGER task_search_short_id_document_insert
AFTER INSERT ON task_search_documents
WHEN NEW.source_kind = 'short_id'
BEGIN
    INSERT INTO task_search_short_id_fts(rowid, short_id)
    SELECT document_id, short_id
    FROM task_search_content
    WHERE document_id = NEW.document_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_short_id_document_delete
BEFORE DELETE ON task_search_documents
WHEN OLD.source_kind = 'short_id'
BEGIN
    INSERT INTO task_search_short_id_fts(task_search_short_id_fts, rowid, short_id)
    SELECT 'delete', document_id, short_id
    FROM task_search_content
    WHERE document_id = OLD.document_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_text_document_insert
AFTER INSERT ON task_search_documents
WHEN NEW.source_kind != 'short_id'
BEGIN
    INSERT INTO task_search_fts(rowid, title, body, comment)
    SELECT document_id, title, body, comment
    FROM task_search_content
    WHERE document_id = NEW.document_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_text_document_delete
BEFORE DELETE ON task_search_documents
WHEN OLD.source_kind != 'short_id'
BEGIN
    INSERT INTO task_search_fts(task_search_fts, rowid, title, body, comment)
    SELECT 'delete', document_id, title, body, comment
    FROM task_search_content
    WHERE document_id = OLD.document_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_task_insert
AFTER INSERT ON tasks
BEGIN
    INSERT INTO task_search_documents (source_kind, task_id)
    VALUES ('short_id', NEW.id), ('title', NEW.id), ('body', NEW.id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_comment_insert
AFTER INSERT ON task_comments
BEGIN
    INSERT INTO task_search_documents (source_kind, comment_id)
    VALUES ('comment', NEW.id);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_task_title_before_update
BEFORE UPDATE OF title ON tasks
BEGIN
    INSERT INTO task_search_fts(task_search_fts, rowid, title, body, comment)
    SELECT 'delete', document_id, OLD.title, NULL, NULL
    FROM task_search_documents
    WHERE task_id = OLD.id
      AND source_kind = 'title';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_task_title_after_update
AFTER UPDATE OF title ON tasks
BEGIN
    INSERT INTO task_search_fts(rowid, title, body, comment)
    SELECT document_id, NEW.title, NULL, NULL
    FROM task_search_documents
    WHERE task_id = NEW.id
      AND source_kind = 'title';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_task_body_before_update
BEFORE UPDATE OF body ON tasks
BEGIN
    INSERT INTO task_search_fts(task_search_fts, rowid, title, body, comment)
    SELECT 'delete', document_id, NULL, OLD.body, NULL
    FROM task_search_documents
    WHERE task_id = OLD.id
      AND source_kind = 'body';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_task_body_after_update
AFTER UPDATE OF body ON tasks
BEGIN
    INSERT INTO task_search_fts(rowid, title, body, comment)
    SELECT document_id, NULL, NEW.body, NULL
    FROM task_search_documents
    WHERE task_id = NEW.id
      AND source_kind = 'body';
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_comment_body_before_update
BEFORE UPDATE OF body ON task_comments
BEGIN
    INSERT INTO task_search_fts(task_search_fts, rowid, title, body, comment)
    SELECT 'delete', document_id, NULL, NULL, OLD.body
    FROM task_search_documents
    WHERE comment_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_comment_body_after_update
AFTER UPDATE OF body ON task_comments
BEGIN
    INSERT INTO task_search_fts(rowid, title, body, comment)
    SELECT document_id, NULL, NULL, NEW.body
    FROM task_search_documents
    WHERE comment_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_comment_delete
BEFORE DELETE ON task_comments
BEGIN
    DELETE FROM task_search_documents
    WHERE comment_id = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_task_delete
BEFORE DELETE ON tasks
BEGIN
    DELETE FROM task_search_documents
    WHERE task_id = OLD.id
       OR comment_id IN (
            SELECT id
            FROM task_comments
            WHERE task_id = OLD.id
       );
END;
-- +goose StatementEnd
