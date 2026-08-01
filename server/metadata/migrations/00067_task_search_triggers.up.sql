-- +goose Up

-- +goose StatementBegin
CREATE TRIGGER task_search_document_insert
AFTER INSERT ON task_search_documents
BEGIN
    INSERT INTO task_search_fts(rowid, title, body, comment)
    SELECT document_id, title, body, comment
    FROM task_search_content
    WHERE document_id = NEW.document_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_search_document_delete
BEFORE DELETE ON task_search_documents
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
    VALUES ('title', NEW.id), ('body', NEW.id);
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
