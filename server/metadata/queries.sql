



-- name: ListWorkspaceBindingsByCanonicalRoot :many
SELECT
    p.id AS project_id,
    p.display_name AS project_display_name,
    p.project_key,
    w.id AS workspace_id,
    w.canonical_root_path AS workspace_root
FROM workspaces w
JOIN projects p ON p.id = w.project_id
WHERE w.canonical_root_path = sqlc.arg(canonical_root_path)
ORDER BY p.created_at_unix_ms ASC, p.rowid ASC, w.created_at_unix_ms ASC, w.rowid ASC;

-- name: GetWorkspaceBindingByProjectAndCanonicalRoot :one
SELECT
    p.id AS project_id,
    p.display_name AS project_display_name,
    p.project_key,
    w.id AS workspace_id,
    w.canonical_root_path AS workspace_root
FROM workspaces w
JOIN projects p ON p.id = w.project_id
WHERE w.project_id = sqlc.arg(project_id)
  AND w.canonical_root_path = sqlc.arg(canonical_root_path)
LIMIT 1;

-- name: ListWorkspacesByCanonicalRoot :many
SELECT
    id,
    project_id,
    canonical_root_path,
    git_metadata_json,
    created_at_unix_ms,
    updated_at_unix_ms,
    chat_draft_json
FROM workspaces
WHERE canonical_root_path = sqlc.arg(canonical_root_path)
ORDER BY created_at_unix_ms ASC, rowid ASC;

-- name: GetWorkspaceByID :one
SELECT
    id,
    project_id,
    canonical_root_path,
    git_metadata_json,
    created_at_unix_ms,
    updated_at_unix_ms,
    chat_draft_json
FROM workspaces
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: GetWorkspaceChatDraft :one
SELECT
    chat_draft_json
FROM workspaces
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: ReplaceWorkspaceChatDraft :execrows
UPDATE workspaces
SET chat_draft_json = sqlc.arg(chat_draft_json)
WHERE id = sqlc.arg(id);

-- name: ListProjectKeyRows :many
SELECT
    id,
    display_name,
    project_key
FROM projects
ORDER BY created_at_unix_ms ASC, rowid ASC;

-- name: GetProjectKeyState :one
SELECT
    p.id,
    p.display_name,
    p.project_key,
    p.next_task_seq,
    CAST(COALESCE(COUNT(t.id), 0) AS INTEGER) AS task_count
FROM projects p
LEFT JOIN task_records t ON t.project_id = p.id
WHERE p.id = sqlc.arg(project_id)
GROUP BY p.id, p.display_name, p.project_key, p.next_task_seq
LIMIT 1;

-- name: SetProjectKey :execrows
UPDATE projects
SET
    project_key = sqlc.arg(project_key),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(project_id);

-- name: InsertProjectLabel :one
INSERT INTO project_labels (
    id,
    project_id,
    name,
    created_at_unix_ms,
    updated_at_unix_ms,
    ordinal
) SELECT
    sqlc.arg(id),
    sqlc.arg(project_id),
    sqlc.arg(name),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms),
    sqlc.arg(ordinal)
FROM projects
WHERE projects.id = sqlc.arg(project_id)
  AND (
      SELECT COUNT(*)
      FROM project_labels
      WHERE project_labels.project_id = sqlc.arg(project_id)
  ) < CAST(sqlc.arg(catalog_limit) AS INTEGER)
RETURNING id, project_id, name, ordinal;

-- name: ListProjectLabels :many
SELECT id, project_id, name, ordinal
FROM project_labels
WHERE project_id = sqlc.arg(project_id)
ORDER BY ordinal ASC
LIMIT 101;

-- name: SetProjectLabelOrdinal :exec
UPDATE project_labels
SET ordinal = sqlc.arg(ordinal)
WHERE id = sqlc.arg(id)
  AND project_id = sqlc.arg(project_id);

-- name: MoveProjectLabelOrdinalsToTemporaryBand :exec
UPDATE project_labels
SET ordinal = ordinal + CAST(sqlc.arg(temporary_band_offset) AS INTEGER)
WHERE project_id = sqlc.arg(project_id);

-- name: RenameProjectLabel :one
UPDATE project_labels
SET
    name = sqlc.arg(name),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id)
  AND project_id = sqlc.arg(project_id)
RETURNING id, project_id, name, ordinal;

-- name: DeleteProjectLabel :one
DELETE FROM project_labels
WHERE id = sqlc.arg(id)
  AND project_id = sqlc.arg(project_id)
RETURNING id, project_id, name, ordinal;

-- name: ListTaskAssignedLabelIDsByTasks :many
SELECT tla.task_id, pl.id AS label_id
FROM task_label_assignments tla
JOIN project_labels pl ON pl.id = tla.label_id
WHERE tla.task_id IN (sqlc.slice('task_ids'))
ORDER BY tla.task_id ASC, pl.ordinal ASC, pl.id ASC;

-- name: ListProjectLabelsByIDs :many
SELECT id, project_id, name, ordinal
FROM project_labels
WHERE id IN (sqlc.slice('label_ids'))
ORDER BY ordinal ASC, id ASC;

-- name: InsertTaskLabelAssignment :exec
INSERT INTO task_label_assignments (task_id, label_id)
VALUES (sqlc.arg(task_id), sqlc.arg(label_id))
ON CONFLICT(task_id, label_id) DO NOTHING;

-- name: AcquireTaskLabelWriteLock :one
UPDATE projects
SET updated_at_unix_ms = updated_at_unix_ms
WHERE id = (
    SELECT task_records.project_id
    FROM task_records
    WHERE task_records.id = sqlc.arg(task_id)
)
RETURNING id;

-- name: DeleteTaskLabelAssignment :execrows
DELETE FROM task_label_assignments
WHERE task_id = sqlc.arg(task_id)
  AND label_id = sqlc.arg(label_id);

-- name: AllocateProjectTaskSequence :one
UPDATE projects
SET
    next_task_seq = next_task_seq + 1,
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(project_id)
RETURNING project_key, next_task_seq;

-- name: InsertWorkflow :exec
INSERT INTO workflows (
    id,
    name,
    description,
    version,
    execution_target_policy,
    execution_target_custom_ref,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES (
    sqlc.arg(id),
    sqlc.arg(name),
    sqlc.arg(description),
    sqlc.arg(version),
    sqlc.arg(execution_target_policy),
    sqlc.narg(execution_target_custom_ref),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms)
);

-- name: UpdateWorkflowInfo :execrows
UPDATE workflows
SET
    name = sqlc.arg(name),
    description = sqlc.arg(description),
    version = version + 1,
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: UpdateWorkflowInfoWithoutVersion :execrows
UPDATE workflows
SET
    name = sqlc.arg(name),
    description = sqlc.arg(description),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: UpdateWorkflowMetadata :execrows
UPDATE workflows
SET
    name = sqlc.arg(name),
    description = sqlc.arg(description),
    execution_target_policy = sqlc.arg(execution_target_policy),
    execution_target_custom_ref = sqlc.narg(execution_target_custom_ref),
    version = version + 1,
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: UpdateWorkflowMetadataWithoutVersion :execrows
UPDATE workflows
SET
    name = sqlc.arg(name),
    description = sqlc.arg(description),
    execution_target_policy = sqlc.arg(execution_target_policy),
    execution_target_custom_ref = sqlc.narg(execution_target_custom_ref),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: IncrementWorkflowVersion :one
UPDATE workflows
SET
    version = version + 1,
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id)
RETURNING version;

-- name: AcquireWorkflowGraphSaveWriteLock :execrows
UPDATE workflows
SET updated_at_unix_ms = updated_at_unix_ms
WHERE id = sqlc.arg(id);

-- name: GetWorkflow :one
SELECT
    id,
    name,
    description,
    version,
    execution_target_policy,
    execution_target_custom_ref,
    created_at_unix_ms,
    updated_at_unix_ms
FROM workflows
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: ListWorkflows :many
SELECT
    id,
    name,
    description,
    version,
    execution_target_policy,
    execution_target_custom_ref,
    created_at_unix_ms,
    updated_at_unix_ms
FROM workflows
ORDER BY updated_at_unix_ms DESC, rowid DESC;

-- name: ListWorkflowRecordsPage :many
SELECT
    workflows.id,
    workflows.name,
    workflows.description,
    workflows.version,
    workflows.execution_target_policy,
    workflows.execution_target_custom_ref,
    workflows.created_at_unix_ms,
    workflows.updated_at_unix_ms,
    CAST(MAX(
        workflows.updated_at_unix_ms,
        COALESCE((
            SELECT MAX(task_records.updated_at_unix_ms)
            FROM task_records
            WHERE task_records.workflow_id = workflows.id
        ), workflows.updated_at_unix_ms)
    ) AS INTEGER) AS global_activity_at_unix_ms,
    project_latest_task.updated_at_unix_ms AS project_activity_at_unix_ms,
    project_link.is_default AS project_link_default,
    lower(workflows.name) AS project_name_order_key
FROM workflows
LEFT JOIN project_workflow_link_records project_link
    ON project_link.project_id = sqlc.narg(project_id)
   AND project_link.workflow_id = workflows.id
LEFT JOIN tasks project_latest_task
    ON project_latest_task.id = (
        SELECT latest_task.id
        FROM tasks latest_task INDEXED BY tasks_project_workflow_link_updated_idx
        WHERE latest_task.project_workflow_link_id = project_link.id
        ORDER BY latest_task.updated_at_unix_ms DESC, latest_task.id DESC
        LIMIT 1
    )
WHERE workflows.id = COALESCE(sqlc.narg(workflow_id), workflows.id)
  AND (
      sqlc.arg(search_query) = ''
      OR lower(workflows.name) LIKE '%' || lower(sqlc.arg(search_query)) || '%'
      OR lower(workflows.description) LIKE '%' || lower(sqlc.arg(search_query)) || '%'
  )
  AND (
      sqlc.narg(project_id) IS NULL
      OR project_link.id IS NOT NULL
  )
ORDER BY
    project_link_default DESC,
    CASE
        WHEN project_link_default IS NULL THEN global_activity_at_unix_ms
        ELSE project_activity_at_unix_ms
    END DESC,
    CASE WHEN project_link_default IS NOT NULL THEN lower(workflows.name) END ASC,
    CASE WHEN project_link_default IS NULL THEN workflows.id END DESC,
    CASE WHEN project_link_default IS NOT NULL THEN workflows.id END ASC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: InsertWorkflowNode :exec
INSERT INTO workflow_nodes (
    id,
    workflow_id,
    node_key,
    kind,
    display_name,
    subagent_role,
    completion_mode,
    script_path,
    join_input_providers_json,
    group_id,
    sort_order
) VALUES (
    sqlc.arg(id),
    sqlc.arg(workflow_id),
    sqlc.arg(node_key),
    sqlc.arg(kind),
    sqlc.arg(display_name),
    sqlc.arg(subagent_role),
    sqlc.arg(completion_mode),
    sqlc.narg(script_path),
    sqlc.arg(join_input_providers_json),
    sqlc.narg(group_id),
    sqlc.arg(sort_order)
);

-- name: InsertWorkflowNodeGroup :exec
INSERT INTO workflow_node_groups (
    id,
    workflow_id,
    group_key,
    display_name,
    sort_order
) VALUES (
    sqlc.arg(id),
    sqlc.arg(workflow_id),
    sqlc.arg(group_key),
    sqlc.arg(display_name),
    sqlc.arg(sort_order)
);

-- name: UpdateWorkflowNodeGroup :execrows
UPDATE workflow_node_groups
SET
    group_key = sqlc.arg(group_key),
    display_name = sqlc.arg(display_name),
    sort_order = sqlc.arg(sort_order)
WHERE id = sqlc.arg(id)
  AND workflow_id = sqlc.arg(workflow_id);

-- name: DeleteWorkflowNodeGroup :execrows
DELETE FROM workflow_node_groups
WHERE id = sqlc.arg(id)
  AND workflow_id = sqlc.arg(workflow_id);

-- name: DeleteWorkflowTransitionGroupByID :execrows
DELETE FROM workflow_transition_groups
WHERE id = sqlc.arg(id);

-- name: UpsertWorkflowNodeGroup :execrows
INSERT INTO workflow_node_groups (id, workflow_id, group_key, display_name, sort_order)
VALUES (
    sqlc.arg(id),
    sqlc.arg(workflow_id),
    sqlc.arg(group_key),
    sqlc.arg(display_name),
    sqlc.arg(sort_order)
)
ON CONFLICT(id) DO UPDATE SET
    group_key = excluded.group_key,
    display_name = excluded.display_name,
    sort_order = excluded.sort_order
WHERE workflow_node_groups.workflow_id = excluded.workflow_id;

-- name: UpsertWorkflowNode :execrows
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, subagent_role, completion_mode, script_path, join_input_providers_json, group_id, sort_order)
VALUES (
    sqlc.arg(id),
    sqlc.arg(workflow_id),
    sqlc.arg(node_key),
    sqlc.arg(kind),
    sqlc.arg(display_name),
    sqlc.arg(subagent_role),
    sqlc.arg(completion_mode),
    sqlc.narg(script_path),
    sqlc.arg(join_input_providers_json),
    sqlc.narg(group_id),
    sqlc.arg(sort_order)
)
ON CONFLICT(id) DO UPDATE SET
    node_key = excluded.node_key,
    kind = excluded.kind,
    display_name = excluded.display_name,
    subagent_role = excluded.subagent_role,
    completion_mode = excluded.completion_mode,
    script_path = excluded.script_path,
    join_input_providers_json = excluded.join_input_providers_json,
    group_id = excluded.group_id,
    sort_order = excluded.sort_order
WHERE workflow_nodes.workflow_id = excluded.workflow_id;

-- name: UpsertWorkflowTransitionGroup :execrows
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name, description, sort_order)
SELECT
    sqlc.arg(id),
    sqlc.arg(source_node_id),
    sqlc.arg(transition_id),
    sqlc.arg(display_name),
    sqlc.arg(description),
    sqlc.arg(sort_order)
WHERE EXISTS (
    SELECT 1
    FROM workflow_nodes source
    WHERE source.id = sqlc.arg(source_node_id)
      AND source.workflow_id = sqlc.arg(workflow_id)
)
ON CONFLICT(id) DO UPDATE SET
    source_node_id = excluded.source_node_id,
    transition_id = excluded.transition_id,
    display_name = excluded.display_name,
    description = excluded.description,
    sort_order = excluded.sort_order
WHERE EXISTS (
    SELECT 1
    FROM workflow_nodes source
    WHERE source.id = workflow_transition_groups.source_node_id
      AND source.workflow_id = (
          SELECT new_source.workflow_id
          FROM workflow_nodes new_source
          WHERE new_source.id = excluded.source_node_id
      )
);

-- name: UpsertWorkflowEdge :execrows
INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, assignee_selection, thinking_selection, requires_approval, context_mode, context_source_kind, context_source_node_key, prompt_template, parameters_json, input_bindings_json, output_requirements_json, sort_order)
SELECT
    sqlc.arg(id),
    sqlc.arg(transition_group_id),
    sqlc.arg(edge_key),
    sqlc.arg(target_node_id),
    sqlc.arg(assignee_selection),
    sqlc.arg(thinking_selection),
    sqlc.arg(requires_approval),
    sqlc.arg(context_mode),
    sqlc.arg(context_source_kind),
    sqlc.arg(context_source_node_key),
    sqlc.arg(prompt_template),
    sqlc.arg(parameters_json),
    sqlc.arg(input_bindings_json),
    sqlc.arg(output_requirements_json),
    sqlc.arg(sort_order)
WHERE EXISTS (
    SELECT 1
    FROM workflow_transition_groups tg
    JOIN workflow_nodes source ON source.id = tg.source_node_id
    JOIN workflow_nodes target ON target.id = sqlc.arg(target_node_id)
    WHERE tg.id = sqlc.arg(transition_group_id)
      AND source.workflow_id = sqlc.arg(workflow_id)
      AND target.workflow_id = sqlc.arg(workflow_id)
)
ON CONFLICT(id) DO UPDATE SET
    transition_group_id = excluded.transition_group_id,
    edge_key = excluded.edge_key,
    target_node_id = excluded.target_node_id,
    assignee_selection = excluded.assignee_selection,
    thinking_selection = excluded.thinking_selection,
    requires_approval = excluded.requires_approval,
    context_mode = excluded.context_mode,
    context_source_kind = excluded.context_source_kind,
    context_source_node_key = excluded.context_source_node_key,
    prompt_template = excluded.prompt_template,
    parameters_json = excluded.parameters_json,
    input_bindings_json = excluded.input_bindings_json,
    output_requirements_json = excluded.output_requirements_json,
    sort_order = excluded.sort_order
WHERE EXISTS (
    SELECT 1
    FROM workflow_transition_groups tg
    JOIN workflow_nodes source ON source.id = tg.source_node_id
    WHERE tg.id = workflow_edges.transition_group_id
      AND source.workflow_id = (
          SELECT new_source.workflow_id
          FROM workflow_transition_groups new_tg
          JOIN workflow_nodes new_source ON new_source.id = new_tg.source_node_id
          WHERE new_tg.id = excluded.transition_group_id
      )
);

-- name: ListWorkflowNodeGroups :many
SELECT
    id,
    workflow_id,
    group_key,
    display_name,
    sort_order
FROM workflow_node_groups
WHERE workflow_id = sqlc.arg(workflow_id)
ORDER BY sort_order ASC, rowid ASC;

-- name: GetWorkflowNodeGroupByKey :one
SELECT
    id,
    workflow_id,
    group_key,
    display_name,
    sort_order
FROM workflow_node_groups
WHERE workflow_id = sqlc.arg(workflow_id)
  AND group_key = sqlc.arg(group_key)
LIMIT 1;

-- name: GetWorkflowNodeGroupByID :one
SELECT
    id,
    workflow_id,
    group_key,
    display_name,
    sort_order
FROM workflow_node_groups
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: ListWorkflowNodes :many
SELECT
    id,
    workflow_id,
    node_key,
    kind,
    display_name,
    subagent_role,
    completion_mode,
    script_path,
    join_input_providers_json,
    group_id,
    sort_order
FROM workflow_nodes
WHERE workflow_id = sqlc.arg(workflow_id)
ORDER BY sort_order ASC, rowid ASC;

-- name: GetWorkflowNode :one
SELECT
    id,
    workflow_id,
    node_key,
    kind,
    display_name,
    subagent_role,
    completion_mode,
    script_path,
    join_input_providers_json,
    group_id,
    sort_order
