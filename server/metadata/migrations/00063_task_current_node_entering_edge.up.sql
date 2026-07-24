-- +goose Up

ALTER TABLE task_current_nodes
ADD COLUMN entered_by_edge_id TEXT REFERENCES workflow_edges(id) ON DELETE RESTRICT;

UPDATE task_current_nodes
SET entered_by_edge_id = (
    SELECT transition_edge.workflow_edge_id
    FROM task_node_placements placement
    JOIN task_transition_edges transition_edge
      ON transition_edge.target_placement_id = placement.id
    WHERE placement.task_id = task_current_nodes.task_id
      AND placement.node_id = task_current_nodes.node_id
      AND (
          (placement.parallel_branch_edge_id IS NULL AND task_current_nodes.transition_branch_key IS NULL)
          OR (
              placement.parallel_branch_edge_id IS NOT NULL
              AND EXISTS (
                  SELECT 1
                  FROM workflow_edges branch
                  WHERE branch.id = placement.parallel_branch_edge_id
                    AND branch.edge_key = task_current_nodes.transition_branch_key
              )
          )
      )
    ORDER BY transition_edge.rowid DESC
    LIMIT 1
)
WHERE entered_by_edge_id IS NULL;

CREATE TABLE migration_current_node_entering_edge_errors (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER migration_current_node_entering_edge_fail
BEFORE INSERT ON migration_current_node_entering_edge_errors
BEGIN
    SELECT RAISE(ABORT, 'current node entering edge migration failure: executable current node has no resolvable entering transition edge');
END;
-- +goose StatementEnd

INSERT INTO migration_current_node_entering_edge_errors (task_id, node_id)
SELECT current_node.task_id, current_node.node_id
FROM task_current_nodes current_node
JOIN workflow_nodes node ON node.id = current_node.node_id
WHERE node.kind IN ('agent', 'script')
  AND current_node.entered_by_edge_id IS NULL;

DROP TRIGGER migration_current_node_entering_edge_fail;
DROP TABLE migration_current_node_entering_edge_errors;
