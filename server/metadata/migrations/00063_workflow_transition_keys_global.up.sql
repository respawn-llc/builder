-- +goose Up

CREATE TEMP TABLE migration_workflow_transition_key_repairs (
    workflow_id TEXT NOT NULL,
    group_id TEXT PRIMARY KEY,
    source_node_id TEXT NOT NULL,
    old_transition_id TEXT NOT NULL,
    new_transition_id TEXT NOT NULL
);

WITH ranked_groups AS (
    SELECT
        source_node.workflow_id,
        transition_group.id AS group_id,
        transition_group.source_node_id,
        transition_group.transition_id AS old_transition_id,
        row_number() OVER (
            PARTITION BY source_node.workflow_id, transition_group.transition_id
            ORDER BY transition_group.id
        ) AS duplicate_rank,
        count(*) OVER (
            PARTITION BY source_node.workflow_id, transition_group.transition_id
        ) AS duplicate_count
    FROM workflow_transition_groups transition_group
    JOIN workflow_nodes source_node
        ON source_node.id = transition_group.source_node_id
)
INSERT INTO migration_workflow_transition_key_repairs (
    workflow_id,
    group_id,
    source_node_id,
    old_transition_id,
    new_transition_id
)
SELECT
    workflow_id,
    group_id,
    source_node_id,
    old_transition_id,
    substr(
        old_transition_id,
        1,
        64 - length('_branch_' || duplicate_rank)
    ) || '_branch_' || duplicate_rank
FROM ranked_groups
WHERE duplicate_count > 1
  AND duplicate_rank > 1;

CREATE TEMP TABLE migration_workflow_transition_key_conflicts (
    value INTEGER NOT NULL CHECK (value = 0)
);

INSERT INTO migration_workflow_transition_key_conflicts (value)
SELECT 1
FROM migration_workflow_transition_key_repairs repair
JOIN workflow_transition_groups existing
    ON existing.transition_id = repair.new_transition_id
   AND existing.id != repair.group_id
JOIN workflow_nodes existing_source
    ON existing_source.id = existing.source_node_id
   AND existing_source.workflow_id = repair.workflow_id;

DROP TABLE migration_workflow_transition_key_conflicts;

UPDATE task_pending_approvals
SET transition_snapshot_json = json_set(
    transition_snapshot_json,
    '$.transition_id',
    (
        SELECT repair.new_transition_id
        FROM migration_workflow_transition_key_repairs repair
        WHERE repair.workflow_id = json_extract(task_pending_approvals.transition_snapshot_json, '$.workflow_id')
          AND repair.source_node_id = json_extract(task_pending_approvals.transition_snapshot_json, '$.source_node_id')
          AND repair.old_transition_id = json_extract(task_pending_approvals.transition_snapshot_json, '$.transition_id')
    )
)
WHERE EXISTS (
    SELECT 1
    FROM migration_workflow_transition_key_repairs repair
    WHERE repair.workflow_id = json_extract(task_pending_approvals.transition_snapshot_json, '$.workflow_id')
      AND repair.source_node_id = json_extract(task_pending_approvals.transition_snapshot_json, '$.source_node_id')
      AND repair.old_transition_id = json_extract(task_pending_approvals.transition_snapshot_json, '$.transition_id')
);

UPDATE task_current_nodes
SET prior_node_values_json = json_set(
    prior_node_values_json,
    '$.transition_parameters',
    (
        SELECT json_group_object(parameter.key, parameter.value)
        FROM (
            SELECT entry.key, entry.value
            FROM json_each(task_current_nodes.prior_node_values_json, '$.transition_parameters') entry
            UNION ALL
            SELECT repair.new_transition_id, entry.value
            FROM json_each(task_current_nodes.prior_node_values_json, '$.transition_parameters') entry
            JOIN migration_workflow_transition_key_repairs repair
                ON repair.workflow_id = (
                    SELECT link.workflow_id
                    FROM tasks task
                    JOIN project_workflow_links link
                        ON link.id = task.project_workflow_link_id
                    WHERE task.id = task_current_nodes.task_id
                )
               AND repair.old_transition_id = entry.key
        ) parameter
    )
)
WHERE EXISTS (
    SELECT 1
    FROM json_each(task_current_nodes.prior_node_values_json, '$.transition_parameters') entry
    JOIN migration_workflow_transition_key_repairs repair
        ON repair.workflow_id = (
            SELECT link.workflow_id
            FROM tasks task
            JOIN project_workflow_links link
                ON link.id = task.project_workflow_link_id
            WHERE task.id = task_current_nodes.task_id
        )
       AND repair.old_transition_id = entry.key
);

UPDATE task_pending_approval_branches
SET target_snapshot_json = json_set(
    target_snapshot_json,
    '$.prior_values.transition_parameters',
    (
        SELECT json_group_object(parameter.key, parameter.value)
        FROM (
            SELECT entry.key, entry.value
            FROM json_each(task_pending_approval_branches.target_snapshot_json, '$.prior_values.transition_parameters') entry
            UNION ALL
            SELECT repair.new_transition_id, entry.value
            FROM json_each(task_pending_approval_branches.target_snapshot_json, '$.prior_values.transition_parameters') entry
            JOIN migration_workflow_transition_key_repairs repair
                ON repair.workflow_id = (
                    SELECT json_extract(approval.transition_snapshot_json, '$.workflow_id')
                    FROM task_pending_approvals approval
                    WHERE approval.id = task_pending_approval_branches.approval_id
                )
               AND repair.old_transition_id = entry.key
        ) parameter
    )
)
WHERE EXISTS (
    SELECT 1
    FROM json_each(task_pending_approval_branches.target_snapshot_json, '$.prior_values.transition_parameters') entry
    JOIN migration_workflow_transition_key_repairs repair
        ON repair.old_transition_id = entry.key
);

UPDATE workflow_transition_groups
SET transition_id = (
    SELECT repair.new_transition_id
    FROM migration_workflow_transition_key_repairs repair
    WHERE repair.group_id = workflow_transition_groups.id
)
WHERE id IN (
    SELECT group_id
    FROM migration_workflow_transition_key_repairs
);

DROP TABLE migration_workflow_transition_key_repairs;

-- +goose Down
SELECT kent_workflow_transition_key_migration_is_irreversible();