FROM workflow_nodes
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: DeleteWorkflowNode :execrows
DELETE FROM workflow_nodes
WHERE id = sqlc.arg(id);

-- name: CountWorkflowNodesByGroup :one
SELECT CAST(COUNT(*) AS INTEGER) AS node_count
FROM workflow_nodes
WHERE group_id = sqlc.arg(group_id);

-- name: InsertWorkflowTransitionGroup :exec
INSERT INTO workflow_transition_groups (
    id,
    source_node_id,
    transition_id,
    display_name,
    description,
    sort_order
) VALUES (
    sqlc.arg(id),
    sqlc.arg(source_node_id),
    sqlc.arg(transition_id),
    sqlc.arg(display_name),
    sqlc.arg(description),
    sqlc.arg(sort_order)
);

-- name: ListWorkflowTransitionGroups :many
SELECT
    tg.id,
    source.workflow_id AS workflow_id,
    tg.source_node_id,
    tg.transition_id,
    tg.display_name,
    tg.description,
    tg.sort_order
FROM workflow_transition_groups tg
JOIN workflow_nodes source ON source.id = tg.source_node_id
WHERE source.workflow_id = sqlc.arg(workflow_id)
ORDER BY tg.sort_order ASC, tg.rowid ASC;

-- name: InsertWorkflowEdge :exec
INSERT INTO workflow_edges (
    id,
    transition_group_id,
    edge_key,
    target_node_id,
    assignee_selection,
    thinking_selection,
    requires_approval,
    context_mode,
    context_source_kind,
    context_source_node_key,
    prompt_template,
    parameters_json,
    input_bindings_json,
    output_requirements_json,
    sort_order
) VALUES (
    sqlc.arg(id),
    sqlc.arg(transition_group_id),
    sqlc.arg(edge_key),
    sqlc.arg(target_node_id),
    sqlc.arg(assignee_selection),
    sqlc.arg(thinking_selection),
    sqlc.arg(requires_approval),
    sqlc.arg(context_mode),
    sqlc.arg(context_source_kind),
    sqlc.arg(context_source_node_key),
    sqlc.arg(prompt_template),
    sqlc.arg(parameters_json),
    sqlc.arg(input_bindings_json),
    sqlc.arg(output_requirements_json),
    sqlc.arg(sort_order)
);

-- name: ListWorkflowEdges :many
SELECT
    e.id,
    source.workflow_id AS workflow_id,
    e.transition_group_id,
    e.edge_key,
    e.target_node_id,
    e.assignee_selection,
    e.thinking_selection,
    e.requires_approval,
    e.context_mode,
    e.context_source_kind,
    e.context_source_node_key,
    e.prompt_template,
    e.parameters_json,
    e.input_bindings_json,
    e.output_requirements_json,
    e.sort_order
FROM workflow_edges e
JOIN workflow_transition_groups tg ON tg.id = e.transition_group_id
JOIN workflow_nodes source ON source.id = tg.source_node_id
WHERE source.workflow_id = sqlc.arg(workflow_id)
ORDER BY e.sort_order ASC, e.rowid ASC;

-- name: GetWorkflowEdge :one
SELECT
    e.id,
    source.workflow_id AS workflow_id,
    e.transition_group_id,
    e.edge_key,
    e.target_node_id,
    e.assignee_selection,
    e.thinking_selection,
    e.requires_approval,
    e.context_mode,
    e.context_source_kind,
    e.context_source_node_key,
    e.prompt_template,
    e.parameters_json,
    e.input_bindings_json,
    e.output_requirements_json,
    e.sort_order
FROM workflow_edges e
JOIN workflow_transition_groups tg ON tg.id = e.transition_group_id
JOIN workflow_nodes source ON source.id = tg.source_node_id
WHERE e.id = sqlc.arg(id)
LIMIT 1;

-- name: DeleteWorkflowEdge :execrows
DELETE FROM workflow_edges
WHERE id = sqlc.arg(id);

-- name: UpdateWorkflowNode :execrows
UPDATE workflow_nodes
SET
    node_key = sqlc.arg(node_key),
    kind = sqlc.arg(kind),
    display_name = sqlc.arg(display_name),
    subagent_role = sqlc.arg(subagent_role),
    completion_mode = sqlc.arg(completion_mode),
    script_path = sqlc.narg(script_path),
    join_input_providers_json = sqlc.arg(join_input_providers_json),
    group_id = sqlc.narg(group_id)
WHERE id = sqlc.arg(id)
  AND workflow_id = sqlc.arg(workflow_id);

-- name: UpdateWorkflowTransitionGroup :execrows
UPDATE workflow_transition_groups
SET
    source_node_id = sqlc.arg(source_node_id),
    transition_id = sqlc.arg(transition_id),
    display_name = sqlc.arg(display_name),
    description = sqlc.arg(description)
WHERE workflow_transition_groups.id = sqlc.arg(transition_group_id)
  AND (
      SELECT source.workflow_id
      FROM workflow_transition_groups existing
      JOIN workflow_nodes source ON source.id = existing.source_node_id
      WHERE existing.id = sqlc.arg(transition_group_id)
  ) = sqlc.arg(workflow_id)
  AND EXISTS (
      SELECT 1
      FROM workflow_nodes new_source
      WHERE new_source.id = sqlc.arg(source_node_id)
        AND new_source.workflow_id = sqlc.arg(workflow_id)
  );

-- name: UpdateWorkflowEdge :execrows
UPDATE workflow_edges
SET
    transition_group_id = sqlc.arg(transition_group_id),
    edge_key = sqlc.arg(edge_key),
    target_node_id = sqlc.arg(target_node_id),
    assignee_selection = sqlc.arg(assignee_selection),
    thinking_selection = sqlc.arg(thinking_selection),
    requires_approval = sqlc.arg(requires_approval),
    context_mode = sqlc.arg(context_mode),
    context_source_kind = sqlc.arg(context_source_kind),
    context_source_node_key = sqlc.arg(context_source_node_key),
    prompt_template = sqlc.arg(prompt_template),
    parameters_json = sqlc.arg(parameters_json),
    input_bindings_json = sqlc.arg(input_bindings_json),
    output_requirements_json = sqlc.arg(output_requirements_json)
WHERE workflow_edges.id = sqlc.arg(edge_id)
  AND (
      SELECT source.workflow_id
      FROM workflow_edges existing
      JOIN workflow_transition_groups tg ON tg.id = existing.transition_group_id
      JOIN workflow_nodes source ON source.id = tg.source_node_id
      WHERE existing.id = sqlc.arg(edge_id)
  ) = sqlc.arg(workflow_id)
  AND EXISTS (
      SELECT 1
      FROM workflow_transition_groups new_tg
      JOIN workflow_nodes new_source ON new_source.id = new_tg.source_node_id
      JOIN workflow_nodes target ON target.id = sqlc.arg(target_node_id)
      WHERE new_tg.id = sqlc.arg(transition_group_id)
        AND new_source.workflow_id = sqlc.arg(workflow_id)
        AND target.workflow_id = sqlc.arg(workflow_id)
  );

-- name: ClearProjectDefaultWorkflowLinks :exec
UPDATE projects
SET
    default_project_workflow_link_id = NULL,
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(project_id);

-- name: InsertProjectWorkflowLink :exec
INSERT INTO project_workflow_links (
    id,
    project_id,
    workflow_id,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES (
    sqlc.arg(id),
    sqlc.arg(project_id),
    sqlc.arg(workflow_id),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms)
);

-- name: GetProjectWorkflowLink :one
SELECT
    id,
    project_id,
    workflow_id,
    is_default,
    created_at_unix_ms,
    updated_at_unix_ms
FROM project_workflow_link_records
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: GetDefaultProjectWorkflowLink :one
SELECT
    id,
    project_id,
    workflow_id,
    is_default,
    created_at_unix_ms,
    updated_at_unix_ms
FROM project_workflow_link_records
WHERE project_id = sqlc.arg(project_id)
  AND is_default = 1
LIMIT 1;

-- name: GetActiveProjectWorkflowLinkByWorkflow :one
SELECT
    id,
    project_id,
    workflow_id,
    is_default,
    created_at_unix_ms,
    updated_at_unix_ms
FROM project_workflow_link_records
WHERE project_id = sqlc.arg(project_id)
  AND workflow_id = sqlc.arg(workflow_id)
LIMIT 1;

-- name: ListProjectWorkflowLinksForTaskSelection :many
SELECT
    id,
    project_id,
    workflow_id,
    is_default,
    created_at_unix_ms,
    updated_at_unix_ms
FROM project_workflow_link_records
WHERE project_id = sqlc.arg(project_id)
ORDER BY created_at_unix_ms ASC, id ASC
LIMIT 2;

-- name: ListProjectWorkflowLinks :many
SELECT
    id,
    project_id,
    workflow_id,
    is_default,
    created_at_unix_ms,
    updated_at_unix_ms
FROM project_workflow_link_records
WHERE project_id = sqlc.arg(project_id)
ORDER BY is_default DESC, created_at_unix_ms ASC, id ASC;

-- name: ListWorkflowProjectLinks :many
SELECT
    id,
    project_id,
    workflow_id,
    is_default,
    created_at_unix_ms,
    updated_at_unix_ms
FROM project_workflow_link_records
WHERE workflow_id = sqlc.arg(workflow_id)
ORDER BY project_id ASC, is_default DESC, created_at_unix_ms ASC;

-- name: CountActiveProjectWorkflowLinks :one
SELECT CAST(COUNT(*) AS INTEGER) AS active_link_count
FROM project_workflow_links
WHERE project_id = sqlc.arg(project_id);

-- name: CountTasksByProjectWorkflowLink :one
SELECT CAST(COUNT(*) AS INTEGER) AS task_count
FROM tasks
WHERE project_workflow_link_id = sqlc.arg(project_workflow_link_id);

-- name: ListProjectWorkflowLinkTaskReferences :many
SELECT
    id,
    short_id,
    title
FROM tasks
WHERE project_workflow_link_id = sqlc.arg(project_workflow_link_id)
ORDER BY updated_at_unix_ms DESC, id ASC
LIMIT sqlc.arg(limit);

-- name: CountNonTerminalTasksByProjectWorkflowLink :one
SELECT CAST(COUNT(DISTINCT t.id) AS INTEGER) AS task_count
FROM tasks t
JOIN task_current_nodes current_node ON current_node.task_id = t.id
JOIN workflow_nodes node ON node.id = current_node.node_id
WHERE t.project_workflow_link_id = sqlc.arg(project_workflow_link_id)
  AND node.kind != 'terminal';

-- name: CountNonTerminalTasksByWorkflow :one
SELECT CAST(COUNT(DISTINCT t.id) AS INTEGER) AS task_count
FROM task_records t
JOIN task_current_nodes current_node ON current_node.task_id = t.id
JOIN workflow_nodes node ON node.id = current_node.node_id
WHERE t.workflow_id = sqlc.arg(workflow_id)
  AND node.kind != 'terminal';

-- name: DeleteProjectWorkflowLink :execrows
DELETE FROM project_workflow_links
WHERE id = sqlc.arg(id);

-- name: SetProjectDefaultWorkflowLink :execrows
UPDATE projects
SET
    default_project_workflow_link_id = sqlc.arg(project_workflow_link_id),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(project_id);

-- name: CountProjectWorkflowLinksByIDAndProject :one
SELECT CAST(COUNT(*) AS INTEGER) AS link_count
FROM project_workflow_links
WHERE id = sqlc.arg(project_workflow_link_id)
  AND project_id = sqlc.arg(project_id);

-- name: DeleteProjectWorkflowLinkUnlessDefaultNeedsReplacement :execrows
DELETE FROM project_workflow_links
WHERE project_workflow_links.id = sqlc.arg(id)
  AND NOT (
      EXISTS (
          SELECT 1
          FROM projects p
          WHERE p.id = project_workflow_links.project_id
            AND p.default_project_workflow_link_id = project_workflow_links.id
      )
      AND (
          SELECT COUNT(*)
          FROM project_workflow_links active
          WHERE active.project_id = project_workflow_links.project_id
      ) > 1
  );

-- name: GetProjectWorkflowUnlinkState :one
SELECT
    p.default_project_workflow_link_id AS default_project_workflow_link_id,
    (SELECT CAST(COUNT(*) AS INTEGER) FROM project_workflow_links active WHERE active.project_id = p.id) AS active_link_count
FROM projects p
WHERE p.id = sqlc.arg(project_id);

-- name: ListWorkflowTaskIDs :many
SELECT id
FROM task_records
WHERE workflow_id = sqlc.arg(workflow_id);

-- name: ListProjectTaskIDs :many
SELECT id
FROM task_records
WHERE project_id = sqlc.arg(project_id);

-- name: DeleteWorkflowTaskPendingApprovalsByWorkflowID :execrows
DELETE FROM task_pending_approvals
WHERE source_task_id IN (
    SELECT task_records.id
    FROM task_records
    WHERE workflow_id = sqlc.arg(workflow_id)
);

-- name: DeleteWorkflowTaskCurrentNodesByWorkflowID :execrows
DELETE FROM task_current_nodes
WHERE task_id IN (
    SELECT task_records.id
    FROM task_records
    WHERE workflow_id = sqlc.arg(workflow_id)
);

-- name: DeleteWorkflowTaskCommentsByWorkflowID :execrows
DELETE FROM task_comments
WHERE task_id IN (
    SELECT task_records.id
    FROM task_records
    WHERE workflow_id = sqlc.arg(workflow_id)
);

-- name: DeleteWorkflowTasksByWorkflowID :execrows
DELETE FROM tasks
WHERE id IN (
    SELECT task_records.id
    FROM task_records
    WHERE workflow_id = sqlc.arg(workflow_id)
);

-- name: ClearDeletedWorkflowDefaultProjectLinks :execrows
UPDATE projects
SET
    default_project_workflow_link_id = NULL,
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE default_project_workflow_link_id IN (
    SELECT id
    FROM project_workflow_links
    WHERE workflow_id = sqlc.arg(workflow_id)
);

-- name: DeleteProjectWorkflowLinksByWorkflowID :execrows
DELETE FROM project_workflow_links
WHERE workflow_id = sqlc.arg(workflow_id);

-- name: DeleteWorkflowByID :execrows
DELETE FROM workflows
WHERE id = sqlc.arg(id);

-- name: GetWorkflowTransitionGroupWorkflowID :one
SELECT source.workflow_id
FROM workflow_transition_groups tg
JOIN workflow_nodes source ON source.id = tg.source_node_id
WHERE tg.id = sqlc.arg(id)
LIMIT 1;

-- name: DeleteTaskCommentsByTask :execrows
DELETE FROM task_comments
WHERE task_id = sqlc.arg(task_id);

-- name: DeleteTaskLabelAssignmentsByTask :execrows
DELETE FROM task_label_assignments
WHERE task_id = sqlc.arg(task_id);

-- name: DeleteTask :execrows
DELETE FROM tasks
WHERE id = sqlc.arg(id);

-- name: CountTaskNodeReferences :one
SELECT CAST(COUNT(*) AS INTEGER) AS ref_count
FROM (
    SELECT current_node.task_id FROM task_current_nodes current_node WHERE current_node.node_id = sqlc.arg(node_id)
    UNION ALL
    SELECT approval.id FROM task_pending_approvals approval WHERE approval.source_node_id = sqlc.arg(node_id)
    UNION ALL
    SELECT branch.approval_id
    FROM task_pending_approval_branches branch
    WHERE json_extract(branch.target_snapshot_json, '$.node_id') = sqlc.arg(node_id)
);

-- name: CountCurrentTaskNodeAnchorReferences :one
SELECT CAST(COUNT(*) AS INTEGER) AS ref_count
FROM (
    SELECT current_node.task_id
    FROM task_current_nodes current_node
    WHERE current_node.node_id = sqlc.arg(node_id)
    UNION ALL
    SELECT approval.id
    FROM task_pending_approvals approval
    WHERE approval.source_node_id = sqlc.arg(node_id)
    UNION ALL
    SELECT branch.approval_id
    FROM task_pending_approval_branches branch
    WHERE json_extract(branch.target_snapshot_json, '$.node_id') = sqlc.arg(node_id)
);

-- name: CountTaskEdgeReferences :one
SELECT CAST(COUNT(*) AS INTEGER) AS ref_count
FROM (
    SELECT current_node.task_id
    FROM task_current_nodes current_node
    WHERE current_node.entered_by_edge_id = sqlc.arg(edge_id)
    UNION ALL
    SELECT branch.approval_id
    FROM task_pending_approval_branches branch
    WHERE json_extract(branch.target_snapshot_json, '$.entered_by_edge_id') = sqlc.arg(edge_id)
);

-- name: CountAllTaskEdgeReferences :one
SELECT CAST(COUNT(*) AS INTEGER) AS ref_count
FROM (
    SELECT current_node.task_id
    FROM task_current_nodes current_node
    WHERE current_node.entered_by_edge_id = sqlc.arg(edge_id)
    UNION ALL
    SELECT branch.approval_id
    FROM task_pending_approval_branches branch
    WHERE json_extract(branch.target_snapshot_json, '$.entered_by_edge_id') = sqlc.arg(edge_id)
);

-- name: GetWorkflowEdgeParameterEditPolicyImpact :one
SELECT
    (
        SELECT CAST(COUNT(*) AS INTEGER)
        FROM task_current_nodes current_node
        JOIN task_records task ON task.id = current_node.task_id
        WHERE task.workflow_id = sqlc.arg(workflow_id)
          AND current_node.entered_by_edge_id = sqlc.arg(edge_id)
          AND (
              current_node.scheduling_state IS NULL
              OR current_node.scheduling_state != 'interrupted'
          )
    ) AS active_current_node_count,
    (
        SELECT CAST(COUNT(*) AS INTEGER)
        FROM task_current_nodes current_node
        JOIN task_records task ON task.id = current_node.task_id
        WHERE task.workflow_id = sqlc.arg(workflow_id)
          AND current_node.entered_by_edge_id = sqlc.arg(edge_id)
          AND current_node.transition_branch_key IS NOT NULL
    ) AS unresolved_parallel_branch_count,
    (
        SELECT CAST(COUNT(DISTINCT approval.id) AS INTEGER)
        FROM task_pending_approvals approval
        JOIN task_records task ON task.id = approval.source_task_id
        JOIN task_pending_approval_branches branch ON branch.approval_id = approval.id
        WHERE task.workflow_id = sqlc.arg(workflow_id)
          AND json_extract(branch.target_snapshot_json, '$.entered_by_edge_id') = sqlc.arg(edge_id)
    ) AS pending_approval_count;

