-- +goose Up

DROP TRIGGER task_node_placements_terminal_state_insert;
DROP TRIGGER task_node_placements_terminal_state_update;

-- +goose StatementBegin
CREATE TRIGGER task_node_placements_terminal_state_insert
BEFORE INSERT ON task_node_placements
FOR EACH ROW
WHEN NEW.state = 'waiting_approval'
AND EXISTS (
    SELECT 1
    FROM workflow_nodes n
    WHERE n.id = NEW.node_id
      AND n.kind = 'terminal'
)
BEGIN
    SELECT RAISE(ABORT, 'terminal task node placements cannot await approval');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_node_placements_terminal_state_update
BEFORE UPDATE OF node_id, state ON task_node_placements
FOR EACH ROW
WHEN NEW.state = 'waiting_approval'
AND EXISTS (
    SELECT 1
    FROM workflow_nodes n
    WHERE n.id = NEW.node_id
      AND n.kind = 'terminal'
)
BEGIN
    SELECT RAISE(ABORT, 'terminal task node placements cannot await approval');
END;
-- +goose StatementEnd

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
      AND t.canceled_at_unix_ms = 0
      AND p.state IN ('active', 'waiting_approval')
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node has current task references');
END;
-- +goose StatementEnd

UPDATE task_node_placements
SET state = 'active'
WHERE state = 'completed'
  AND EXISTS (
      SELECT 1
      FROM workflow_nodes n
      WHERE n.id = task_node_placements.node_id
        AND n.kind = 'terminal'
  )
  AND NOT EXISTS (
      SELECT 1
      FROM task_node_placements current_position
      WHERE current_position.task_id = task_node_placements.task_id
        AND current_position.state IN ('active', 'waiting_approval')
  );
