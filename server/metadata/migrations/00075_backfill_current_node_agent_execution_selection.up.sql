-- +goose Up

WITH legacy_current_node_selection AS (
    SELECT
        current_node.rowid AS current_node_rowid,
        kent_migration_current_node_agent_execution_v1(
            coalesce(edge.context_mode, ''),
            coalesce(edge.context_source_kind, ''),
            CASE WHEN current_node.session_id IS NOT NULL THEN 1 ELSE 0 END,
            CASE
                WHEN current_node.scheduling_state IS NOT NULL
                 AND current_node.scheduling_state != 'ready'
                 AND session.task_id = current_node.task_id
                 AND EXISTS (
                     SELECT 1
                     FROM session_workflow_node_associations association
                     WHERE association.session_id = current_node.session_id
                       AND association.node_id = current_node.node_id
                       AND (
                           (association.transition_branch_key IS NULL AND current_node.transition_branch_key IS NULL)
                           OR association.transition_branch_key = current_node.transition_branch_key
                       )
                 )
                THEN 1
                ELSE 0
            END,
            node.subagent_role,
            json_extract(session.continuation_json, '$.agent_role')
        ) AS selection_json
    FROM task_current_nodes current_node
    JOIN workflow_nodes node ON node.id = current_node.node_id
    LEFT JOIN workflow_edges edge ON edge.id = current_node.entered_by_edge_id
    LEFT JOIN sessions session ON session.id = current_node.session_id
    WHERE node.kind = 'agent'
      AND (current_node.effective_assignee IS NULL OR current_node.assignee_origin IS NULL)
),
resolved_current_node_selection AS (
    SELECT
        current_node_rowid,
        json_extract(selection_json, '$.assignee') AS assignee,
        json_extract(selection_json, '$.origin') AS origin,
        json_extract(selection_json, '$.thinking') AS thinking
    FROM legacy_current_node_selection
)
UPDATE task_current_nodes
SET effective_assignee = (
        SELECT assignee
        FROM resolved_current_node_selection
        WHERE resolved_current_node_selection.current_node_rowid = task_current_nodes.rowid
    ),
    effective_thinking = (
        SELECT thinking
        FROM resolved_current_node_selection
        WHERE resolved_current_node_selection.current_node_rowid = task_current_nodes.rowid
    ),
    assignee_origin = (
        SELECT origin
        FROM resolved_current_node_selection
        WHERE resolved_current_node_selection.current_node_rowid = task_current_nodes.rowid
    )
WHERE rowid IN (SELECT current_node_rowid FROM resolved_current_node_selection);

WITH legacy_approval_target_selection AS (
    SELECT
        branch.rowid AS branch_rowid,
        json_extract(branch.target_snapshot_json, '$.node_id') AS node_id,
        node.kind AS node_kind,
        branch.target_snapshot_json AS target_snapshot_json,
        branch.effective_edge_configuration_json AS edge_snapshot_json,
        kent_migration_current_node_agent_execution_v1(
            coalesce(json_extract(branch.effective_edge_configuration_json, '$.context_mode'), ''),
            coalesce(json_extract(branch.effective_edge_configuration_json, '$.context_source.kind'), ''),
            CASE WHEN json_extract(branch.target_snapshot_json, '$.session_id') IS NOT NULL THEN 1 ELSE 0 END,
            0,
            node.subagent_role,
            json_extract(session.continuation_json, '$.agent_role')
        ) AS selection_json
    FROM task_pending_approval_branches branch
    JOIN workflow_nodes node
      ON node.id = json_extract(branch.target_snapshot_json, '$.node_id')
    LEFT JOIN sessions session
      ON session.id = json_extract(branch.target_snapshot_json, '$.session_id')
),
resolved_approval_target_selection AS (
    SELECT
        branch_rowid,
        node_kind,
        target_snapshot_json,
        edge_snapshot_json,
        selection_json
    FROM legacy_approval_target_selection
)
UPDATE task_pending_approval_branches
SET target_snapshot_json = (
        SELECT CASE
            WHEN node_kind = 'agent' THEN json_set(
                target_snapshot_json,
                '$.node_kind', node_kind,
                '$.agent_execution_selection', json(selection_json)
            )
            ELSE json_remove(
                json_set(target_snapshot_json, '$.node_kind', node_kind),
                '$.agent_execution_selection'
            )
        END
        FROM resolved_approval_target_selection
        WHERE resolved_approval_target_selection.branch_rowid = task_pending_approval_branches.rowid
    ),
    effective_edge_configuration_json = (
        SELECT json_set(
            edge_snapshot_json,
            '$.assignee_selection', 'configured',
            '$.thinking_selection', 'configured'
        )
        FROM resolved_approval_target_selection
        WHERE resolved_approval_target_selection.branch_rowid = task_pending_approval_branches.rowid
    )
WHERE rowid IN (SELECT branch_rowid FROM resolved_approval_target_selection);

SELECT kent_migration_current_node_agent_execution_validation_v1(
    node.kind,
    current_node.effective_assignee,
    current_node.effective_thinking,
    current_node.assignee_origin
)
FROM task_current_nodes current_node
JOIN workflow_nodes node ON node.id = current_node.node_id;

SELECT kent_migration_pending_approval_agent_execution_validation_v1(
    branch.target_snapshot_json
)
FROM task_pending_approval_branches branch;