-- name: GetWorkflowGraphActiveWorkPolicyImpact :one
SELECT
    (
        SELECT CAST(COUNT(*) AS INTEGER)
        FROM task_current_nodes current_node
        JOIN task_records task ON task.id = current_node.task_id
        JOIN workflow_nodes node ON node.id = current_node.node_id
        WHERE task.workflow_id = sqlc.arg(workflow_id)
          AND node.kind NOT IN ('start', 'terminal')
    ) AS active_current_node_count,
    (
        SELECT CAST(COUNT(*) AS INTEGER)
        FROM task_pending_approvals approval
        JOIN task_records task ON task.id = approval.source_task_id
        WHERE task.workflow_id = sqlc.arg(workflow_id)
    ) AS pending_approval_count;

-- name: GetWorkflowDeleteImpact :one
SELECT
    w.id AS workflow_id,
    w.version,
    CAST(COUNT(DISTINCT pwl.project_id) AS INTEGER) AS project_count,
    CAST(COUNT(DISTINCT pwl.id) AS INTEGER) AS link_count,
    CAST(COUNT(DISTINCT CASE
        WHEN p.default_project_workflow_link_id = pwl.id
          AND EXISTS (
              SELECT 1
              FROM project_workflow_links other
              WHERE other.project_id = pwl.project_id
                AND other.workflow_id != w.id
          )
        THEN pwl.project_id
    END) AS INTEGER) AS default_replacement_project_count,
    CAST(COUNT(DISTINCT t.id) AS INTEGER) AS task_count,
    CAST(COUNT(DISTINCT CASE
        WHEN node.kind NOT IN ('start', 'terminal') THEN current_node.task_id
    END) AS INTEGER) AS current_node_count,
    CAST(COUNT(DISTINCT approval.id) AS INTEGER) AS pending_approval_count,
    CAST(COUNT(DISTINCT CASE
        WHEN node.kind NOT IN ('start', 'terminal')
          OR approval.id IS NOT NULL
        THEN t.id
    END) AS INTEGER) AS blocked_task_count
FROM workflows w
LEFT JOIN project_workflow_links pwl ON pwl.workflow_id = w.id
LEFT JOIN projects p ON p.id = pwl.project_id
LEFT JOIN task_records t ON t.project_workflow_link_id = pwl.id
LEFT JOIN task_current_nodes current_node ON current_node.task_id = t.id
LEFT JOIN workflow_nodes node ON node.id = current_node.node_id
LEFT JOIN task_pending_approvals approval ON approval.source_task_id = t.id
WHERE w.id = sqlc.arg(workflow_id)
GROUP BY w.id, w.version;

-- name: InsertTask :exec
INSERT INTO tasks (
    id,
    project_workflow_link_id,
    workflow_revision_seen,
    task_seq,
    short_id,
    title,
    body,
    source_url,
    source_workspace_id,
    managed_worktree_id,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
) VALUES (
    sqlc.arg(id),
    sqlc.arg(project_workflow_link_id),
    sqlc.arg(workflow_revision_seen),
    sqlc.arg(task_seq),
    sqlc.arg(short_id),
    sqlc.arg(title),
    sqlc.arg(body),
    sqlc.arg(source_url),
    sqlc.narg(source_workspace_id),
    sqlc.narg(managed_worktree_id),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms),
    sqlc.arg(metadata_json)
);

-- name: GetTask :one
SELECT
    id,
    project_id,
    project_workflow_link_id,
    workflow_id,
    workflow_revision_seen,
    task_seq,
    short_id,
    title,
    body,
    source_url,
    source_workspace_id,
    managed_worktree_id,
    execution_target_mode,
    execution_target_requested_ref,
    execution_target_resolved_ref,
    execution_target_commit_oid,
    execution_target_provenance,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
FROM task_records
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: ListTasksByIDs :many
SELECT
    id,
    project_id,
    project_workflow_link_id,
    workflow_id,
    workflow_revision_seen,
    task_seq,
    short_id,
    title,
    body,
    source_url,
    source_workspace_id,
    managed_worktree_id,
    execution_target_mode,
    execution_target_requested_ref,
    execution_target_resolved_ref,
    execution_target_commit_oid,
    execution_target_provenance,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
FROM task_records
WHERE id IN (
    SELECT CAST(value AS TEXT)
    FROM json_each(sqlc.arg(task_ids_json))
)
ORDER BY id ASC;

-- name: ListWorkflowTaskStatusRecordsByTasks :many
SELECT
    task_id,
    is_done,
    CAST(kind AS TEXT) AS kind,
    primary_status_rank,
    CAST(node_ids_json AS TEXT) AS node_ids_json,
    CAST(attention_types_json AS TEXT) AS attention_types_json
FROM workflow_task_status_records
WHERE task_id IN (sqlc.slice('task_ids'))
ORDER BY task_id ASC;

-- name: ListWorkflowTaskStatusProjectionByTasks :many
WITH
requested_task_ids AS (
    SELECT CAST(value AS TEXT) AS task_id
    FROM json_each(sqlc.arg(task_ids_json))
),
args AS (
    SELECT CAST(sqlc.arg(live_task_states_json) AS TEXT) AS live_task_states_json
),
live_task_states AS (
    SELECT
        CAST(json_extract(value, '$.task_id') AS TEXT) AS task_id,
        CAST(json_extract(value, '$.has_running') AS INTEGER) AS has_running,
        CAST(json_extract(value, '$.has_queued') AS INTEGER) AS has_queued,
        CAST(json_extract(value, '$.waiting_question') AS INTEGER) AS waiting_question,
        CAST(json_extract(value, '$.has_waiting_approval') AS INTEGER) AS has_waiting_approval
    FROM json_each((SELECT live_task_states_json FROM args))
),
effective_status AS (
    SELECT
        durable.task_id,
        durable.is_done,
        CASE
            WHEN durable.is_done != 0 THEN 'done'
            WHEN COALESCE(live.waiting_question, 0) != 0 THEN 'waiting_question'
            WHEN COALESCE(live.has_waiting_approval, 0) != 0
              OR durable.kind = 'waiting_approval' THEN 'waiting_approval'
            WHEN COALESCE(live.has_running, 0) != 0 THEN 'running'
            WHEN COALESCE(live.has_queued, 0) != 0 THEN 'queued'
            WHEN durable.kind IN ('running', 'queued', 'waiting_question') THEN 'active'
            ELSE durable.kind
        END AS kind,
        CASE
            WHEN durable.is_done != 0 THEN 1
            WHEN COALESCE(live.waiting_question, 0) != 0 THEN 2
            WHEN COALESCE(live.has_waiting_approval, 0) != 0
              OR durable.kind = 'waiting_approval' THEN 3
            WHEN COALESCE(live.has_running, 0) != 0 THEN 5
            WHEN COALESCE(live.has_queued, 0) != 0 THEN 6
            WHEN durable.kind IN ('running', 'queued', 'waiting_question') THEN 8
            ELSE durable.primary_status_rank
        END AS primary_status_rank,
        durable.node_ids_json,
        CASE
            WHEN durable.is_done != 0 THEN durable.attention_types_json
            ELSE (
                SELECT json_group_array(attention_type)
                FROM (
                    SELECT CAST(existing_attention.value AS TEXT) AS attention_type
                    FROM json_each(durable.attention_types_json) existing_attention
                    WHERE existing_attention.value != 'question'
                    UNION
                    SELECT 'question'
                    WHERE COALESCE(live.waiting_question, 0) != 0
                    UNION
                    SELECT 'approval'
                    WHERE COALESCE(live.has_waiting_approval, 0) != 0
                    ORDER BY attention_type ASC
                )
            )
        END AS attention_types_json
    FROM workflow_task_status_records durable
    LEFT JOIN live_task_states live ON live.task_id = durable.task_id
)

SELECT
    task_id,
    is_done,
    CAST(kind AS TEXT) AS kind,
    primary_status_rank,
    CAST(node_ids_json AS TEXT) AS node_ids_json,
    CAST(attention_types_json AS TEXT) AS attention_types_json
FROM effective_status
WHERE task_id IN (SELECT task_id FROM requested_task_ids)
ORDER BY task_id ASC;

-- name: AnchorTaskSearchReadSnapshot :one
SELECT EXISTS(
    SELECT 1
    FROM task_search_documents
) AS anchored;

-- name: GetTaskByProjectShortID :one
SELECT
    id,
    project_id,
    project_workflow_link_id,
    workflow_id,
    workflow_revision_seen,
    task_seq,
    short_id,
    title,
    body,
    source_url,
    source_workspace_id,
    managed_worktree_id,
    execution_target_mode,
    execution_target_requested_ref,
    execution_target_resolved_ref,
    execution_target_commit_oid,
    execution_target_provenance,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
FROM task_records
WHERE project_id = sqlc.arg(project_id)
  AND short_id = sqlc.arg(short_id)
LIMIT 1;

-- name: ListTasksByShortID :many
SELECT
    id,
    project_id,
    project_workflow_link_id,
    workflow_id,
    workflow_revision_seen,
    task_seq,
    short_id,
    title,
    body,
    source_url,
    source_workspace_id,
    managed_worktree_id,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
FROM task_records
WHERE short_id = sqlc.arg(short_id)
ORDER BY created_at_unix_ms ASC, id ASC;

-- name: UpdateTaskManagedWorktree :execrows
UPDATE tasks
SET
    managed_worktree_id = sqlc.narg(managed_worktree_id),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: ListTasksByProject :many
SELECT
    id,
    project_id,
    project_workflow_link_id,
    workflow_id,
    workflow_revision_seen,
    task_seq,
    short_id,
    title,
    body,
    source_url,
    source_workspace_id,
    managed_worktree_id,
    execution_target_mode,
    execution_target_requested_ref,
    execution_target_resolved_ref,
    execution_target_commit_oid,
    execution_target_provenance,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
FROM task_records
WHERE project_workflow_link_id IN (
    SELECT id
    FROM project_workflow_links
    WHERE project_workflow_links.project_id = sqlc.arg(project_id)
)
ORDER BY updated_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM tasks storage
    WHERE storage.id = task_records.id
) DESC;

-- name: ListProjectWorkflowTaskActivity :many
SELECT
    workflow_id,
    CAST(MAX(updated_at_unix_ms) AS INTEGER) AS latest_updated_at_unix_ms
FROM task_records
WHERE project_id = sqlc.arg(project_id)
GROUP BY workflow_id
ORDER BY latest_updated_at_unix_ms DESC, workflow_id ASC;

-- name: ListBoardColumnTaskCounts :many
WITH
label_filter_args AS (
    SELECT
        CAST(sqlc.arg(label_filter_kind) AS TEXT) AS label_filter_kind,
        CAST(sqlc.narg(label_filter_mode) AS TEXT) AS label_filter_mode,
        CAST(sqlc.arg(label_ids_json) AS TEXT) AS label_ids_json,
        CAST(sqlc.narg(dependency_filter) AS INTEGER) AS dependency_filter
),
effective_current_nodes AS (
    SELECT
        t.id AS task_id,
        current_node.node_id
    FROM task_current_nodes current_node
    JOIN task_records t ON t.id = current_node.task_id
    WHERE t.project_id = sqlc.arg(project_id)
      AND t.workflow_id = sqlc.arg(workflow_id)
)
SELECT
    node_id,
    CAST(COUNT(DISTINCT task_id) AS INTEGER) AS task_count
FROM effective_current_nodes
JOIN label_filter_args
WHERE (
    label_filter_args.label_filter_kind = 'none'
    OR (
        label_filter_args.label_filter_kind = 'named'
        AND label_filter_args.label_filter_mode = 'any'
        AND (
            EXISTS (
                SELECT 1
                FROM json_each(label_filter_args.label_ids_json) selected_label
                JOIN task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                  ON assignment.label_id = selected_label.value
                WHERE assignment.task_id = effective_current_nodes.task_id
            )
            OR EXISTS (
                SELECT 1
                FROM json_each(sqlc.arg(excluded_label_ids_json)) excluded_label
                WHERE NOT EXISTS (
                    SELECT 1
                    FROM task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                    WHERE assignment.label_id = excluded_label.value
                      AND assignment.task_id = effective_current_nodes.task_id
                )
            )
        )
    )
    OR (
        label_filter_args.label_filter_kind = 'named'
        AND label_filter_args.label_filter_mode = 'all'
        AND NOT EXISTS (
            SELECT 1
            FROM json_each(label_filter_args.label_ids_json) selected_label
            WHERE NOT EXISTS (
                SELECT 1
                FROM task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                WHERE assignment.label_id = selected_label.value
                  AND assignment.task_id = effective_current_nodes.task_id
            )
        )
        AND NOT EXISTS (
            SELECT 1
            FROM json_each(sqlc.arg(excluded_label_ids_json)) excluded_label
            JOIN task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
              ON assignment.label_id = excluded_label.value
            WHERE assignment.task_id = effective_current_nodes.task_id
        )
    )
    OR (
        label_filter_args.label_filter_kind = 'unlabeled'
        AND NOT EXISTS (
            SELECT 1
            FROM task_label_assignments assignment
            WHERE assignment.task_id = effective_current_nodes.task_id
        )
    )
)
AND
    (
        label_filter_args.dependency_filter IS NULL
        OR CAST(label_filter_args.dependency_filter AS INTEGER) = (
            NOT EXISTS (
                SELECT 1
                FROM task_dependencies dependency INDEXED BY task_dependencies_reverse_idx
                WHERE dependency.blocked_task_id = effective_current_nodes.task_id
                  AND NOT EXISTS (
                      SELECT 1
                      FROM workflow_task_status_records status
                      WHERE status.task_id = dependency.blocker_task_id
                        AND status.is_done != 0
                  )
            )
        )
    )

GROUP BY node_id
ORDER BY node_id ASC;

-- name: ListWorkflowTaskListRows :many
WITH
args AS (
    SELECT
        CAST(sqlc.arg(project_id) AS TEXT) AS project_id,
        sqlc.narg(workflow_id) AS workflow_id,
        CAST(sqlc.narg(visible_columns_json) AS TEXT) AS visible_columns_json,
        CAST(sqlc.arg(column_filter_set) AS INTEGER) AS column_filter_set,
        CAST(sqlc.narg(column_keys_json) AS TEXT) AS column_keys_json,
        CAST(sqlc.arg(status_filter_set) AS INTEGER) AS status_filter_set,
        CAST(sqlc.arg(status_kinds_json) AS TEXT) AS status_kinds_json,
        CAST(sqlc.arg(attention_filter_set) AS INTEGER) AS attention_filter_set,
        CAST(sqlc.arg(attention_kinds_json) AS TEXT) AS attention_kinds_json,
        CAST(sqlc.arg(label_filter_kind) AS TEXT) AS label_filter_kind,
        CAST(sqlc.narg(label_filter_mode) AS TEXT) AS label_filter_mode,
        CAST(sqlc.arg(label_ids_json) AS TEXT) AS label_ids_json,
        CAST(sqlc.narg(dependency_filter) AS INTEGER) AS dependency_filter,
        CAST(sqlc.arg(offset_rows) AS INTEGER) AS offset_rows,
        CAST(sqlc.arg(sort_selector_count) AS INTEGER) AS sort_selector_count,
        CAST(sqlc.arg(sort_1_field) AS TEXT) AS sort_1_field,
        CAST(sqlc.arg(sort_1_desc) AS INTEGER) AS sort_1_desc,
        CAST(sqlc.arg(sort_2_field) AS TEXT) AS sort_2_field,
        CAST(sqlc.arg(sort_2_desc) AS INTEGER) AS sort_2_desc,
        CAST(sqlc.arg(sort_3_field) AS TEXT) AS sort_3_field,
        CAST(sqlc.arg(sort_3_desc) AS INTEGER) AS sort_3_desc,
        CAST(sqlc.arg(sort_4_field) AS TEXT) AS sort_4_field,
        CAST(sqlc.arg(sort_4_desc) AS INTEGER) AS sort_4_desc,
        CAST(sqlc.arg(sort_5_field) AS TEXT) AS sort_5_field,
        CAST(sqlc.arg(sort_5_desc) AS INTEGER) AS sort_5_desc,
        CAST(sqlc.arg(sort_6_field) AS TEXT) AS sort_6_field,
        CAST(sqlc.arg(sort_6_desc) AS INTEGER) AS sort_6_desc,
        CAST(sqlc.arg(sort_7_field) AS TEXT) AS sort_7_field,
        CAST(sqlc.arg(sort_7_desc) AS INTEGER) AS sort_7_desc,
        CASE WHEN
            (CAST(sqlc.arg(sort_selector_count) AS INTEGER) >= 1 AND CAST(sqlc.arg(sort_1_field) AS TEXT) = 'labels') OR
            (CAST(sqlc.arg(sort_selector_count) AS INTEGER) >= 2 AND CAST(sqlc.arg(sort_2_field) AS TEXT) = 'labels') OR
            (CAST(sqlc.arg(sort_selector_count) AS INTEGER) >= 3 AND CAST(sqlc.arg(sort_3_field) AS TEXT) = 'labels') OR
            (CAST(sqlc.arg(sort_selector_count) AS INTEGER) >= 4 AND CAST(sqlc.arg(sort_4_field) AS TEXT) = 'labels') OR
            (CAST(sqlc.arg(sort_selector_count) AS INTEGER) >= 5 AND CAST(sqlc.arg(sort_5_field) AS TEXT) = 'labels') OR
            (CAST(sqlc.arg(sort_selector_count) AS INTEGER) >= 6 AND CAST(sqlc.arg(sort_6_field) AS TEXT) = 'labels') OR
            (CAST(sqlc.arg(sort_selector_count) AS INTEGER) >= 7 AND CAST(sqlc.arg(sort_7_field) AS TEXT) = 'labels')
        THEN 1 ELSE 0 END AS labels_requested,
        CAST(sqlc.arg(live_task_states_json) AS TEXT) AS live_task_states_json,
        CAST(sqlc.arg(limit_rows) AS INTEGER) AS limit_rows
),
visible_columns AS (
    SELECT
        CAST(json_extract(value, '$.node_id') AS TEXT) AS node_id,
        CAST(json_extract(value, '$.node_key') AS TEXT) AS node_key,
        CAST(json_extract(value, '$.node_kind') AS TEXT) AS node_kind,
        CAST(json_extract(value, '$.status_order') AS INTEGER) AS column_rank
    FROM args, json_each(args.visible_columns_json)
),
current_positions AS (
    SELECT current_node.task_id, current_node.node_id
    FROM args
    CROSS JOIN project_workflow_links task_link
    CROSS JOIN tasks t INDEXED BY tasks_project_workflow_link_idx
    JOIN task_current_nodes current_node ON current_node.task_id = t.id
    WHERE task_link.project_id = args.project_id
      AND (args.workflow_id IS NULL OR task_link.workflow_id = args.workflow_id)
      AND t.project_workflow_link_id = task_link.id
),
column_positions AS (
    SELECT DISTINCT position.task_id, columns.node_key, columns.column_rank
    FROM current_positions position
    JOIN visible_columns columns ON columns.node_id = position.node_id
),
column_facts AS (
    SELECT
        task_id,
        CAST(MIN(column_rank) AS INTEGER) AS column_rank,
        CAST(json_group_array(node_key) AS TEXT) AS column_keys_json
    FROM (
        SELECT task_id, node_key, column_rank
        FROM column_positions
        ORDER BY task_id ASC, column_rank ASC, node_key ASC
    )
    GROUP BY task_id
),
live_task_states AS (
    SELECT
        CAST(json_extract(value, '$.task_id') AS TEXT) AS task_id,
        CAST(json_extract(value, '$.has_running') AS INTEGER) AS has_running,
        CAST(json_extract(value, '$.has_queued') AS INTEGER) AS has_queued,
        CAST(json_extract(value, '$.waiting_question') AS INTEGER) AS waiting_question,
        CAST(json_extract(value, '$.has_waiting_approval') AS INTEGER) AS has_waiting_approval
    FROM json_each((SELECT live_task_states_json FROM args))
),
effective_status AS (
    SELECT
        durable.task_id,
        durable.is_done,
        CASE
            WHEN durable.is_done != 0 THEN 'done'
            WHEN COALESCE(live.waiting_question, 0) != 0 THEN 'waiting_question'
            WHEN COALESCE(live.has_waiting_approval, 0) != 0
              OR durable.kind = 'waiting_approval' THEN 'waiting_approval'
            WHEN COALESCE(live.has_running, 0) != 0 THEN 'running'
            WHEN COALESCE(live.has_queued, 0) != 0 THEN 'queued'
            WHEN durable.kind IN ('running', 'queued', 'waiting_question') THEN 'active'
            ELSE durable.kind
        END AS kind,
        CASE
            WHEN durable.is_done != 0 THEN 1
            WHEN COALESCE(live.waiting_question, 0) != 0 THEN 2
            WHEN COALESCE(live.has_waiting_approval, 0) != 0
              OR durable.kind = 'waiting_approval' THEN 3
            WHEN COALESCE(live.has_running, 0) != 0 THEN 5
            WHEN COALESCE(live.has_queued, 0) != 0 THEN 6
            WHEN durable.kind IN ('running', 'queued', 'waiting_question') THEN 8
            ELSE durable.primary_status_rank
        END AS primary_status_rank,
        durable.node_ids_json,
        CASE
            WHEN durable.is_done != 0 THEN durable.attention_types_json
            ELSE (
                SELECT json_group_array(attention_type)
                FROM (
                    SELECT CAST(existing_attention.value AS TEXT) AS attention_type
                    FROM json_each(durable.attention_types_json) existing_attention
                    WHERE existing_attention.value != 'question'
                    UNION
                    SELECT 'question'
                    WHERE COALESCE(live.waiting_question, 0) != 0
                    UNION
                    SELECT 'approval'
                    WHERE COALESCE(live.has_waiting_approval, 0) != 0
                    ORDER BY attention_type ASC
                )
            )
        END AS attention_types_json
    FROM workflow_task_status_records durable
    LEFT JOIN live_task_states live ON live.task_id = durable.task_id
),

