-- +goose Up

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
