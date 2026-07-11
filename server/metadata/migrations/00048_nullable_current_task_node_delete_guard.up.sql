-- +goose Up

DROP TRIGGER workflow_nodes_current_task_anchor_delete;

-- +goose StatementBegin
CREATE TRIGGER workflow_nodes_current_task_anchor_delete
BEFORE DELETE ON workflow_nodes
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_node_placements p
    JOIN task_records t ON t.id = p.task_id
    WHERE p.node_id = OLD.id
      AND t.canceled_at_unix_ms IS NULL
      AND p.state IN ('active', 'waiting_approval')
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node has current task references');
END;
-- +goose StatementEnd
