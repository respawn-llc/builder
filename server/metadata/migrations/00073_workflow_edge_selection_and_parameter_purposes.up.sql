-- +goose Up

ALTER TABLE workflow_edges
    ADD COLUMN assignee_selection TEXT NOT NULL DEFAULT 'configured'
        CHECK (assignee_selection IN ('configured', 'previous_node'));

ALTER TABLE workflow_edges
    ADD COLUMN thinking_selection TEXT NOT NULL DEFAULT 'configured'
        CHECK (thinking_selection IN ('configured', 'previous_node'));

UPDATE workflow_edges
SET parameters_json = (
    SELECT json_group_array(
        json_set(json(value), '$.purpose', 'ordinary')
    )
    FROM json_each(workflow_edges.parameters_json)
);

UPDATE task_pending_approval_branches
SET effective_edge_configuration_json = json_set(
    effective_edge_configuration_json,
    '$.parameters',
    (
        SELECT json_group_array(
            json_set(json(value), '$.purpose', 'ordinary')
        )
        FROM json_each(
            CASE
                WHEN json_type(effective_edge_configuration_json, '$.parameters') = 'array'
                    THEN json_extract(effective_edge_configuration_json, '$.parameters')
                ELSE '[]'
            END
        )
    )
)
WHERE json_type(effective_edge_configuration_json, '$.parameters') = 'array';
