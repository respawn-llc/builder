-- +goose Up

CREATE TEMP TABLE migration_prior_origin_workflow_graphs (
    workflow_id TEXT PRIMARY KEY,
    graph_json TEXT NOT NULL
);

INSERT INTO migration_prior_origin_workflow_graphs (workflow_id, graph_json)
SELECT
    workflow.id,
    (
        SELECT COALESCE(json_group_array(json_object(
            'edge_id', graph.edge_id,
            'snapshot_priority', 0,
            'transition_key', graph.transition_key,
            'source_node_id', graph.source_node_id,
            'source_node_key', graph.source_node_key,
            'source_node_kind', graph.source_node_kind,
            'target_node_id', graph.target_node_id,
            'target_node_key', graph.target_node_key,
            'target_node_kind', graph.target_node_kind,
            'prompt_template', graph.prompt_template,
            'parameters_json', graph.parameters_json
        )), '[]')
        FROM (
            SELECT
                edge.id AS edge_id,
                transition_group.transition_id AS transition_key,
                source_node.id AS source_node_id,
                source_node.node_key AS source_node_key,
                source_node.kind AS source_node_kind,
                target_node.id AS target_node_id,
                target_node.node_key AS target_node_key,
                target_node.kind AS target_node_kind,
                edge.prompt_template,
                edge.parameters_json
            FROM workflow_edges edge
            JOIN workflow_transition_groups transition_group
                ON transition_group.id = edge.transition_group_id
            JOIN workflow_nodes source_node
                ON source_node.id = transition_group.source_node_id
            JOIN workflow_nodes target_node
                ON target_node.id = edge.target_node_id
            WHERE source_node.workflow_id = workflow.id
            ORDER BY edge.id
        ) graph
    )
FROM workflows workflow;

UPDATE task_current_nodes
SET prior_node_values_json = kent_migration_reclassify_prior_values_v1(
    task_current_nodes.task_id,
    task_current_nodes.node_id,
    task_current_nodes.transition_branch_key,
    task_current_nodes.prior_node_values_json,
    (
        SELECT graph.graph_json
        FROM task_records task
        JOIN migration_prior_origin_workflow_graphs graph
            ON graph.workflow_id = task.workflow_id
        WHERE task.id = task_current_nodes.task_id
    )
);

UPDATE task_pending_approval_branches
SET target_snapshot_json = (
    SELECT json_set(
        json_remove(task_pending_approval_branches.target_snapshot_json, '$.prior_node_values'),
        '$.prior_values',
        json(kent_migration_reclassify_prior_values_v1(
            approval.source_task_id,
            json_extract(task_pending_approval_branches.target_snapshot_json, '$.node_id'),
            json_extract(task_pending_approval_branches.target_snapshot_json, '$.transition_branch_key'),
            COALESCE(
                json_extract(task_pending_approval_branches.target_snapshot_json, '$.prior_values'),
                json_extract(task_pending_approval_branches.target_snapshot_json, '$.prior_node_values')
            ),
            graph.graph_json
        ))
    )
    FROM task_pending_approvals approval
    JOIN task_records task
        ON task.id = approval.source_task_id
    JOIN migration_prior_origin_workflow_graphs graph
        ON graph.workflow_id = task.workflow_id
    WHERE approval.id = task_pending_approval_branches.approval_id
);

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_prior_value_origins_insert
BEFORE INSERT ON task_current_nodes
FOR EACH ROW
WHEN json_type(NEW.prior_node_values_json, '$.node_outputs') IS NOT 'object'
  OR json_type(NEW.prior_node_values_json, '$.transition_parameters') IS NOT 'object'
BEGIN
    SELECT RAISE(ABORT, 'current node prior values must preserve Node output and Transition parameter origins');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_prior_value_origins_update
BEFORE UPDATE OF prior_node_values_json ON task_current_nodes
FOR EACH ROW
WHEN json_type(NEW.prior_node_values_json, '$.node_outputs') IS NOT 'object'
  OR json_type(NEW.prior_node_values_json, '$.transition_parameters') IS NOT 'object'
BEGIN
    SELECT RAISE(ABORT, 'current node prior values must preserve Node output and Transition parameter origins');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_pending_approval_branches_prior_value_origins_insert
BEFORE INSERT ON task_pending_approval_branches
FOR EACH ROW
WHEN json_type(NEW.target_snapshot_json, '$.prior_values') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_values.node_outputs') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_values.transition_parameters') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_node_values') IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'pending approval target prior values must preserve Node output and Transition parameter origins');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_pending_approval_branches_prior_value_origins_update
BEFORE UPDATE OF target_snapshot_json ON task_pending_approval_branches
FOR EACH ROW
WHEN json_type(NEW.target_snapshot_json, '$.prior_values') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_values.node_outputs') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_values.transition_parameters') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_node_values') IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'pending approval target prior values must preserve Node output and Transition parameter origins');
END;
-- +goose StatementEnd

DROP TABLE migration_prior_origin_workflow_graphs;

-- +goose Down
SELECT kent_current_node_prior_value_origins_are_irreversible();