eligible_rows AS (
    SELECT
        t.id,
        pwl.project_id,
        t.project_workflow_link_id,
        pwl.workflow_id,
        w.name AS workflow_name,
        t.workflow_revision_seen,
        t.task_seq,
        t.short_id,
        t.title,
        t.body,
        t.source_url,
        t.source_workspace_id,
        t.managed_worktree_id,
        t.execution_target_mode,
        t.execution_target_requested_ref,
        t.execution_target_resolved_ref,
        t.execution_target_commit_oid,
        t.execution_target_provenance,
        t.created_at_unix_ms,
        t.updated_at_unix_ms,
        t.metadata_json,
        column_facts.column_rank,
        column_facts.column_keys_json,
        CAST(status.kind AS TEXT) AS kind,
        CAST(status.primary_status_rank AS INTEGER) AS primary_status_rank,
        CAST(status.node_ids_json AS TEXT) AS node_ids_json,
        CAST(status.attention_types_json AS TEXT) AS attention_types_json,
        CAST(kent_label_casefold_v1_fold(t.title) AS TEXT) AS title_sort
    FROM args
    CROSS JOIN project_workflow_links pwl
    CROSS JOIN tasks t INDEXED BY tasks_project_workflow_link_idx
    JOIN workflows w ON w.id = pwl.workflow_id
    JOIN effective_status status ON status.task_id = t.id
    LEFT JOIN column_facts
        ON args.workflow_id IS NOT NULL
       AND column_facts.task_id = t.id
    WHERE pwl.project_id = args.project_id
      AND (args.workflow_id IS NULL OR pwl.workflow_id = args.workflow_id)
      AND t.project_workflow_link_id = pwl.id
      AND (
          args.column_filter_set = 0
          OR EXISTS (
              SELECT 1
              FROM json_each(args.column_keys_json) filter_key
              JOIN json_each(column_facts.column_keys_json) task_key ON task_key.value = filter_key.value
          )
      )
      AND (
          args.status_filter_set = 0
          OR status.kind IN (SELECT value FROM json_each(args.status_kinds_json))
      )
      AND (
          args.attention_filter_set = 0
          OR EXISTS (
              SELECT 1
              FROM json_each(args.attention_kinds_json) filter_attention
              JOIN json_each(status.attention_types_json) task_attention ON task_attention.value = filter_attention.value
          )
      )
      AND (
          args.label_filter_kind = 'none'
          OR (
              args.label_filter_kind = 'named'
              AND args.label_filter_mode = 'any'
              AND (
                  EXISTS (
                      SELECT 1
                      FROM json_each(args.label_ids_json) selected_label
                      JOIN task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                        ON assignment.label_id = selected_label.value
                      WHERE assignment.task_id = t.id
                  )
                  OR EXISTS (
                      SELECT 1
                      FROM json_each(sqlc.arg(excluded_label_ids_json)) excluded_label
                      WHERE NOT EXISTS (
                          SELECT 1
                          FROM task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                          WHERE assignment.label_id = excluded_label.value
                            AND assignment.task_id = t.id
                      )
                  )
              )
          )
          OR (
              args.label_filter_kind = 'named'
              AND args.label_filter_mode = 'all'
              AND NOT EXISTS (
                  SELECT 1
                  FROM json_each(args.label_ids_json) selected_label
                  WHERE NOT EXISTS (
                      SELECT 1
                      FROM task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                      WHERE assignment.label_id = selected_label.value
                        AND assignment.task_id = t.id
                  )
              )
              AND NOT EXISTS (
                  SELECT 1
                  FROM json_each(sqlc.arg(excluded_label_ids_json)) excluded_label
                  JOIN task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                    ON assignment.label_id = excluded_label.value
                  WHERE assignment.task_id = t.id
              )
          )
          OR (
              args.label_filter_kind = 'unlabeled'
              AND NOT EXISTS (
                  SELECT 1
                  FROM task_label_assignments assignment
                  WHERE assignment.task_id = t.id
              )
          )
      )
      AND
          (
              args.dependency_filter IS NULL
              OR CAST(args.dependency_filter AS INTEGER) = (
                  NOT EXISTS (
                      SELECT 1
                      FROM task_dependencies dependency INDEXED BY task_dependencies_reverse_idx
                      WHERE dependency.blocked_task_id = t.id
                        AND NOT EXISTS (
                            SELECT 1
                            FROM workflow_task_status_records status
                            WHERE status.task_id = dependency.blocker_task_id
                              AND status.is_done != 0
                        )
                  )
              )
          )

),
task_label_values AS (
    SELECT
        eligible.id AS task_id,

(
    SELECT group_concat(printf('%03d', ordered_label.ordinal), '')
    FROM (
        SELECT label.ordinal
        FROM task_label_assignments assignment
        JOIN project_labels label ON label.id = assignment.label_id
        WHERE assignment.task_id = eligible.id
        ORDER BY label.ordinal ASC, label.id ASC
    ) ordered_label
)
 AS label_ordinals
    FROM args
    CROSS JOIN eligible_rows eligible
    WHERE args.labels_requested != 0
      AND EXISTS (
        SELECT 1
        FROM task_label_assignments assignment
        WHERE assignment.task_id = eligible.id
    )
),
scored_rows AS (
    SELECT
        eligible.*,
        labels.label_ordinals,
        CASE WHEN args.labels_requested != 0 AND labels.task_id IS NULL THEN 1 ELSE 0 END AS labels_unlabeled,
        CASE args.sort_1_field
            WHEN 'created' THEN printf('%020d', eligible.created_at_unix_ms)
            WHEN 'updated' THEN printf('%020d', eligible.updated_at_unix_ms)
            WHEN 'status' THEN printf('%020d', eligible.primary_status_rank)
            WHEN 'column' THEN printf('%020d', eligible.column_rank)
            WHEN 'title' THEN eligible.title_sort
            WHEN 'labels' THEN labels.label_ordinals
            WHEN 'short_id' THEN printf('%020d', eligible.task_seq)
            ELSE ''
        END AS sort_1_value,
        CASE args.sort_2_field
            WHEN 'created' THEN printf('%020d', eligible.created_at_unix_ms)
            WHEN 'updated' THEN printf('%020d', eligible.updated_at_unix_ms)
            WHEN 'status' THEN printf('%020d', eligible.primary_status_rank)
            WHEN 'column' THEN printf('%020d', eligible.column_rank)
            WHEN 'title' THEN eligible.title_sort
            WHEN 'labels' THEN labels.label_ordinals
            WHEN 'short_id' THEN printf('%020d', eligible.task_seq)
            ELSE ''
        END AS sort_2_value,
        CASE args.sort_3_field
            WHEN 'created' THEN printf('%020d', eligible.created_at_unix_ms)
            WHEN 'updated' THEN printf('%020d', eligible.updated_at_unix_ms)
            WHEN 'status' THEN printf('%020d', eligible.primary_status_rank)
            WHEN 'column' THEN printf('%020d', eligible.column_rank)
            WHEN 'title' THEN eligible.title_sort
            WHEN 'labels' THEN labels.label_ordinals
            WHEN 'short_id' THEN printf('%020d', eligible.task_seq)
            ELSE ''
        END AS sort_3_value,
        CASE args.sort_4_field
            WHEN 'created' THEN printf('%020d', eligible.created_at_unix_ms)
            WHEN 'updated' THEN printf('%020d', eligible.updated_at_unix_ms)
            WHEN 'status' THEN printf('%020d', eligible.primary_status_rank)
            WHEN 'column' THEN printf('%020d', eligible.column_rank)
            WHEN 'title' THEN eligible.title_sort
            WHEN 'labels' THEN labels.label_ordinals
            WHEN 'short_id' THEN printf('%020d', eligible.task_seq)
            ELSE ''
        END AS sort_4_value,
        CASE args.sort_5_field
            WHEN 'created' THEN printf('%020d', eligible.created_at_unix_ms)
            WHEN 'updated' THEN printf('%020d', eligible.updated_at_unix_ms)
            WHEN 'status' THEN printf('%020d', eligible.primary_status_rank)
            WHEN 'column' THEN printf('%020d', eligible.column_rank)
            WHEN 'title' THEN eligible.title_sort
            WHEN 'labels' THEN labels.label_ordinals
            WHEN 'short_id' THEN printf('%020d', eligible.task_seq)
            ELSE ''
        END AS sort_5_value,
        CASE args.sort_6_field
            WHEN 'created' THEN printf('%020d', eligible.created_at_unix_ms)
            WHEN 'updated' THEN printf('%020d', eligible.updated_at_unix_ms)
            WHEN 'status' THEN printf('%020d', eligible.primary_status_rank)
            WHEN 'column' THEN printf('%020d', eligible.column_rank)
            WHEN 'title' THEN eligible.title_sort
            WHEN 'labels' THEN labels.label_ordinals
            WHEN 'short_id' THEN printf('%020d', eligible.task_seq)
            ELSE ''
        END AS sort_6_value,
        CASE args.sort_7_field
            WHEN 'created' THEN printf('%020d', eligible.created_at_unix_ms)
            WHEN 'updated' THEN printf('%020d', eligible.updated_at_unix_ms)
            WHEN 'status' THEN printf('%020d', eligible.primary_status_rank)
            WHEN 'column' THEN printf('%020d', eligible.column_rank)
            WHEN 'title' THEN eligible.title_sort
            WHEN 'labels' THEN labels.label_ordinals
            WHEN 'short_id' THEN printf('%020d', eligible.task_seq)
            ELSE ''
        END AS sort_7_value
    FROM args
    CROSS JOIN eligible_rows eligible
    LEFT JOIN task_label_values labels
        ON args.labels_requested != 0
       AND labels.task_id = eligible.id
),
matching_workflows AS (
    SELECT task_link.workflow_id
    FROM args
    CROSS JOIN project_workflow_links task_link
    WHERE task_link.project_id = args.project_id
      AND (args.workflow_id IS NULL OR task_link.workflow_id = args.workflow_id)
      AND EXISTS (
          SELECT 1
          FROM eligible_rows eligible
          WHERE eligible.project_workflow_link_id = task_link.id
          LIMIT 1
      )
    LIMIT 2
),
page_rows AS (
    SELECT rows.*
    FROM scored_rows rows
    CROSS JOIN args
    ORDER BY
        CASE WHEN args.sort_selector_count >= 1 AND args.labels_requested != 0 AND args.sort_1_field = 'labels' THEN rows.labels_unlabeled ELSE 0 END ASC,
        CASE WHEN args.sort_selector_count >= 1 AND args.sort_1_desc = 0 THEN rows.sort_1_value END ASC,
        CASE WHEN args.sort_selector_count >= 1 AND args.sort_1_desc != 0 THEN rows.sort_1_value END DESC,
        CASE WHEN args.sort_selector_count >= 2 AND args.labels_requested != 0 AND args.sort_2_field = 'labels' THEN rows.labels_unlabeled ELSE 0 END ASC,
        CASE WHEN args.sort_selector_count >= 2 AND args.sort_2_desc = 0 THEN rows.sort_2_value END ASC,
        CASE WHEN args.sort_selector_count >= 2 AND args.sort_2_desc != 0 THEN rows.sort_2_value END DESC,
        CASE WHEN args.sort_selector_count >= 3 AND args.labels_requested != 0 AND args.sort_3_field = 'labels' THEN rows.labels_unlabeled ELSE 0 END ASC,
        CASE WHEN args.sort_selector_count >= 3 AND args.sort_3_desc = 0 THEN rows.sort_3_value END ASC,
        CASE WHEN args.sort_selector_count >= 3 AND args.sort_3_desc != 0 THEN rows.sort_3_value END DESC,
        CASE WHEN args.sort_selector_count >= 4 AND args.labels_requested != 0 AND args.sort_4_field = 'labels' THEN rows.labels_unlabeled ELSE 0 END ASC,
        CASE WHEN args.sort_selector_count >= 4 AND args.sort_4_desc = 0 THEN rows.sort_4_value END ASC,
        CASE WHEN args.sort_selector_count >= 4 AND args.sort_4_desc != 0 THEN rows.sort_4_value END DESC,
        CASE WHEN args.sort_selector_count >= 5 AND args.labels_requested != 0 AND args.sort_5_field = 'labels' THEN rows.labels_unlabeled ELSE 0 END ASC,
        CASE WHEN args.sort_selector_count >= 5 AND args.sort_5_desc = 0 THEN rows.sort_5_value END ASC,
        CASE WHEN args.sort_selector_count >= 5 AND args.sort_5_desc != 0 THEN rows.sort_5_value END DESC,
        CASE WHEN args.sort_selector_count >= 6 AND args.labels_requested != 0 AND args.sort_6_field = 'labels' THEN rows.labels_unlabeled ELSE 0 END ASC,
        CASE WHEN args.sort_selector_count >= 6 AND args.sort_6_desc = 0 THEN rows.sort_6_value END ASC,
        CASE WHEN args.sort_selector_count >= 6 AND args.sort_6_desc != 0 THEN rows.sort_6_value END DESC,
        CASE WHEN args.sort_selector_count >= 7 AND args.labels_requested != 0 AND args.sort_7_field = 'labels' THEN rows.labels_unlabeled ELSE 0 END ASC,
        CASE WHEN args.sort_selector_count >= 7 AND args.sort_7_desc = 0 THEN rows.sort_7_value END ASC,
        CASE WHEN args.sort_selector_count >= 7 AND args.sort_7_desc != 0 THEN rows.sort_7_value END DESC,
        rows.id ASC
    LIMIT (SELECT limit_rows FROM args)
    OFFSET (SELECT offset_rows FROM args)
)
SELECT
    page.id,
    page.project_id,
    page.project_workflow_link_id,
    page.workflow_id,
    page.workflow_name,
    page.workflow_revision_seen,
    page.task_seq,
    page.short_id,
    page.title,
    page.body,
    page.source_url,
    page.source_workspace_id,
    page.managed_worktree_id,
    page.execution_target_mode,
    page.execution_target_requested_ref,
    page.execution_target_resolved_ref,
    page.execution_target_commit_oid,
    page.execution_target_provenance,
    page.created_at_unix_ms,
    page.updated_at_unix_ms,
    page.metadata_json,
    page.column_rank,
    page.column_keys_json,
    page.kind,
    page.primary_status_rank,
    page.node_ids_json,
    page.attention_types_json,
    page.title_sort,
    CAST((SELECT COUNT(*) FROM matching_workflows) AS INTEGER) AS matching_workflow_count
