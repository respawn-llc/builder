-- +goose Up

UPDATE workflow_edges
SET input_bindings_json = json_array()
WHERE json_type(input_bindings_json) = 'object'
  AND NOT EXISTS (
      SELECT 1
      FROM json_each(workflow_edges.input_bindings_json)
  );
