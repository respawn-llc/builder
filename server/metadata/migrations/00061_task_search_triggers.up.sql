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