FROM args
CROSS JOIN (SELECT 1) summary
LEFT JOIN page_rows page ON TRUE
ORDER BY
    CASE WHEN args.sort_selector_count >= 1 AND args.labels_requested != 0 AND args.sort_1_field = 'labels' THEN page.labels_unlabeled ELSE 0 END ASC,
    CASE WHEN args.sort_selector_count >= 1 AND args.sort_1_desc = 0 THEN page.sort_1_value END ASC,
    CASE WHEN args.sort_selector_count >= 1 AND args.sort_1_desc != 0 THEN page.sort_1_value END DESC,
    CASE WHEN args.sort_selector_count >= 2 AND args.labels_requested != 0 AND args.sort_2_field = 'labels' THEN page.labels_unlabeled ELSE 0 END ASC,
    CASE WHEN args.sort_selector_count >= 2 AND args.sort_2_desc = 0 THEN page.sort_2_value END ASC,
    CASE WHEN args.sort_selector_count >= 2 AND args.sort_2_desc != 0 THEN page.sort_2_value END DESC,
    CASE WHEN args.sort_selector_count >= 3 AND args.labels_requested != 0 AND args.sort_3_field = 'labels' THEN page.labels_unlabeled ELSE 0 END ASC,
    CASE WHEN args.sort_selector_count >= 3 AND args.sort_3_desc = 0 THEN page.sort_3_value END ASC,
    CASE WHEN args.sort_selector_count >= 3 AND args.sort_3_desc != 0 THEN page.sort_3_value END DESC,
    CASE WHEN args.sort_selector_count >= 4 AND args.labels_requested != 0 AND args.sort_4_field = 'labels' THEN page.labels_unlabeled ELSE 0 END ASC,
    CASE WHEN args.sort_selector_count >= 4 AND args.sort_4_desc = 0 THEN page.sort_4_value END ASC,
    CASE WHEN args.sort_selector_count >= 4 AND args.sort_4_desc != 0 THEN page.sort_4_value END DESC,
    CASE WHEN args.sort_selector_count >= 5 AND args.labels_requested != 0 AND args.sort_5_field = 'labels' THEN page.labels_unlabeled ELSE 0 END ASC,
    CASE WHEN args.sort_selector_count >= 5 AND args.sort_5_desc = 0 THEN page.sort_5_value END ASC,
    CASE WHEN args.sort_selector_count >= 5 AND args.sort_5_desc != 0 THEN page.sort_5_value END DESC,
    CASE WHEN args.sort_selector_count >= 6 AND args.labels_requested != 0 AND args.sort_6_field = 'labels' THEN page.labels_unlabeled ELSE 0 END ASC,
    CASE WHEN args.sort_selector_count >= 6 AND args.sort_6_desc = 0 THEN page.sort_6_value END ASC,
    CASE WHEN args.sort_selector_count >= 6 AND args.sort_6_desc != 0 THEN page.sort_6_value END DESC,
    CASE WHEN args.sort_selector_count >= 7 AND args.labels_requested != 0 AND args.sort_7_field = 'labels' THEN page.labels_unlabeled ELSE 0 END ASC,
    CASE WHEN args.sort_selector_count >= 7 AND args.sort_7_desc = 0 THEN page.sort_7_value END ASC,
    CASE WHEN args.sort_selector_count >= 7 AND args.sort_7_desc != 0 THEN page.sort_7_value END DESC,
    page.id ASC;

-- name: UpdateTaskEditableFields :execrows
UPDATE tasks
SET
    title = sqlc.arg(title),
    body = sqlc.arg(body),
    source_workspace_id = sqlc.narg(source_workspace_id),
    metadata_json = sqlc.arg(metadata_json),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: WorkflowHasContinueSessionEdge :one
SELECT CAST(EXISTS (
    SELECT 1
    FROM workflow_edges e
    JOIN workflow_transition_groups g ON g.id = e.transition_group_id
    JOIN workflow_nodes n ON n.id = g.source_node_id
    WHERE n.workflow_id = sqlc.arg(workflow_id)
      AND e.context_mode = 'continue_session'
) AS INTEGER) AS has_continue_session_edge;

-- name: CountNonTerminalTasksByManagedWorktree :one
SELECT CAST(COUNT(DISTINCT t.id) AS INTEGER) AS ref_count
FROM tasks t
JOIN task_current_nodes current_node ON current_node.task_id = t.id
JOIN workflow_nodes node ON node.id = current_node.node_id
WHERE t.managed_worktree_id = sqlc.arg(managed_worktree_id)
  AND node.kind != 'terminal';

-- name: CountOtherNonTerminalTasksByManagedWorktree :one
SELECT CAST(COUNT(DISTINCT t.id) AS INTEGER) AS ref_count
FROM tasks t
JOIN task_current_nodes current_node ON current_node.task_id = t.id
JOIN workflow_nodes node ON node.id = current_node.node_id
WHERE t.managed_worktree_id = sqlc.arg(managed_worktree_id)
  AND t.id != sqlc.arg(task_id)
  AND node.kind != 'terminal';

-- name: CountNonTerminalTasksBySourceWorkspace :one
SELECT CAST(COUNT(DISTINCT t.id) AS INTEGER) AS task_count
FROM tasks t
WHERE t.source_workspace_id = sqlc.arg(workspace_id)
  AND (
      EXISTS (
          SELECT 1
          FROM task_current_nodes current_node
          JOIN workflow_nodes node ON node.id = current_node.node_id
          WHERE current_node.task_id = t.id
            AND node.kind != 'terminal'
      )
      OR EXISTS (
          SELECT 1
          FROM task_pending_approvals approval
          WHERE approval.source_task_id = t.id
      )
  );

-- name: InsertTaskComment :exec
INSERT INTO task_comments (
    id,
    task_id,
    body,
    author_kind,
    author_id,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES (
    sqlc.arg(id),
    sqlc.arg(task_id),
    sqlc.arg(body),
    sqlc.arg(author_kind),
    sqlc.arg(author_id),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms)
);

-- name: UpdateTaskCommentBody :execrows
UPDATE task_comments
SET
    body = sqlc.arg(body),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: DeleteTaskComment :execrows
DELETE FROM task_comments
WHERE id = sqlc.arg(id);

-- name: CountTaskComments :one
SELECT CAST(COUNT(*) AS INTEGER)
FROM task_comments
WHERE task_id = sqlc.arg(task_id);

-- name: ListTaskComments :many
SELECT
    id,
    task_id,
    body,
    author_kind,
    author_id,
    created_at_unix_ms,
    updated_at_unix_ms
FROM task_comments
WHERE task_id = sqlc.arg(task_id)
ORDER BY created_at_unix_ms DESC, id DESC
LIMIT sqlc.arg(limit_rows)
OFFSET sqlc.arg(offset_rows);

-- name: GetWorkspaceBindingByID :one
SELECT
    p.id AS project_id,
    p.display_name AS project_display_name,
    p.project_key,
    w.id AS workspace_id,
    w.canonical_root_path AS workspace_root
FROM workspaces w
JOIN projects p ON p.id = w.project_id
WHERE w.id = sqlc.arg(workspace_id)
LIMIT 1;

-- name: UpsertProject :exec
INSERT INTO projects (
    id,
    display_name,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
) VALUES (
    sqlc.arg(id),
    sqlc.arg(display_name),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms),
    sqlc.arg(metadata_json)
)
ON CONFLICT(id) DO UPDATE SET
    display_name = excluded.display_name,
    updated_at_unix_ms = excluded.updated_at_unix_ms,
    metadata_json = excluded.metadata_json;

-- name: UpsertWorkspace :exec
INSERT INTO workspaces (
    id,
    project_id,
    canonical_root_path,
    git_metadata_json,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES (
    sqlc.arg(id),
    sqlc.arg(project_id),
    sqlc.arg(canonical_root_path),
    sqlc.arg(git_metadata_json),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms)
)
ON CONFLICT(id) DO UPDATE SET
    project_id = excluded.project_id,
    canonical_root_path = excluded.canonical_root_path,
    git_metadata_json = excluded.git_metadata_json,
    updated_at_unix_ms = excluded.updated_at_unix_ms;

-- name: InsertWorkspaceBinding :execrows
INSERT INTO workspaces (
    id,
    project_id,
    canonical_root_path,
    git_metadata_json,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES (
    sqlc.arg(id),
    sqlc.arg(project_id),
    sqlc.arg(canonical_root_path),
    sqlc.arg(git_metadata_json),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms)
)
ON CONFLICT(project_id, canonical_root_path) DO NOTHING;

-- name: UpdateWorkspaceBindingCanonicalRoot :execrows
UPDATE workspaces
SET
    canonical_root_path = sqlc.arg(canonical_root_path),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: DeleteWorkspaceBindingByID :execrows
DELETE FROM workspaces
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(workspace_id);

-- name: AcquireWorkspaceUnlinkWriteLock :execrows
UPDATE workspaces
SET updated_at_unix_ms = updated_at_unix_ms
WHERE project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(workspace_id);

-- name: ListWorkspaceSessionIDs :many
SELECT id
FROM sessions
WHERE workspace_id = sqlc.arg(workspace_id)
ORDER BY rowid ASC;

-- name: CountExecutableCurrentNodesByWorkspace :one
SELECT CAST(COUNT(DISTINCT current_node.task_id) AS INTEGER) AS current_node_count
FROM task_current_nodes current_node
JOIN task_records task ON task.id = current_node.task_id
JOIN workflow_nodes node ON node.id = current_node.node_id
LEFT JOIN sessions session ON session.id = current_node.session_id
WHERE node.kind IN ('agent', 'script')
  AND (
      task.source_workspace_id = sqlc.arg(workspace_id)
      OR session.workspace_id = sqlc.arg(workspace_id)
  );

-- name: CountManagedOwnedWorktreesByWorkspace :one
SELECT CAST(COUNT(*) AS INTEGER) AS worktree_count
FROM worktrees
WHERE workspace_id = sqlc.arg(workspace_id)
  AND managed <> 0
  AND created_branch <> 0;

-- name: CountTasksMissingSourceWorkspaceSnapshot :one
SELECT CAST(COUNT(*) AS INTEGER) AS task_count
FROM tasks
WHERE source_workspace_id = sqlc.arg(workspace_id)
  AND (
      NOT json_valid(metadata_json)
      OR NULLIF(json_extract(metadata_json, '$.source_workspace_snapshot.root_path'), '') IS NULL
      OR NULLIF(json_extract(metadata_json, '$.source_workspace_snapshot.display_name'), '') IS NULL
  );

-- name: ListTasksMissingSourceWorkspaceDisplayName :many
SELECT metadata_json
FROM tasks
WHERE source_workspace_id = sqlc.arg(workspace_id)
  AND json_valid(metadata_json)
  AND NULLIF(json_extract(metadata_json, '$.source_workspace_snapshot.root_path'), '') IS NOT NULL
  AND NULLIF(json_extract(metadata_json, '$.source_workspace_snapshot.display_name'), '') IS NULL
ORDER BY rowid ASC;

-- name: CountSessionsMissingWorkspaceSnapshot :one
SELECT CAST(COUNT(*) AS INTEGER) AS session_count
FROM sessions
WHERE workspace_id = CAST(sqlc.arg(workspace_id) AS TEXT)
  AND (
      NOT json_valid(metadata_json)
      OR NULLIF(json_extract(metadata_json, '$.workspace_root'), '') IS NULL
      OR NULLIF(json_extract(metadata_json, '$.workspace_container'), '') IS NULL
  );

-- name: ListWorktreesByWorkspaceID :many
SELECT
    wt.id,
    wt.workspace_id,
    wt.canonical_root_path,
    CASE WHEN wt.canonical_root_path = w.canonical_root_path THEN 1 ELSE 0 END AS is_main,
    wt.managed,
    wt.created_branch,
    wt.origin_session_id,
    wt.git_metadata_json,
    wt.creation_base_commit_oid,
    wt.created_at_unix_ms,
    wt.updated_at_unix_ms
FROM worktrees wt
JOIN workspaces w ON w.id = wt.workspace_id
WHERE wt.workspace_id = sqlc.arg(workspace_id)
ORDER BY wt.created_at_unix_ms ASC, wt.rowid ASC;

-- name: ListManagedWorktreeRoots :many
SELECT wt.canonical_root_path
FROM worktrees wt
WHERE wt.managed <> 0
ORDER BY wt.created_at_unix_ms ASC, wt.rowid ASC;

-- name: GetWorktreeByID :one
SELECT
    wt.id,
    wt.workspace_id,
    wt.canonical_root_path,
    CASE WHEN wt.canonical_root_path = w.canonical_root_path THEN 1 ELSE 0 END AS is_main,
    wt.managed,
    wt.created_branch,
    wt.origin_session_id,
    wt.git_metadata_json,
    wt.creation_base_commit_oid,
    wt.created_at_unix_ms,
    wt.updated_at_unix_ms
FROM worktrees wt
JOIN workspaces w ON w.id = wt.workspace_id
WHERE wt.id = sqlc.arg(id)
LIMIT 1;

-- name: GetWorktreeByCanonicalRoot :one
SELECT
    wt.id,
    wt.workspace_id,
    wt.canonical_root_path,
    CASE WHEN wt.canonical_root_path = w.canonical_root_path THEN 1 ELSE 0 END AS is_main,
    wt.managed,
    wt.created_branch,
    wt.origin_session_id,
    wt.git_metadata_json,
    wt.creation_base_commit_oid,
    wt.created_at_unix_ms,
    wt.updated_at_unix_ms
FROM worktrees wt
JOIN workspaces w ON w.id = wt.workspace_id
WHERE wt.canonical_root_path = sqlc.arg(canonical_root_path)
LIMIT 1;

-- name: UpsertWorktree :exec
INSERT INTO worktrees (
    id,
    workspace_id,
    canonical_root_path,
    managed,
    created_branch,
    origin_session_id,
    git_metadata_json,
    creation_base_commit_oid,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES (
    sqlc.arg(id),
    sqlc.arg(workspace_id),
    sqlc.arg(canonical_root_path),
    sqlc.arg(managed),
    sqlc.arg(created_branch),
    sqlc.arg(origin_session_id),
    sqlc.arg(git_metadata_json),
    sqlc.narg(creation_base_commit_oid),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms)
)
ON CONFLICT(canonical_root_path) DO UPDATE SET
    workspace_id = excluded.workspace_id,
    managed = excluded.managed,
    created_branch = excluded.created_branch,
    origin_session_id = excluded.origin_session_id,
    git_metadata_json = excluded.git_metadata_json,
    updated_at_unix_ms = excluded.updated_at_unix_ms;

-- name: DeleteWorktreeByID :execrows
DELETE FROM worktrees
WHERE id = sqlc.arg(id);

-- name: UpdateWorktreeCanonicalRoot :execrows
UPDATE worktrees
SET
    canonical_root_path = sqlc.arg(canonical_root_path),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: UpsertSession :exec
INSERT INTO sessions (
    id,
    project_id,
    workspace_id,
    worktree_id,
    artifact_relpath,
    name,
    first_prompt_preview,
    input_draft,
    previous_session_id,
    parent_agent_session_id,
    category,
    created_at_unix_ms,
    updated_at_unix_ms,
    last_sequence,
    model_request_count,
    launch_visible,
    cwd_relpath,
    continuation_json,
    locked_json,
    usage_state_json,
    metadata_json
) VALUES (
    sqlc.arg(id),
    sqlc.arg(project_id),
    sqlc.arg(workspace_id),
    sqlc.narg(worktree_id),
    sqlc.arg(artifact_relpath),
    sqlc.arg(name),
    sqlc.arg(first_prompt_preview),
    sqlc.arg(input_draft),
    sqlc.narg(previous_session_id),
    sqlc.narg(parent_agent_session_id),
    sqlc.narg(category),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms),
    sqlc.arg(last_sequence),
    sqlc.arg(model_request_count),
    sqlc.arg(launch_visible),
    sqlc.arg(cwd_relpath),
    sqlc.arg(continuation_json),
    sqlc.arg(locked_json),
    sqlc.arg(usage_state_json),
    sqlc.arg(metadata_json)
)
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    first_prompt_preview = excluded.first_prompt_preview,
    input_draft = excluded.input_draft,
    previous_session_id = excluded.previous_session_id,
    parent_agent_session_id = excluded.parent_agent_session_id,
    category = excluded.category,
    updated_at_unix_ms = MAX(sessions.updated_at_unix_ms, excluded.updated_at_unix_ms),
    last_sequence = excluded.last_sequence,
    model_request_count = excluded.model_request_count,
    launch_visible = CASE
        WHEN sessions.launch_visible <> 0 OR excluded.launch_visible <> 0 THEN 1
        ELSE 0
    END,
    continuation_json = excluded.continuation_json,
    locked_json = excluded.locked_json,
    usage_state_json = excluded.usage_state_json,
    metadata_json = excluded.metadata_json;

-- name: ReconcileSessionEventLog :execrows
UPDATE sessions
SET
    last_sequence = sqlc.arg(last_sequence),
    updated_at_unix_ms = MAX(updated_at_unix_ms, sqlc.arg(updated_at_unix_ms)),
    usage_state_json = CASE
        WHEN sqlc.arg(invalidate_usage_state) <> 0 THEN 'null'
        ELSE usage_state_json
    END,
    metadata_json = json_set(
        CASE WHEN json_valid(metadata_json) THEN metadata_json ELSE '{}' END,
        '$.conversation_established',
        json(CASE WHEN sqlc.arg(conversation_established) <> 0 THEN 'true' ELSE 'false' END)
    )
WHERE id = sqlc.arg(session_id)
  AND last_sequence = sqlc.arg(observed_last_sequence);

-- name: GetProjectDisplayName :one
SELECT display_name
FROM projects
WHERE id = sqlc.arg(project_id)
LIMIT 1;

-- name: SetProjectDisplayName :execrows
UPDATE projects
SET
    display_name = sqlc.arg(display_name),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(project_id);

-- name: CountProjectWorkspaces :one
SELECT CAST(COUNT(*) AS INTEGER) AS workspace_count
FROM workspaces
WHERE project_id = sqlc.arg(project_id);

-- name: GetProjectPrimaryWorkspaceID :one
SELECT primary_workspace_id
FROM projects
WHERE id = sqlc.arg(project_id)
LIMIT 1;

-- name: SetProjectPrimaryWorkspace :execrows
UPDATE projects
SET
    primary_workspace_id = sqlc.arg(workspace_id),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(project_id);

-- name: ListProjects :many
SELECT
    p.id,
    p.display_name,
    p.project_key,
    COALESCE(w.canonical_root_path, '') AS root_path,
    CAST(COALESCE(COUNT(s.id), 0) AS INTEGER) AS session_count,
    COALESCE(MAX(s.updated_at_unix_ms), p.updated_at_unix_ms) AS latest_activity_unix_ms
FROM projects p
LEFT JOIN workspaces w ON w.id = p.primary_workspace_id AND w.project_id = p.id
LEFT JOIN sessions s ON s.project_id = p.id AND s.launch_visible <> 0
GROUP BY p.id, p.display_name, p.project_key, w.canonical_root_path, p.updated_at_unix_ms
ORDER BY latest_activity_unix_ms DESC;

-- name: ListProjectHomeSummaries :many
SELECT
    p.id AS project_id,
    p.project_key,
    p.display_name,
    COALESCE(w.id, '') AS primary_workspace_id,
    COALESCE(w.canonical_root_path, '') AS primary_workspace_root_path,
    CAST(COALESCE(w.updated_at_unix_ms, p.updated_at_unix_ms) AS INTEGER) AS primary_workspace_updated_at_unix_ms,
    default_workflow.workflow_id AS default_workflow_id,
    COALESCE(default_workflow.workflow_name, '') AS default_workflow_name,
    CASE WHEN default_workflow.workflow_id IS NULL THEN 0 ELSE 1 END AS default_workflow_valid,
    CAST(MAX(
        p.updated_at_unix_ms,
        COALESCE(w.updated_at_unix_ms, 0),
        COALESCE((SELECT MAX(t.updated_at_unix_ms) FROM task_records t WHERE t.project_id = p.id), 0),
        COALESCE((
            SELECT MAX(tc.updated_at_unix_ms)
            FROM task_comments tc
            JOIN task_records comment_tasks ON comment_tasks.id = tc.task_id
            WHERE comment_tasks.project_id = p.id
        ), 0),
        COALESCE((SELECT MAX(s.updated_at_unix_ms) FROM sessions s WHERE s.project_id = p.id AND s.launch_visible <> 0), 0),
        COALESCE((SELECT MAX(pwl.updated_at_unix_ms) FROM project_workflow_links pwl WHERE pwl.project_id = p.id), 0)
    ) AS INTEGER) AS latest_activity_unix_ms,
    CAST((SELECT COUNT(*) FROM task_records t WHERE t.project_id = p.id) AS INTEGER) AS task_count,
    CAST((
        SELECT COUNT(*)
        FROM task_records attention_tasks
        JOIN workflow_task_status_records attention_status ON attention_status.task_id = attention_tasks.id
        WHERE attention_tasks.project_id = p.id
          AND json_array_length(attention_status.attention_types_json) > 0
    ) AS INTEGER) AS attention_count,
    CAST((
        SELECT COUNT(*)
        FROM project_workflow_links pwl
        WHERE pwl.project_id = p.id
    ) AS INTEGER) AS workflow_count
FROM projects p
LEFT JOIN workspaces w ON w.id = p.primary_workspace_id AND w.project_id = p.id
JOIN project_default_workflow_identity default_workflow ON default_workflow.project_id = p.id
WHERE (sqlc.arg(project_id) = '' OR p.id = sqlc.arg(project_id))
ORDER BY latest_activity_unix_ms DESC, p.rowid DESC
LIMIT sqlc.arg(limit_rows)
OFFSET sqlc.arg(offset_rows);

-- name: GetProjectSummary :one
SELECT
    p.id,
    p.display_name,
    p.project_key,
    COALESCE(w.canonical_root_path, '') AS root_path,
    CAST(COALESCE(COUNT(s.id), 0) AS INTEGER) AS session_count,
    COALESCE(MAX(s.updated_at_unix_ms), p.updated_at_unix_ms) AS latest_activity_unix_ms
FROM projects p
LEFT JOIN workspaces w ON w.id = p.primary_workspace_id AND w.project_id = p.id
LEFT JOIN sessions s ON s.project_id = p.id AND s.launch_visible <> 0
WHERE p.id = sqlc.arg(project_id)
GROUP BY p.id, p.display_name, p.project_key, w.canonical_root_path, p.updated_at_unix_ms
LIMIT 1;

-- name: ListProjectWorkspaces :many
SELECT
    w.id,
    w.canonical_root_path AS root_path,
    CASE WHEN w.id = p.primary_workspace_id THEN 1 ELSE 0 END AS is_primary,
    CAST(COALESCE(COUNT(s.id), 0) AS INTEGER) AS session_count,
    COALESCE(MAX(s.updated_at_unix_ms), w.updated_at_unix_ms) AS latest_activity_unix_ms,
    w.created_at_unix_ms AS attached_at_unix_ms,
    w.id AS workspace_order_id
FROM workspaces w
JOIN projects p ON p.id = w.project_id
LEFT JOIN sessions s ON s.workspace_id = w.id AND s.launch_visible <> 0
JOIN (
    SELECT recent.id
    FROM workspaces recent
    WHERE recent.project_id = sqlc.arg(project_id)
    ORDER BY recent.created_at_unix_ms DESC, recent.rowid DESC
    LIMIT sqlc.arg(workspace_collection_limit)
) recent_workspaces ON recent_workspaces.id = w.id
WHERE w.project_id = sqlc.arg(project_id)
GROUP BY w.id, w.canonical_root_path, p.primary_workspace_id, w.updated_at_unix_ms, w.created_at_unix_ms
ORDER BY CASE WHEN w.id = p.primary_workspace_id THEN 1 ELSE 0 END DESC, latest_activity_unix_ms DESC, w.created_at_unix_ms ASC, w.rowid ASC;

-- name: ListProjectWorkspaceBoundary :many
SELECT
    w.id,
    w.canonical_root_path AS root_path
FROM workspaces w
WHERE w.project_id = sqlc.arg(project_id)
ORDER BY w.created_at_unix_ms DESC, w.rowid DESC
LIMIT sqlc.arg(workspace_collection_limit);

-- name: ListProjectWorkspacesPage :many
SELECT
    w.id,
    w.canonical_root_path AS root_path,
    CASE WHEN w.id = p.primary_workspace_id THEN 1 ELSE 0 END AS is_primary,
    CAST(COALESCE(COUNT(s.id), 0) AS INTEGER) AS session_count,
    COALESCE(MAX(s.updated_at_unix_ms), w.updated_at_unix_ms) AS latest_activity_unix_ms
FROM workspaces w
JOIN projects p ON p.id = w.project_id
LEFT JOIN sessions s ON s.workspace_id = w.id AND s.launch_visible <> 0
JOIN (
    SELECT recent.id
    FROM workspaces recent
    WHERE recent.project_id = sqlc.arg(project_id)
    ORDER BY recent.created_at_unix_ms DESC, recent.rowid DESC
    LIMIT sqlc.arg(workspace_collection_limit)
) recent_workspaces ON recent_workspaces.id = w.id
WHERE w.project_id = sqlc.arg(project_id)
GROUP BY w.id, w.canonical_root_path, p.primary_workspace_id, w.updated_at_unix_ms
ORDER BY CASE WHEN w.id = p.primary_workspace_id THEN 1 ELSE 0 END DESC, w.created_at_unix_ms DESC, w.rowid DESC
LIMIT sqlc.arg(limit_rows)
OFFSET sqlc.arg(offset_rows);

-- name: ListNewestSessionPage :many
SELECT
    id,
    name,
    first_prompt_preview,
    COALESCE(category, 'main') AS category,
    updated_at_unix_ms
FROM sessions
WHERE project_id = sqlc.arg(project_id)
  AND launch_visible <> 0
  AND COALESCE(category, 'main') = sqlc.arg(category)
ORDER BY updated_at_unix_ms DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListOlderSessionPage :many
SELECT
    id,
    name,
    first_prompt_preview,
    COALESCE(category, 'main') AS category,
    updated_at_unix_ms
FROM sessions
WHERE project_id = sqlc.arg(project_id)
  AND launch_visible <> 0
  AND COALESCE(category, 'main') = sqlc.arg(category)
  AND (
      updated_at_unix_ms < sqlc.arg(boundary_updated_at_unix_ms)
      OR (
          updated_at_unix_ms = sqlc.arg(boundary_updated_at_unix_ms)
          AND id < sqlc.arg(boundary_session_id)
      )
  )
ORDER BY updated_at_unix_ms DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListNewerSessionPage :many
SELECT
    id,
    name,
    first_prompt_preview,
    COALESCE(category, 'main') AS category,
    updated_at_unix_ms
FROM sessions
WHERE project_id = sqlc.arg(project_id)
  AND launch_visible <> 0
  AND COALESCE(category, 'main') = sqlc.arg(category)
  AND (
      updated_at_unix_ms > sqlc.arg(boundary_updated_at_unix_ms)
      OR (
          updated_at_unix_ms = sqlc.arg(boundary_updated_at_unix_ms)
          AND id > sqlc.arg(boundary_session_id)
      )
  )
ORDER BY updated_at_unix_ms ASC, id ASC
LIMIT sqlc.arg(page_limit);

-- name: ListProjectSessionIDs :many
SELECT id
FROM sessions
WHERE project_id = sqlc.arg(project_id)
ORDER BY rowid ASC;

-- name: ListProjectSessionArtifacts :many
SELECT
    id,
    artifact_relpath
FROM sessions
WHERE project_id = sqlc.arg(project_id)
  AND trim(artifact_relpath) != ''
ORDER BY rowid ASC;

-- name: GetProjectDeleteBlockerCounts :one
SELECT
    CAST((
        SELECT COUNT(DISTINCT id)
        FROM (
            SELECT t.id
            FROM task_records t
            JOIN task_current_nodes current_node ON current_node.task_id = t.id
            JOIN workflow_nodes node ON node.id = current_node.node_id
            WHERE t.project_id = sqlc.arg(delete_project_id)
              -- Backlog/start-node tasks are drafts, not active project work.
              AND node.kind NOT IN ('start', 'terminal')
            UNION
            SELECT t.id
            FROM task_records t
            JOIN task_pending_approvals approval ON approval.source_task_id = t.id
            WHERE t.project_id = sqlc.arg(delete_project_id)
        )
    ) AS INTEGER) AS non_terminal_tasks;

-- name: AcquireProjectDeleteWriteLock :execrows
UPDATE projects
SET updated_at_unix_ms = updated_at_unix_ms
WHERE id = sqlc.arg(project_id);

-- name: DeleteProjectTasks :exec
DELETE FROM tasks
WHERE id IN (
    SELECT id FROM task_records WHERE project_id = sqlc.arg(project_id)
);

-- name: DeleteProjectTaskPendingApprovals :execrows
DELETE FROM task_pending_approvals
WHERE source_task_id IN (
    SELECT id
    FROM task_records
    WHERE project_id = sqlc.arg(project_id)
);

-- name: DeleteProject :execrows
DELETE FROM projects
WHERE id = sqlc.arg(project_id);

-- name: GetSessionRecordByID :one
SELECT
    s.id,
    s.artifact_relpath,
    s.name,
    s.first_prompt_preview,
    s.input_draft,
    s.previous_session_id,
    s.parent_agent_session_id,
    s.category,
    s.created_at_unix_ms,
    s.updated_at_unix_ms,
    s.last_sequence,
    s.model_request_count,
    s.continuation_json,
    s.locked_json,
    s.usage_state_json,
    s.metadata_json,
    COALESCE(w.canonical_root_path, json_extract(s.metadata_json, '$.workspace_root'), '') AS workspace_root
FROM sessions s
LEFT JOIN workspaces w ON w.id = s.workspace_id
WHERE s.id = sqlc.arg(session_id)
LIMIT 1;

-- name: GetSessionExecutionTargetByID :one
SELECT
    s.id AS session_id,
    s.workspace_id AS execution_target_workspace_binding,
    s.project_id,
    COALESCE(s.workspace_id, '') AS workspace_id,
    CAST(COALESCE(json_extract(s.metadata_json, '$.workspace_container'), '') AS TEXT) AS workspace_snapshot_name,
    COALESCE(w.canonical_root_path, json_extract(s.metadata_json, '$.workspace_root'), '') AS workspace_root,
    s.worktree_id,
    wt.canonical_root_path AS worktree_root,
    s.cwd_relpath
FROM sessions s
LEFT JOIN workspaces w ON w.id = s.workspace_id
LEFT JOIN worktrees wt ON wt.id = s.worktree_id
WHERE s.id = sqlc.arg(session_id)
LIMIT 1;

-- name: GetSessionWorkspaceRetargetStateByID :one
SELECT
    s.id AS session_id,
    s.project_id,
    p.display_name AS project_display_name,
    p.project_key,
    s.artifact_relpath
FROM sessions s
JOIN projects p ON p.id = s.project_id
WHERE s.id = sqlc.arg(session_id)
LIMIT 1;

-- name: ListSessionWorkflowTaskIDs :many
SELECT task_id
FROM sessions
WHERE id = sqlc.arg(session_id)
  AND task_id IS NOT NULL
ORDER BY task_id ASC;

-- name: BindSessionToTask :execrows
UPDATE sessions
SET task_id = sqlc.arg(task_id)
WHERE sessions.id = sqlc.arg(session_id)
  AND EXISTS (
      SELECT 1
      FROM task_records task
      WHERE task.id = sqlc.arg(task_id)
        AND task.project_id = sessions.project_id
  )
  AND (
      task_id IS NULL
      OR task_id = sqlc.arg(task_id)
  );

-- name: BindSessionToSerialCurrentNode :execrows
UPDATE task_current_nodes
SET session_id = sqlc.arg(session_id)
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key IS NULL
  AND (
      session_id = sqlc.arg(session_id)
      OR (
          CAST(sqlc.narg(expected_current_session_id) AS TEXT) IS NULL
          AND session_id IS NULL
      )
      OR session_id = CAST(sqlc.narg(expected_current_session_id) AS TEXT)
  );

-- name: BindSessionToBranchCurrentNode :execrows
UPDATE task_current_nodes
SET session_id = sqlc.arg(session_id)
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key = sqlc.arg(transition_branch_key)
  AND (
      session_id = sqlc.arg(session_id)
      OR (
          CAST(sqlc.narg(expected_current_session_id) AS TEXT) IS NULL
          AND session_id IS NULL
      )
      OR session_id = CAST(sqlc.narg(expected_current_session_id) AS TEXT)
  );

-- name: UpsertSerialSessionWorkflowNodeAssociation :exec
INSERT INTO session_workflow_node_associations (
    session_id,
    node_id,
    transition_branch_key,
    associated_at_unix_ms
) VALUES (
    sqlc.arg(session_id),
    sqlc.arg(node_id),
    NULL,
    sqlc.arg(associated_at_unix_ms)
)
ON CONFLICT(session_id, node_id) WHERE transition_branch_key IS NULL DO UPDATE SET
    associated_at_unix_ms = excluded.associated_at_unix_ms;

-- name: UpsertBranchSessionWorkflowNodeAssociation :exec
INSERT INTO session_workflow_node_associations (
    session_id,
    node_id,
    transition_branch_key,
    associated_at_unix_ms
) VALUES (
    sqlc.arg(session_id),
    sqlc.arg(node_id),
    sqlc.arg(transition_branch_key),
    sqlc.arg(associated_at_unix_ms)
)
ON CONFLICT(session_id, node_id, transition_branch_key) WHERE transition_branch_key IS NOT NULL DO UPDATE SET
    associated_at_unix_ms = excluded.associated_at_unix_ms;

-- name: CountTaskSessions :one
SELECT CAST(COUNT(*) AS INTEGER) AS session_count
FROM sessions
WHERE task_id = sqlc.arg(task_id);

-- name: GetLatestSerialTaskSessionAssociationForNode :one
SELECT
    association.session_id,
    association.node_id,
    association.associated_at_unix_ms
FROM session_workflow_node_associations association
JOIN sessions session ON session.id = association.session_id
WHERE session.task_id = sqlc.arg(task_id)
  AND association.node_id = sqlc.arg(node_id)
  AND association.transition_branch_key IS NULL
ORDER BY association.associated_at_unix_ms DESC, association.session_id DESC
LIMIT 1;

-- name: GetLatestBranchTaskSessionAssociationForNode :one
SELECT
    association.session_id,
    association.node_id,
    association.transition_branch_key,
    association.associated_at_unix_ms
FROM session_workflow_node_associations association
JOIN sessions session ON session.id = association.session_id
WHERE session.task_id = sqlc.arg(task_id)
  AND association.node_id = sqlc.arg(node_id)
  AND association.transition_branch_key = sqlc.arg(transition_branch_key)
ORDER BY association.associated_at_unix_ms DESC, association.session_id DESC
LIMIT 1;

-- name: RetargetSessionWorkspaceProject :execrows
UPDATE sessions
SET
    project_id = sqlc.arg(target_project_id),
    workspace_id = sqlc.arg(target_workspace_id),
    worktree_id = NULL,
    cwd_relpath = '.',
    artifact_relpath = sqlc.arg(target_artifact_relpath),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    metadata_json = json_remove(
        json_set(
            CASE WHEN json_valid(metadata_json) THEN metadata_json ELSE '{}' END,
            '$.workspace_root', CAST(sqlc.arg(target_workspace_root) AS TEXT),
            '$.workspace_container', CAST(sqlc.arg(target_workspace_container) AS TEXT),
            '$.worktree_reminder', json('null')
        ),
        '$.workflow_session'
    )
WHERE id = sqlc.arg(session_id)
  AND project_id = sqlc.arg(source_project_id)
  AND artifact_relpath = sqlc.arg(source_artifact_relpath);

-- name: UpdateSessionExecutionTargetByID :execrows
UPDATE sessions
SET
    workspace_id = sqlc.arg(workspace_id),
    worktree_id = sqlc.narg(worktree_id),
    cwd_relpath = sqlc.arg(cwd_relpath)
WHERE id = sqlc.arg(session_id);

-- name: DeleteSessionRecordByID :execrows
DELETE FROM sessions
WHERE id = sqlc.arg(session_id);

-- name: AcquireWorkspaceRegistrationLock :execrows
UPDATE projects
SET updated_at_unix_ms = updated_at_unix_ms
WHERE id = '';

-- name: ListSessionsTargetingWorktree :many
SELECT
    id,
    name,
    updated_at_unix_ms
FROM sessions
WHERE worktree_id = sqlc.arg(worktree_id)
ORDER BY updated_at_unix_ms DESC, rowid DESC;

-- name: InsertSessionPromptHistoryEntry :execrows
INSERT INTO session_prompt_history_entries (
    session_id,
    source_id,
    text,
    created_at_unix_ms
) VALUES (
    sqlc.arg(session_id),
    sqlc.arg(source_id),
    sqlc.arg(text),
    sqlc.arg(created_at_unix_ms)
)
ON CONFLICT DO NOTHING;

-- name: GetSessionPromptHistoryEntryBySourceID :one
SELECT
    sequence,
    session_id,
    source_id,
    text,
    created_at_unix_ms
FROM session_prompt_history_entries
WHERE session_id = sqlc.arg(session_id)
  AND source_id = sqlc.arg(source_id)
LIMIT 1;

-- name: ListSessionPromptHistoryText :many
SELECT text
FROM (
    SELECT
        sequence,
        text
    FROM session_prompt_history_entries
    WHERE session_id = sqlc.arg(session_id)
    ORDER BY sequence DESC
    LIMIT sqlc.arg(max_entries)
)
ORDER BY sequence ASC;

-- name: ListWorkflowDurableAttentionCandidates :many
WITH durable_attention (
    kind,
    id,
    project_id,
    workflow_id,
    task_id,
    short_id,
    title,
    approval_id,
    node_id,
    transition_branch_key,
    session_id,
    interruption_reason,
    interruption_detail_json,
    occurred_at_unix_ms
) AS (
    SELECT
        'approval' AS kind,
        CAST('approval:' || approval.id AS TEXT) AS id,
        task.project_id,
        task.workflow_id,
        task.id AS task_id,
        task.short_id,
        task.title,
        approval.id AS approval_id,
        CAST(NULL AS TEXT) AS node_id,
        CAST(NULL AS TEXT) AS transition_branch_key,
        approval.source_session_id AS session_id,
        CAST(NULL AS TEXT) AS interruption_reason,
        CAST(NULL AS TEXT) AS interruption_detail_json,
        approval.created_at_unix_ms AS occurred_at_unix_ms
    FROM task_pending_approvals approval
    JOIN task_records task ON task.id = approval.source_task_id
    WHERE (
        sqlc.narg(selected_task_id) IS NULL
        OR task.id = sqlc.narg(selected_task_id)
    )
      AND (
        CAST(sqlc.arg(cursor_active) AS INTEGER) = 0
        OR approval.created_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
        OR (
            approval.created_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms)
            AND ('approval:' || approval.id) < sqlc.arg(cursor_item_id)
        )
      )

    UNION ALL

    SELECT
        'interrupted' AS kind,
        CAST('interrupted:' || json_object(
            'task_id', current_node.task_id,
            'node_id', current_node.node_id,
            'transition_branch_key', current_node.transition_branch_key
        ) AS TEXT) AS id,
        task.project_id,
        task.workflow_id,
        task.id AS task_id,
        task.short_id,
        task.title,
        CAST(NULL AS TEXT) AS approval_id,
        CAST(current_node.node_id AS TEXT) AS node_id,
        CAST(current_node.transition_branch_key AS TEXT) AS transition_branch_key,
        current_node.session_id,
        CAST(current_node.interruption_reason AS TEXT) AS interruption_reason,
        CAST(current_node.interruption_detail_json AS TEXT) AS interruption_detail_json,
        current_node.interrupted_at_unix_ms AS occurred_at_unix_ms
    FROM task_current_nodes current_node
    JOIN task_records task ON task.id = current_node.task_id
    WHERE current_node.scheduling_state = 'interrupted'
      AND current_node.interruption_reason NOT IN ('user_interrupt', 'workflow_runtime_canceled')
      AND (
          sqlc.narg(selected_task_id) IS NULL
          OR task.id = sqlc.narg(selected_task_id)
      )
      AND (
          CAST(sqlc.arg(cursor_active) AS INTEGER) = 0
          OR current_node.interrupted_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (
              current_node.interrupted_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms)
              AND (
                  'interrupted:' || json_object(
                      'task_id', current_node.task_id,
                      'node_id', current_node.node_id,
                      'transition_branch_key', current_node.transition_branch_key
                  )
              ) < sqlc.arg(cursor_item_id)
          )
      )
)
SELECT
    durable_attention.kind,
    durable_attention.id,
    durable_attention.project_id,
    durable_attention.workflow_id,
    durable_attention.task_id,
    durable_attention.short_id,
    durable_attention.title,
    approval.id AS approval_id,
    durable_attention.node_id,
    durable_attention.transition_branch_key,
    durable_attention.session_id,
    durable_attention.interruption_reason,
    durable_attention.interruption_detail_json,
    durable_attention.occurred_at_unix_ms
FROM durable_attention
LEFT JOIN task_pending_approvals approval
  ON durable_attention.kind = 'approval'
 AND approval.id = durable_attention.approval_id
ORDER BY 14 DESC, 2 DESC
LIMIT CASE
    WHEN sqlc.arg(page_limit) = 0 THEN -1
    ELSE sqlc.arg(page_limit)
END;

-- name: ListWorkflowTaskActivityRows :many
SELECT
    CAST('comment:' || c.id AS TEXT) AS activity_id,
    'comment' AS kind,
    c.id AS source_id,
    c.updated_at_unix_ms AS occurred_at_unix_ms,
    c.updated_at_unix_ms AS updated_at_unix_ms,
    CAST(NULL AS TEXT) AS session_name
FROM task_comments c
WHERE c.task_id = sqlc.arg(task_id)

UNION ALL

SELECT
    CAST('session_started:' || s.id AS TEXT) AS activity_id,
    'session_started' AS kind,
    s.id AS source_id,
    s.created_at_unix_ms AS occurred_at_unix_ms,
    s.created_at_unix_ms AS updated_at_unix_ms,
    s.name AS session_name
FROM sessions s
WHERE s.task_id = sqlc.arg(task_id)
ORDER BY occurred_at_unix_ms DESC, activity_id DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: ListTaskCommentsByIDs :many
SELECT
    id,
    task_id,
    body,
    author_kind,
    author_id,
    created_at_unix_ms,
    updated_at_unix_ms
FROM task_comments
WHERE id IN (sqlc.slice('ids'));

-- name: ListSessionNamesByIDs :many
SELECT id, name
FROM sessions
WHERE id IN (sqlc.slice('ids'));

-- name: LockTaskExecutionTarget :execrows
UPDATE tasks
SET
    managed_worktree_id = sqlc.narg(managed_worktree_id),
    execution_target_mode = sqlc.narg(execution_target_mode),
    execution_target_requested_ref = sqlc.narg(execution_target_requested_ref),
    execution_target_resolved_ref = sqlc.narg(execution_target_resolved_ref),
    execution_target_commit_oid = sqlc.narg(execution_target_commit_oid),
    execution_target_provenance = sqlc.narg(execution_target_provenance),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(task_id)
  AND execution_target_mode IS NULL
  AND execution_target_requested_ref IS NULL
  AND execution_target_resolved_ref IS NULL
  AND execution_target_commit_oid IS NULL
  AND execution_target_provenance IS NULL
  AND managed_worktree_id IS sqlc.narg(expected_managed_worktree_id);

-- name: TouchTaskUpdatedAt :execrows
UPDATE tasks
SET updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(task_id);

-- name: GetTaskProjectWorkflowIDs :one
SELECT project_id, workflow_id
FROM task_records
WHERE id = sqlc.arg(task_id)
LIMIT 1;

-- name: GetTaskIdentityForComment :one
SELECT t.id AS task_id, t.project_id, t.workflow_id
FROM task_comments c
JOIN task_records t ON t.id = c.task_id
WHERE c.id = sqlc.arg(comment_id)
LIMIT 1;

-- name: ListMetadataSchemaDefinitions :many
SELECT
    type AS object_kind,
    name AS object_name,
    CAST(sql AS TEXT) AS ddl
FROM sqlite_schema
WHERE sql IS NOT NULL
  AND name != 'sqlite_sequence'
ORDER BY
    CASE type
        WHEN 'table' THEN 0
        WHEN 'view' THEN 1
        WHEN 'index' THEN 2
        WHEN 'trigger' THEN 3
    END,
    name;

-- name: ListTaskCurrentNodes :many
SELECT
    current_node.task_id,
    current_node.node_id,
    current_node.transition_branch_key,
    current_node.entered_by_edge_id,
    current_node.current_input_values_json,
    current_node.prior_node_values_json,
    current_node.session_id,
    current_node.scheduling_state,
    current_node.interruption_reason,
    current_node.interruption_detail_json,
    current_node.interrupted_at_unix_ms,
    current_node.effective_assignee,
    current_node.effective_thinking,
    current_node.assignee_origin,
    node.kind AS node_kind
FROM task_current_nodes current_node
JOIN workflow_nodes node ON node.id = current_node.node_id
WHERE current_node.task_id = sqlc.arg(task_id)
ORDER BY
    CASE WHEN current_node.transition_branch_key IS NULL THEN 0 ELSE 1 END,
    current_node.transition_branch_key;

-- name: ListTaskCurrentNodesByTasks :many
SELECT
    current_node.task_id,
    current_node.node_id,
    current_node.transition_branch_key,
    current_node.entered_by_edge_id,
    current_node.current_input_values_json,
    current_node.prior_node_values_json,
    current_node.session_id,
    current_node.scheduling_state,
    current_node.interruption_reason,
    current_node.interruption_detail_json,
    current_node.interrupted_at_unix_ms,
    current_node.effective_assignee,
    current_node.effective_thinking,
    current_node.assignee_origin,
    node.kind AS node_kind
FROM task_current_nodes current_node
JOIN workflow_nodes node ON node.id = current_node.node_id
WHERE current_node.task_id IN (sqlc.slice('task_ids'))
ORDER BY
    current_node.task_id,
    CASE WHEN current_node.transition_branch_key IS NULL THEN 0 ELSE 1 END,
    current_node.transition_branch_key;

-- name: AdmitSerialCurrentNode :execrows
UPDATE task_current_nodes
SET scheduling_state = 'admitted'
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key IS NULL
  AND scheduling_state = 'ready'
  AND NOT EXISTS (
      SELECT 1
      FROM task_pending_approvals approval
      WHERE approval.source_task_id = task_current_nodes.task_id
        AND approval.source_node_id = task_current_nodes.node_id
        AND approval.source_transition_branch_key IS NULL
  );

-- name: AdmitBranchCurrentNode :execrows
UPDATE task_current_nodes
SET scheduling_state = 'admitted'
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key = sqlc.arg(transition_branch_key)
  AND scheduling_state = 'ready'
  AND NOT EXISTS (
      SELECT 1
      FROM task_pending_approvals approval
      WHERE approval.source_task_id = task_current_nodes.task_id
        AND approval.source_node_id = task_current_nodes.node_id
        AND approval.source_transition_branch_key = task_current_nodes.transition_branch_key
  );

-- name: ResumeSerialCurrentNode :execrows
UPDATE task_current_nodes
SET scheduling_state = 'ready',
    interruption_reason = NULL,
    interruption_detail_json = NULL,
    interrupted_at_unix_ms = NULL
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key IS NULL
  AND scheduling_state = 'interrupted'
  AND NOT EXISTS (
      SELECT 1
      FROM task_pending_approvals approval
      WHERE approval.source_task_id = task_current_nodes.task_id
        AND approval.source_node_id = task_current_nodes.node_id
        AND approval.source_transition_branch_key IS NULL
  );

-- name: ResumeBranchCurrentNode :execrows
UPDATE task_current_nodes
SET scheduling_state = 'ready',
    interruption_reason = NULL,
    interruption_detail_json = NULL,
    interrupted_at_unix_ms = NULL
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key = sqlc.arg(transition_branch_key)
  AND scheduling_state = 'interrupted'
  AND NOT EXISTS (
      SELECT 1
      FROM task_pending_approvals approval
      WHERE approval.source_task_id = task_current_nodes.task_id
        AND approval.source_node_id = task_current_nodes.node_id
        AND approval.source_transition_branch_key = task_current_nodes.transition_branch_key
  );

-- name: InterruptSerialAdmittedCurrentNode :execrows
UPDATE task_current_nodes
SET scheduling_state = 'interrupted',
    interruption_reason = sqlc.arg(interruption_reason),
    interruption_detail_json = sqlc.arg(interruption_detail_json),
    interrupted_at_unix_ms = sqlc.arg(interrupted_at_unix_ms)
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key IS NULL
  AND scheduling_state = 'admitted';

-- name: InterruptBranchAdmittedCurrentNode :execrows
UPDATE task_current_nodes
SET scheduling_state = 'interrupted',
    interruption_reason = sqlc.arg(interruption_reason),
    interruption_detail_json = sqlc.arg(interruption_detail_json),
    interrupted_at_unix_ms = sqlc.arg(interrupted_at_unix_ms)
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key = sqlc.arg(transition_branch_key)
  AND scheduling_state = 'admitted';

-- name: InterruptSerialCurrentNode :execrows
UPDATE task_current_nodes
SET scheduling_state = 'interrupted',
    interruption_reason = sqlc.arg(interruption_reason),
    interruption_detail_json = sqlc.arg(interruption_detail_json),
    interrupted_at_unix_ms = sqlc.arg(interrupted_at_unix_ms)
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key IS NULL
  AND scheduling_state IN ('ready', 'admitted');

-- name: InterruptBranchCurrentNode :execrows
UPDATE task_current_nodes
SET scheduling_state = 'interrupted',
    interruption_reason = sqlc.arg(interruption_reason),
    interruption_detail_json = sqlc.arg(interruption_detail_json),
    interrupted_at_unix_ms = sqlc.arg(interrupted_at_unix_ms)
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key = sqlc.arg(transition_branch_key)
  AND scheduling_state IN ('ready', 'admitted');

-- name: RecoverExecutableCurrentNodes :many
UPDATE task_current_nodes
SET scheduling_state = 'interrupted',
    interruption_reason = sqlc.arg(interruption_reason),
    interruption_detail_json = sqlc.arg(interruption_detail_json),
    interrupted_at_unix_ms = sqlc.arg(interrupted_at_unix_ms)
WHERE scheduling_state IN ('ready', 'admitted')
  AND NOT EXISTS (
      SELECT 1
      FROM task_pending_approvals approval
      WHERE approval.source_task_id = task_current_nodes.task_id
        AND approval.source_node_id = task_current_nodes.node_id
        AND (
            (approval.source_transition_branch_key IS NULL AND task_current_nodes.transition_branch_key IS NULL)
            OR approval.source_transition_branch_key = task_current_nodes.transition_branch_key
        )
  )
RETURNING task_id, node_id, transition_branch_key;

-- name: InsertTaskCurrentNode :exec
INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    entered_by_edge_id,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms,
    effective_assignee,
    effective_thinking,
    assignee_origin
) VALUES (
    sqlc.arg(task_id),
    sqlc.arg(node_id),
    sqlc.arg(transition_branch_key),
    sqlc.arg(entered_by_edge_id),
    sqlc.arg(current_input_values_json),
    sqlc.arg(prior_node_values_json),
    sqlc.arg(session_id),
    sqlc.arg(scheduling_state),
    sqlc.arg(interruption_reason),
    sqlc.arg(interruption_detail_json),
    sqlc.arg(interrupted_at_unix_ms),
    sqlc.narg(effective_assignee),
    sqlc.narg(effective_thinking),
    sqlc.narg(assignee_origin)
);

-- name: DeleteSerialTaskCurrentNode :execrows
DELETE FROM task_current_nodes
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND transition_branch_key IS NULL;

-- name: DeleteTaskCurrentNode :execrows
DELETE FROM task_current_nodes
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND (
      (transition_branch_key IS NULL AND sqlc.narg(transition_branch_key) IS NULL)
      OR transition_branch_key = sqlc.narg(transition_branch_key)
  );

-- name: DeleteTaskCurrentNodes :execrows
DELETE FROM task_current_nodes
WHERE task_id = sqlc.arg(task_id);

-- name: InsertTaskActiveFanout :exec
INSERT INTO task_active_fanouts (task_id)
VALUES (sqlc.arg(task_id));

-- name: InsertTaskActiveFanoutBranch :exec
INSERT INTO task_active_fanout_branches (
    task_id,
    transition_branch_key,
    arrival_state,
    arrival_values_json
) VALUES (
    sqlc.arg(task_id),
    sqlc.arg(transition_branch_key),
    'pending',
    NULL
);

-- name: UpdateTaskActiveFanoutBranchArrival :execrows
UPDATE task_active_fanout_branches
SET
    arrival_state = 'arrived',
    arrival_values_json = sqlc.arg(arrival_values_json)
WHERE task_id = sqlc.arg(task_id)
  AND transition_branch_key = sqlc.arg(transition_branch_key)
  AND arrival_state = 'pending';

-- name: DeleteTaskActiveFanout :execrows
DELETE FROM task_active_fanouts
WHERE task_id = sqlc.arg(task_id);

-- name: InsertTaskPendingApproval :exec
INSERT INTO task_pending_approvals (
    id,
    source_task_id,
    source_node_id,
    source_transition_branch_key,
    source_session_id,
    workflow_version,
    transition_snapshot_json,
    materialized_values_json,
    created_at_unix_ms
) VALUES (
    sqlc.arg(id),
    sqlc.arg(source_task_id),
    sqlc.arg(source_node_id),
    sqlc.arg(source_transition_branch_key),
    sqlc.arg(source_session_id),
    sqlc.arg(workflow_version),
    sqlc.arg(transition_snapshot_json),
    sqlc.arg(materialized_values_json),
    sqlc.arg(created_at_unix_ms)
);

-- name: InsertTaskPendingApprovalBranch :exec
INSERT INTO task_pending_approval_branches (
    approval_id,
    transition_branch_key,
    target_snapshot_json,
    effective_edge_configuration_json,
    context_source_resolution_json
) VALUES (
    sqlc.arg(approval_id),
    sqlc.arg(transition_branch_key),
    sqlc.arg(target_snapshot_json),
    sqlc.arg(effective_edge_configuration_json),
    sqlc.arg(context_source_resolution_json)
);

-- name: DeleteTaskPendingApproval :execrows
DELETE FROM task_pending_approvals
WHERE id = sqlc.arg(id);

-- name: DeleteTaskPendingApprovalsByTask :execrows
DELETE FROM task_pending_approvals
WHERE source_task_id = sqlc.arg(task_id);

-- name: HasTaskPendingApprovalForCurrentNode :one
SELECT EXISTS (
    SELECT 1
    FROM task_pending_approvals
    WHERE source_task_id = sqlc.arg(task_id)
      AND source_node_id = sqlc.arg(node_id)
      AND (
          (source_transition_branch_key IS NULL AND sqlc.narg(transition_branch_key) IS NULL)
          OR source_transition_branch_key = sqlc.narg(transition_branch_key)
      )
);

-- name: GetTaskActiveFanout :one
SELECT task_id
FROM task_active_fanouts
WHERE task_id = sqlc.arg(task_id);

-- name: ListTaskActiveFanoutBranches :many
SELECT
    task_id,
    transition_branch_key,
    arrival_state,
    arrival_values_json
FROM task_active_fanout_branches
WHERE task_id = sqlc.arg(task_id)
ORDER BY transition_branch_key;

-- name: ListTaskPendingApprovals :many
SELECT
    id,
    source_task_id,
    source_node_id,
    source_transition_branch_key,
    source_session_id,
    workflow_version,
    transition_snapshot_json,
    materialized_values_json,
    created_at_unix_ms
FROM task_pending_approvals
WHERE source_task_id = sqlc.arg(task_id)
ORDER BY created_at_unix_ms, id;

-- name: ListTaskPendingApprovalsByTasks :many
SELECT
    id,
    source_task_id,
    source_node_id,
    source_transition_branch_key,
    source_session_id,
    workflow_version,
    transition_snapshot_json,
    materialized_values_json,
    created_at_unix_ms
FROM task_pending_approvals
WHERE source_task_id IN (
    SELECT CAST(value AS TEXT)
    FROM json_each(sqlc.arg(task_ids_json))
)
ORDER BY source_task_id, created_at_unix_ms, id;

-- name: GetTaskPendingApproval :one
SELECT
    id,
    source_task_id,
    source_node_id,
    source_transition_branch_key,
    source_session_id,
    workflow_version,
    transition_snapshot_json,
    materialized_values_json,
    created_at_unix_ms
FROM task_pending_approvals
WHERE id = sqlc.arg(id);

-- name: ListTaskPendingApprovalBranches :many
SELECT
    approval_id,
    transition_branch_key,
    target_snapshot_json,
    effective_edge_configuration_json,
    context_source_resolution_json
FROM task_pending_approval_branches
WHERE approval_id = sqlc.arg(approval_id)
ORDER BY transition_branch_key;
-- name: GetTaskDependency :one
SELECT blocker_task_id, blocked_task_id
FROM task_dependencies
WHERE blocker_task_id = sqlc.arg(blocker_task_id)
  AND blocked_task_id = sqlc.arg(blocked_task_id)
LIMIT 1;


-- name: AcquireTaskDependencyWriteLock :one
UPDATE projects
SET updated_at_unix_ms = updated_at_unix_ms
WHERE id = (
    SELECT task_records.project_id
    FROM task_records
    WHERE task_records.id = sqlc.arg(task_id)
)
RETURNING id;


-- name: CountTaskDependenciesByBlocker :one
SELECT COUNT(*) AS dependency_count
FROM task_dependencies
WHERE blocker_task_id = sqlc.arg(blocker_task_id);


-- name: CountTaskDependenciesByBlocked :one
SELECT COUNT(*) AS dependency_count
FROM task_dependencies
WHERE blocked_task_id = sqlc.arg(blocked_task_id);


-- name: InsertTaskDependency :exec
INSERT INTO task_dependencies (blocker_task_id, blocked_task_id)
VALUES (sqlc.arg(blocker_task_id), sqlc.arg(blocked_task_id));


-- name: DeleteTaskDependency :execrows
DELETE FROM task_dependencies
WHERE blocker_task_id = sqlc.arg(blocker_task_id)
  AND blocked_task_id = sqlc.arg(blocked_task_id);


-- name: ListTaskDependencyProjectionRows :many
SELECT
    CAST(
        CASE
            WHEN td.blocked_task_id = sqlc.arg(task_id) THEN 'blocked-by'
            ELSE 'blocks'
        END AS TEXT
    ) AS direction,
    related.id AS task_id,
    related.short_id,
    related.title,
    CAST(related.workflow_id AS BLOB) AS workflow_id
FROM task_dependencies td
JOIN task_records related
  ON related.id = CASE
      WHEN td.blocked_task_id = sqlc.arg(task_id) THEN td.blocker_task_id
      ELSE td.blocked_task_id
  END
WHERE td.blocker_task_id = sqlc.arg(task_id)
   OR td.blocked_task_id = sqlc.arg(task_id)
ORDER BY direction ASC, related.short_id ASC, related.id ASC
LIMIT 101;


-- name: ListTaskDependencyBlockedByProjectionRows :many
SELECT
    CAST('blocked-by' AS TEXT) AS direction,
    related.id AS task_id,
    related.short_id,
    related.title,
    CAST(related.workflow_id AS BLOB) AS workflow_id
FROM task_dependencies td
JOIN task_records related ON related.id = td.blocker_task_id
WHERE td.blocked_task_id = sqlc.arg(task_id)
ORDER BY related.short_id ASC, related.id ASC
LIMIT 51;


-- name: ListTaskDependencyNeighborIDs :many
SELECT DISTINCT
    CAST(CASE
        WHEN td.blocker_task_id = sqlc.arg(task_id) THEN td.blocked_task_id
        ELSE td.blocker_task_id
    END AS TEXT) AS task_id
FROM task_dependencies td
WHERE td.blocker_task_id = sqlc.arg(task_id)
   OR td.blocked_task_id = sqlc.arg(task_id)
ORDER BY task_id ASC
LIMIT 100;


-- name: DeleteTaskDependenciesByTask :execrows
DELETE FROM task_dependencies
WHERE blocker_task_id = sqlc.arg(task_id)
   OR blocked_task_id = sqlc.arg(task_id);


-- name: ListTaskDependencyProgressByTasks :many
SELECT
    td.blocked_task_id AS task_id,
    CAST(COUNT(*) AS INTEGER) AS dependency_total_count,
    CAST(SUM(CASE WHEN (
        SELECT status.is_done
        FROM workflow_task_status_records status
        WHERE status.task_id = td.blocker_task_id
        LIMIT 1
    ) != 0 THEN 1 ELSE 0 END) AS INTEGER) AS dependency_satisfied_count
FROM task_dependencies td INDEXED BY task_dependencies_reverse_idx
WHERE td.blocked_task_id IN (sqlc.slice('task_ids'))
GROUP BY td.blocked_task_id;


-- name: TouchTasksUpdatedAt :execrows
UPDATE tasks
SET updated_at_unix_ms = MAX(
    updated_at_unix_ms + 1,
    sqlc.arg(updated_at_unix_ms)
)
WHERE id IN (sqlc.slice('task_ids'));

-- name: AdvanceTaskUpdatedAt :execrows
UPDATE tasks
SET updated_at_unix_ms = MAX(
    updated_at_unix_ms + 1,
    sqlc.arg(updated_at_unix_ms)
)
WHERE id = sqlc.arg(task_id);


-- name: AcquireWorkflowDependencyWriteLock :execrows
UPDATE projects
SET updated_at_unix_ms = updated_at_unix_ms
WHERE id IN (
    SELECT DISTINCT task_records.project_id
    FROM task_records
    WHERE task_records.workflow_id = sqlc.arg(workflow_id)
);


-- name: TouchWorkflowDependencySurvivors :execrows
UPDATE tasks
SET updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id IN (
    SELECT DISTINCT CAST(
        CASE
            WHEN td.blocker_task_id IN (
                SELECT id
                FROM task_records
                WHERE task_records.workflow_id = sqlc.arg(workflow_id)
            ) THEN td.blocked_task_id
            ELSE td.blocker_task_id
        END AS TEXT
    ) AS task_id
    FROM task_dependencies td
    WHERE (
        td.blocker_task_id IN (
            SELECT id
            FROM task_records
            WHERE task_records.workflow_id = sqlc.arg(workflow_id)
        )
        OR td.blocked_task_id IN (
            SELECT id
            FROM task_records
            WHERE task_records.workflow_id = sqlc.arg(workflow_id)
        )
    )
    AND (
        td.blocker_task_id NOT IN (
            SELECT id
            FROM task_records
            WHERE task_records.workflow_id = sqlc.arg(workflow_id)
        )
        OR td.blocked_task_id NOT IN (
            SELECT id
            FROM task_records
            WHERE task_records.workflow_id = sqlc.arg(workflow_id)
        )
    )
);


-- name: DeleteWorkflowTaskDependenciesByWorkflowID :execrows
DELETE FROM task_dependencies
WHERE blocker_task_id IN (
    SELECT id
    FROM task_records
    WHERE task_records.workflow_id = sqlc.arg(workflow_id)
)
OR blocked_task_id IN (
    SELECT id
    FROM task_records
    WHERE task_records.workflow_id = sqlc.arg(workflow_id)
);

-- name: ListBoardNodeTasks :many
WITH board_node_tasks AS (
    SELECT
        t.id,
        t.project_id,
        t.project_workflow_link_id,
        t.workflow_id,
        t.workflow_revision_seen,
        t.task_seq,
        t.short_id,
        t.title,
        t.body,
        t.source_url,
        t.source_workspace_id,
        t.managed_worktree_id,
        t.execution_target_mode,
        t.execution_target_requested_ref,
        t.execution_target_resolved_ref,
        t.execution_target_commit_oid,
        t.execution_target_provenance,
        t.created_at_unix_ms,
        t.updated_at_unix_ms,
        t.metadata_json,

(
    SELECT group_concat(printf('%03d', ordered_label.ordinal), '')
    FROM (
        SELECT label.ordinal
        FROM task_label_assignments assignment
        JOIN project_labels label ON label.id = assignment.label_id
        WHERE assignment.task_id = t.id
        ORDER BY label.ordinal ASC, label.id ASC
    ) ordered_label
)
 AS label_ordinals
    FROM task_records t
    WHERE t.project_id = sqlc.arg(project_id)
      AND t.workflow_id = sqlc.arg(workflow_id)
      AND (
          sqlc.arg(label_filter_kind) = 'none'
          OR (
              sqlc.arg(label_filter_kind) = 'named'
              AND sqlc.narg(label_filter_mode) = 'any'
              AND (
                  EXISTS (
                      SELECT 1
                      FROM json_each(sqlc.arg(label_ids_json)) selected_label
                      JOIN task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                        ON assignment.label_id = selected_label.value
                      WHERE assignment.task_id = t.id
                  )
                  OR EXISTS (
                      SELECT 1
                      FROM json_each(sqlc.arg(excluded_label_ids_json)) excluded_label
                      WHERE NOT EXISTS (
                          SELECT 1
                          FROM task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                          WHERE assignment.label_id = excluded_label.value
                            AND assignment.task_id = t.id
                      )
                  )
              )
          )
          OR (
              sqlc.arg(label_filter_kind) = 'named'
              AND sqlc.narg(label_filter_mode) = 'all'
              AND NOT EXISTS (
                  SELECT 1
                  FROM json_each(sqlc.arg(label_ids_json)) selected_label
                  WHERE NOT EXISTS (
                      SELECT 1
                      FROM task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                      WHERE assignment.label_id = selected_label.value
                        AND assignment.task_id = t.id
                  )
              )
              AND NOT EXISTS (
                  SELECT 1
                  FROM json_each(sqlc.arg(excluded_label_ids_json)) excluded_label
                  JOIN task_label_assignments assignment INDEXED BY task_label_assignments_label_task_idx
                    ON assignment.label_id = excluded_label.value
                  WHERE assignment.task_id = t.id
              )
          )
          OR (
              sqlc.arg(label_filter_kind) = 'unlabeled'
              AND NOT EXISTS (
                  SELECT 1
                  FROM task_label_assignments assignment
                  WHERE assignment.task_id = t.id
              )
          )
      )
      AND
          (
              sqlc.narg(dependency_filter) IS NULL
              OR CAST(sqlc.narg(dependency_filter) AS INTEGER) = (
                  NOT EXISTS (
                      SELECT 1
                      FROM task_dependencies dependency INDEXED BY task_dependencies_reverse_idx
                      WHERE dependency.blocked_task_id = t.id
                        AND NOT EXISTS (
                            SELECT 1
                            FROM workflow_task_status_records status
                            WHERE status.task_id = dependency.blocker_task_id
                              AND status.is_done != 0
                        )
                  )
              )
          )

      AND (
          EXISTS (
              SELECT 1
              FROM task_current_nodes current_node
              WHERE current_node.task_id = t.id
                AND current_node.node_id = sqlc.arg(node_id)
          )
      )
),
board_sort AS (
    SELECT
        CAST(sqlc.arg(sort_field) AS TEXT) AS sort_field,
        CAST(sqlc.arg(sort_direction) AS TEXT) AS sort_direction
),
board_sort_keys AS (
    SELECT
        t.*,
        CASE WHEN sort.sort_field = 'labels' AND t.label_ordinals IS NULL THEN 1 ELSE 0 END AS sort_null_labels,
        CASE WHEN sort.sort_field = 'updated' AND sort.sort_direction = 'asc' THEN t.updated_at_unix_ms END AS sort_updated_ascending,
        CASE WHEN sort.sort_field = 'updated' AND sort.sort_direction = 'desc' THEN t.updated_at_unix_ms END AS sort_updated_descending,
        CASE WHEN sort.sort_field = 'created' AND sort.sort_direction = 'asc' THEN t.created_at_unix_ms END AS sort_created_ascending,
        CASE WHEN sort.sort_field = 'created' AND sort.sort_direction = 'desc' THEN t.created_at_unix_ms END AS sort_created_descending,
        CASE WHEN sort.sort_field = 'labels' AND sort.sort_direction = 'asc' THEN t.label_ordinals END AS sort_labels_ascending,
        CASE WHEN sort.sort_field = 'labels' AND sort.sort_direction = 'desc' THEN t.label_ordinals END AS sort_labels_descending,
        CASE WHEN sort.sort_field = 'short_id' AND sort.sort_direction = 'asc' THEN t.task_seq END AS sort_short_id_ascending,
        CASE WHEN sort.sort_field = 'short_id' AND sort.sort_direction = 'desc' THEN t.task_seq END AS sort_short_id_descending,
        CASE WHEN sort.sort_direction = 'asc' THEN t.task_seq END AS sort_tiebreak_ascending,
        CASE WHEN sort.sort_direction = 'desc' THEN t.task_seq END AS sort_tiebreak_descending
    FROM board_node_tasks t
    CROSS JOIN board_sort sort
),
paged_board_tasks AS (
    SELECT *
    FROM board_sort_keys
    ORDER BY
        sort_null_labels ASC,
        sort_updated_ascending ASC,
        sort_updated_descending DESC,
        sort_created_ascending ASC,
        sort_created_descending DESC,
        sort_labels_ascending ASC,
        sort_labels_descending DESC,
        sort_short_id_ascending ASC,
        sort_short_id_descending DESC,
        sort_tiebreak_ascending ASC,
        sort_tiebreak_descending DESC
    LIMIT sqlc.arg(limit_rows) + 1
    OFFSET sqlc.arg(offset_rows)
),
dependency_progress AS (
    SELECT
        td.blocked_task_id AS task_id,
        CAST(COUNT(*) AS INTEGER) AS dependency_total_count,
        CAST(SUM(CASE WHEN (
            SELECT status.is_done
            FROM workflow_task_status_records status
            WHERE status.task_id = td.blocker_task_id
            LIMIT 1
        ) != 0 THEN 1 ELSE 0 END) AS INTEGER) AS dependency_satisfied_count
    FROM task_dependencies td INDEXED BY task_dependencies_reverse_idx
    WHERE td.blocked_task_id IN (
        SELECT id FROM paged_board_tasks
    )
    GROUP BY td.blocked_task_id
)
SELECT
    page.id,
    page.project_id,
    page.project_workflow_link_id,
    page.workflow_id,
    page.workflow_revision_seen,
    page.task_seq,
    page.short_id,
    page.title,
    page.body,
    page.source_url,
    page.source_workspace_id,
    page.managed_worktree_id,
    page.execution_target_mode,
    page.execution_target_requested_ref,
    page.execution_target_resolved_ref,
    page.execution_target_commit_oid,
    page.execution_target_provenance,
    page.created_at_unix_ms,
    page.updated_at_unix_ms,
    page.metadata_json,
    dependency_progress.dependency_satisfied_count,
    dependency_progress.dependency_total_count
FROM paged_board_tasks page
LEFT JOIN dependency_progress ON dependency_progress.task_id = page.id
ORDER BY
    page.sort_null_labels ASC,
    page.sort_updated_ascending ASC,
    page.sort_updated_descending DESC,
    page.sort_created_ascending ASC,
    page.sort_created_descending DESC,
    page.sort_labels_ascending ASC,
    page.sort_labels_descending DESC,
    page.sort_short_id_ascending ASC,
    page.sort_short_id_descending DESC,
    page.sort_tiebreak_ascending ASC,
    page.sort_tiebreak_descending DESC;


-- name: CountWorktreesByWorkspace :one
SELECT CAST(COUNT(*) AS INTEGER) AS worktree_count
FROM worktrees
WHERE workspace_id = sqlc.arg(workspace_id);
-- name: AcquireCurrentNodeResumeWriteLock :execrows
UPDATE task_current_nodes
SET scheduling_state = scheduling_state
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND (
      (transition_branch_key IS NULL AND sqlc.narg(transition_branch_key) IS NULL)
      OR transition_branch_key = sqlc.narg(transition_branch_key)
  )
  AND scheduling_state = 'interrupted';

-- name: AcquireManualMoveTaskWriteLock :execrows
UPDATE tasks
SET updated_at_unix_ms = updated_at_unix_ms
WHERE id = sqlc.arg(task_id);

-- name: ListUnknownTaskSearchProjectIDs :many
WITH requested_projects AS (
    SELECT CAST(value AS TEXT) AS project_id
    FROM json_each(sqlc.arg(project_ids_json))
)
SELECT requested_projects.project_id
FROM requested_projects
LEFT JOIN projects ON projects.id = requested_projects.project_id
WHERE projects.id IS NULL
ORDER BY requested_projects.project_id ASC;


-- name: GetTaskSearchSourceByDocumentID :one
SELECT
    document.document_id,
    document.source_kind,
    document.task_id,
    document.comment_id,
    content.short_id,
    content.title,
    content.body,
    content.comment
FROM task_search_documents document
JOIN task_search_content content
  ON content.document_id = document.document_id
WHERE document.document_id = sqlc.arg(document_id)
LIMIT 1;


-- name: ValidateTaskSearchFTS5Expression :many
SELECT document.document_id
FROM task_search_fts
JOIN task_search_documents document
  ON document.document_id = task_search_fts.rowid
WHERE task_search_fts MATCH sqlc.arg(fts5_expression)
LIMIT 1;
