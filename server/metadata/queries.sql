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
    updated_at_unix_ms
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
    updated_at_unix_ms
FROM workspaces
WHERE id = sqlc.arg(id)
LIMIT 1;

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
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES (
    sqlc.arg(id),
    sqlc.arg(name),
    sqlc.arg(description),
    sqlc.arg(version),
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

-- name: IncrementWorkflowVersion :one
UPDATE workflows
SET
    version = version + 1,
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id)
RETURNING version;

-- name: GetWorkflow :one
SELECT
    id,
    name,
    description,
    version,
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
    created_at_unix_ms,
    updated_at_unix_ms
FROM workflows
ORDER BY updated_at_unix_ms DESC, rowid DESC;

-- name: ListWorkflowRecordsPage :many
WITH workflow_list(
    id,
    name,
    description,
    version,
    created_at_unix_ms,
    updated_at_unix_ms,
    activity_at_unix_ms
) AS (
    SELECT
        workflows.id,
        workflows.name,
        workflows.description,
        workflows.version,
        workflows.created_at_unix_ms,
        workflows.updated_at_unix_ms,
        CAST(MAX(
            workflows.updated_at_unix_ms,
            COALESCE((
                SELECT MAX(task_records.updated_at_unix_ms)
                FROM task_records
                WHERE task_records.workflow_id = workflows.id
            ), 0)
        ) AS INTEGER) AS activity_at_unix_ms
    FROM workflows
    WHERE (sqlc.arg(exact_name) = '' OR workflows.name = sqlc.arg(exact_name))
      AND (
          sqlc.arg(search_query) = ''
          OR lower(workflows.name) LIKE '%' || lower(sqlc.arg(search_query)) || '%'
          OR lower(workflows.description) LIKE '%' || lower(sqlc.arg(search_query)) || '%'
      )
      AND (
          sqlc.arg(cursor_active) = 0
          OR MAX(
              workflows.updated_at_unix_ms,
              COALESCE((
                  SELECT MAX(task_records.updated_at_unix_ms)
                  FROM task_records
                  WHERE task_records.workflow_id = workflows.id
              ), 0)
          ) < sqlc.arg(cursor_activity_at_unix_ms)
          OR (
              MAX(
                  workflows.updated_at_unix_ms,
                  COALESCE((
                      SELECT MAX(task_records.updated_at_unix_ms)
                      FROM task_records
                      WHERE task_records.workflow_id = workflows.id
                  ), 0)
              ) = sqlc.arg(cursor_activity_at_unix_ms)
              AND workflows.id < sqlc.arg(cursor_workflow_id)
          )
      )
)
SELECT
    id,
    name,
    description,
    version,
    created_at_unix_ms,
    updated_at_unix_ms,
    activity_at_unix_ms
FROM workflow_list
ORDER BY activity_at_unix_ms DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: InsertWorkflowNode :exec
INSERT INTO workflow_nodes (
    id,
    workflow_id,
    node_key,
    kind,
    display_name,
    subagent_role,
    prompt_template,
    completion_mode,
    input_fields_json,
    join_input_providers_json,
    output_fields_json,
    group_id,
    sort_order
) VALUES (
    sqlc.arg(id),
    sqlc.arg(workflow_id),
    sqlc.arg(node_key),
    sqlc.arg(kind),
    sqlc.arg(display_name),
    sqlc.arg(subagent_role),
    sqlc.arg(prompt_template),
    sqlc.arg(completion_mode),
    sqlc.arg(input_fields_json),
    sqlc.arg(join_input_providers_json),
    sqlc.arg(output_fields_json),
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
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, subagent_role, prompt_template, completion_mode, input_fields_json, join_input_providers_json, output_fields_json, group_id, sort_order)
VALUES (
    sqlc.arg(id),
    sqlc.arg(workflow_id),
    sqlc.arg(node_key),
    sqlc.arg(kind),
    sqlc.arg(display_name),
    sqlc.arg(subagent_role),
    sqlc.arg(prompt_template),
    sqlc.arg(completion_mode),
    sqlc.arg(input_fields_json),
    sqlc.arg(join_input_providers_json),
    sqlc.arg(output_fields_json),
    sqlc.narg(group_id),
    sqlc.arg(sort_order)
)
ON CONFLICT(id) DO UPDATE SET
    node_key = excluded.node_key,
    kind = excluded.kind,
    display_name = excluded.display_name,
    subagent_role = excluded.subagent_role,
    prompt_template = excluded.prompt_template,
    completion_mode = excluded.completion_mode,
    input_fields_json = excluded.input_fields_json,
    join_input_providers_json = excluded.join_input_providers_json,
    output_fields_json = excluded.output_fields_json,
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
INSERT INTO workflow_edges (id, transition_group_id, edge_key, target_node_id, requires_approval, context_mode, context_source_kind, context_source_node_key, prompt_template, parameters_json, input_bindings_json, output_requirements_json, sort_order)
SELECT
    sqlc.arg(id),
    sqlc.arg(transition_group_id),
    sqlc.arg(edge_key),
    sqlc.arg(target_node_id),
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
    prompt_template,
    completion_mode,
    input_fields_json,
    join_input_providers_json,
    output_fields_json,
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
    prompt_template,
    completion_mode,
    input_fields_json,
    join_input_providers_json,
    output_fields_json,
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
    prompt_template = sqlc.arg(prompt_template),
    completion_mode = sqlc.arg(completion_mode),
    input_fields_json = sqlc.arg(input_fields_json),
    join_input_providers_json = sqlc.arg(join_input_providers_json),
    output_fields_json = sqlc.arg(output_fields_json),
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
    default_project_workflow_link_id = '',
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
JOIN task_node_placements p ON p.task_id = t.id AND p.state IN ('active', 'waiting_approval')
JOIN workflow_nodes n ON n.id = p.node_id
WHERE t.project_workflow_link_id = sqlc.arg(project_workflow_link_id)
  AND t.canceled_at_unix_ms = 0
  AND n.kind != 'terminal';

-- name: CountNonTerminalTasksByWorkflow :one
SELECT CAST(COUNT(DISTINCT t.id) AS INTEGER) AS task_count
FROM task_records t
JOIN task_node_placements p ON p.task_id = t.id AND p.state IN ('active', 'waiting_approval')
JOIN workflow_nodes n ON n.id = p.node_id
WHERE t.workflow_id = sqlc.arg(workflow_id)
  AND t.canceled_at_unix_ms = 0
  AND n.kind != 'terminal';

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
    COALESCE(p.default_project_workflow_link_id, '') AS default_project_workflow_link_id,
    (SELECT CAST(COUNT(*) AS INTEGER) FROM project_workflow_links active WHERE active.project_id = p.id) AS active_link_count
FROM projects p
WHERE p.id = sqlc.arg(project_id);

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
    default_project_workflow_link_id = '',
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

-- name: DeleteTaskTransitionsByTask :execrows
DELETE FROM task_transitions
WHERE task_id = sqlc.arg(task_id);

-- name: DeleteTaskNodePlacementsByTask :execrows
DELETE FROM task_node_placements
WHERE task_id = sqlc.arg(task_id);

-- name: DeleteTaskCommentsByTask :execrows
DELETE FROM task_comments
WHERE task_id = sqlc.arg(task_id);

-- name: DeleteTask :execrows
DELETE FROM tasks
WHERE id = sqlc.arg(id);

-- name: CountTaskNodeReferences :one
SELECT CAST(COUNT(*) AS INTEGER) AS ref_count
FROM (
    SELECT p.id FROM task_node_placements p WHERE p.node_id = sqlc.arg(node_id)
    UNION ALL
    SELECT tr.id FROM task_transition_records tr WHERE tr.source_node_id = sqlc.arg(node_id)
    UNION ALL
    SELECT te.id FROM task_transition_edges te WHERE te.target_node_id = sqlc.arg(node_id)
);

-- name: CountTaskEdgeReferences :one
SELECT CAST(COUNT(*) AS INTEGER) AS ref_count
FROM (
    -- Only live unresolved references block edge deletion. Completed historical
    -- transition edges intentionally rely on ON DELETE SET NULL.
    SELECT te.id
    FROM task_transition_edges te
    JOIN task_transition_records tt ON tt.id = te.task_transition_id
    JOIN task_records t ON t.id = tt.task_id
    WHERE te.workflow_edge_id = sqlc.arg(edge_id)
      AND t.canceled_at_unix_ms = 0
      AND tt.state = 'pending_approval'
      AND te.state = 'pending'
    UNION ALL
    SELECT p.id
    FROM task_node_placements p
    JOIN task_records t ON t.id = p.task_id
    JOIN workflow_nodes n ON n.id = p.node_id
    WHERE p.parallel_branch_edge_id = sqlc.arg(edge_id)
      AND p.state IN ('active', 'waiting_approval')
      AND t.canceled_at_unix_ms = 0
      AND n.kind != 'terminal'
);

-- name: GetWorkflowGraphActiveWorkPolicyImpact :one
SELECT
    (
        SELECT CAST(COUNT(DISTINCT p.id) AS INTEGER)
        FROM task_records t
        JOIN task_node_placements p ON p.task_id = t.id AND p.state IN ('active', 'waiting_approval')
        JOIN workflow_nodes n ON n.id = p.node_id
        WHERE t.workflow_id = sqlc.arg(workflow_id)
          AND t.canceled_at_unix_ms = 0
          AND n.kind NOT IN ('start', 'terminal')
    ) AS active_node_placement_count,
    (
        SELECT CAST(COUNT(DISTINCT tt.id) AS INTEGER)
        FROM task_transition_records tt
        JOIN task_records t ON t.id = tt.task_id
        WHERE t.workflow_id = sqlc.arg(workflow_id)
          AND t.canceled_at_unix_ms = 0
          AND tt.state = 'pending_approval'
    ) AS pending_approval_count,
    (
        SELECT CAST(COUNT(DISTINCT r.id) AS INTEGER)
        FROM task_run_records r
        JOIN task_records t ON t.id = r.task_id
        JOIN task_node_placements p ON p.id = r.placement_id
        JOIN workflow_nodes n ON n.id = r.node_id
        WHERE t.workflow_id = sqlc.arg(workflow_id)
          AND t.canceled_at_unix_ms = 0
          AND r.started_at_unix_ms > 0
          AND r.completed_at_unix_ms = 0
          AND r.interrupted_at_unix_ms = 0
          AND p.state = 'active'
          AND n.kind = 'agent'
    ) AS active_run_count,
    (
        SELECT CAST(COUNT(DISTINCT r.id) AS INTEGER)
        FROM task_run_records r
        JOIN task_records t ON t.id = r.task_id
        JOIN task_node_placements p ON p.id = r.placement_id
        JOIN workflow_nodes n ON n.id = r.node_id
        WHERE t.workflow_id = sqlc.arg(workflow_id)
          AND t.canceled_at_unix_ms = 0
          AND r.automation_requested_at_unix_ms > 0
          AND r.started_at_unix_ms = 0
          AND r.completed_at_unix_ms = 0
          AND r.interrupted_at_unix_ms = 0
          AND r.waiting_ask_id = ''
          AND p.state = 'active'
          AND n.kind = 'agent'
    ) AS runnable_run_count;

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
        WHEN r.started_at_unix_ms > 0
          AND r.completed_at_unix_ms = 0
          AND r.interrupted_at_unix_ms = 0
          AND placement.state = 'active'
          AND n.kind = 'agent'
        THEN r.id
    END) AS INTEGER) AS active_run_count,
    CAST(COUNT(DISTINCT CASE
        WHEN r.automation_requested_at_unix_ms > 0
          AND r.started_at_unix_ms = 0
          AND r.completed_at_unix_ms = 0
          AND r.interrupted_at_unix_ms = 0
          AND r.waiting_ask_id = ''
          AND t.canceled_at_unix_ms = 0
          AND placement.state = 'active'
          AND n.kind = 'agent'
        THEN r.id
    END) AS INTEGER) AS runnable_run_count,
    CAST(COUNT(DISTINCT CASE
        WHEN (
            r.started_at_unix_ms > 0
            AND r.completed_at_unix_ms = 0
            AND r.interrupted_at_unix_ms = 0
            AND placement.state = 'active'
            AND n.kind = 'agent'
        )
        OR (
            r.automation_requested_at_unix_ms > 0
            AND r.started_at_unix_ms = 0
            AND r.completed_at_unix_ms = 0
            AND r.interrupted_at_unix_ms = 0
            AND r.waiting_ask_id = ''
            AND t.canceled_at_unix_ms = 0
            AND placement.state = 'active'
            AND n.kind = 'agent'
        )
        THEN t.id
    END) AS INTEGER) AS blocked_task_count
FROM workflows w
LEFT JOIN project_workflow_links pwl ON pwl.workflow_id = w.id
LEFT JOIN projects p ON p.id = pwl.project_id
LEFT JOIN task_records t ON t.project_workflow_link_id = pwl.id
LEFT JOIN task_run_records r ON r.task_id = t.id
LEFT JOIN task_node_placements placement ON placement.id = r.placement_id
LEFT JOIN workflow_nodes n ON n.id = r.node_id
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
    canceled_at_unix_ms,
    cancellation_reason,
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
    0,
    '',
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
    canceled_at_unix_ms,
    cancellation_reason,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
FROM task_records
WHERE id = sqlc.arg(id)
LIMIT 1;

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
    canceled_at_unix_ms,
    cancellation_reason,
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
    canceled_at_unix_ms,
    cancellation_reason,
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
    canceled_at_unix_ms,
    cancellation_reason,
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
WITH effective_board_placements AS (
    SELECT
        t.id AS task_id,
        p.node_id AS node_id
    FROM task_node_placements p
    JOIN task_records t ON t.id = p.task_id
    JOIN workflow_nodes n ON n.id = p.node_id
    WHERE p.state IN ('active', 'waiting_approval')
      AND t.project_id = sqlc.arg(project_id)
      AND t.workflow_id = sqlc.arg(workflow_id)
      AND (
          t.canceled_at_unix_ms = 0
          OR n.kind = 'terminal'
          OR trim(sqlc.arg(canceled_terminal_node_id)) = ''
      )
    UNION
    SELECT
        t.id AS task_id,
        sqlc.arg(canceled_terminal_node_id) AS node_id
    FROM task_records t
    WHERE t.project_id = sqlc.arg(project_id)
      AND t.workflow_id = sqlc.arg(workflow_id)
      AND t.canceled_at_unix_ms != 0
      AND trim(sqlc.arg(canceled_terminal_node_id)) != ''
      AND NOT EXISTS (
          SELECT 1
          FROM task_node_placements p
          JOIN workflow_nodes n ON n.id = p.node_id
          WHERE p.task_id = t.id
            AND p.state IN ('active', 'waiting_approval')
            AND n.kind = 'terminal'
      )
    UNION
    SELECT
        t.id AS task_id,
        tt.source_node_id AS node_id
    FROM task_transition_records tt
    JOIN task_records t ON t.id = tt.task_id
    WHERE tt.state = 'pending_approval'
      AND t.project_id = sqlc.arg(project_id)
      AND t.workflow_id = sqlc.arg(workflow_id)
      AND (
          t.canceled_at_unix_ms = 0
          OR trim(sqlc.arg(canceled_terminal_node_id)) = ''
      )
      AND trim(tt.source_node_id) != ''
)
SELECT
    node_id,
    CAST(COUNT(DISTINCT task_id) AS INTEGER) AS task_count
FROM effective_board_placements
GROUP BY node_id
ORDER BY node_id ASC;

-- name: ListWorkflowTaskListRows :many
WITH args AS (
    SELECT
    CAST(sqlc.arg(status_filter_set) AS INTEGER) AS status_filter_set,
    CAST(sqlc.arg(status_keys_json) AS TEXT) AS status_keys_json,
    CAST(sqlc.arg(run_status_filter_set) AS INTEGER) AS run_status_filter_set,
    CAST(sqlc.arg(run_statuses_json) AS TEXT) AS run_statuses_json,
    CAST(sqlc.arg(visible_columns_json) AS TEXT) AS visible_columns_json,
    CAST(sqlc.arg(project_id) AS TEXT) AS project_id,
    CAST(sqlc.arg(workflow_id) AS TEXT) AS workflow_id,
    CAST(sqlc.arg(canceled_terminal_node_id) AS TEXT) AS canceled_terminal_node_id,
    CAST(sqlc.arg(sentinel_status_order) AS INTEGER) AS sentinel_status_order,
    CAST(sqlc.arg(cursor_set) AS INTEGER) AS cursor_set,
    CAST(sqlc.arg(cursor_task_id) AS TEXT) AS cursor_task_id,
    CAST(sqlc.arg(cursor_created_at_unix_ms) AS INTEGER) AS cursor_created_at_unix_ms,
    CAST(sqlc.arg(cursor_updated_at_unix_ms) AS INTEGER) AS cursor_updated_at_unix_ms,
    CAST(sqlc.arg(cursor_status_order) AS INTEGER) AS cursor_status_order,
    CAST(sqlc.arg(cursor_run_count) AS INTEGER) AS cursor_run_count,
    CAST(sqlc.arg(cursor_title_sort) AS TEXT) AS cursor_title_sort,
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
    CAST(sqlc.arg(limit_rows) AS INTEGER) AS limit_rows
),
visible_columns AS (
    SELECT
        CAST(json_extract(value, '$.node_id') AS TEXT) AS node_id,
        CAST(json_extract(value, '$.node_key') AS TEXT) AS node_key,
        CAST(json_extract(value, '$.node_kind') AS TEXT) AS node_kind,
        CAST(json_extract(value, '$.status_order') AS INTEGER) AS status_order
    FROM args, json_each(args.visible_columns_json)
),
selected_tasks AS (
    SELECT task_records.*
    FROM task_records
    CROSS JOIN args
    WHERE task_records.project_id = args.project_id
      AND task_records.workflow_id = args.workflow_id
),
effective_placements AS (
    SELECT t.id AS task_id, p.id AS placement_id, p.node_id AS node_id, p.state AS state, vc.status_order AS status_order, vc.node_key AS node_key, vc.node_kind AS node_kind
    FROM selected_tasks t
    CROSS JOIN args
    JOIN task_node_placements p ON p.task_id = t.id
    JOIN visible_columns vc ON vc.node_id = p.node_id
    WHERE p.state IN ('active', 'waiting_approval')
      AND (t.canceled_at_unix_ms = 0 OR vc.node_kind = 'terminal' OR trim(args.canceled_terminal_node_id) = '')
    UNION
    SELECT t.id AS task_id, '' AS placement_id, vc.node_id AS node_id, 'active' AS state, vc.status_order AS status_order, vc.node_key AS node_key, vc.node_kind AS node_kind
    FROM selected_tasks t
    CROSS JOIN args
    JOIN visible_columns vc ON vc.node_id = args.canceled_terminal_node_id
    WHERE t.canceled_at_unix_ms != 0
      AND trim(args.canceled_terminal_node_id) != ''
      AND NOT EXISTS (
          SELECT 1
          FROM task_node_placements p
          JOIN workflow_nodes n ON n.id = p.node_id
          WHERE p.task_id = t.id
            AND p.state IN ('active', 'waiting_approval')
            AND n.kind = 'terminal'
      )
    UNION
    SELECT t.id AS task_id, 'pending-approval:' || tt.id AS placement_id, tt.source_node_id AS node_id, 'waiting_approval' AS state, vc.status_order AS status_order, vc.node_key AS node_key, vc.node_kind AS node_kind
    FROM task_transition_records tt
    JOIN selected_tasks t ON t.id = tt.task_id
    CROSS JOIN args
    JOIN visible_columns vc ON vc.node_id = tt.source_node_id
    WHERE tt.state = 'pending_approval'
      AND (t.canceled_at_unix_ms = 0 OR trim(args.canceled_terminal_node_id) = '')
),
per_task_status AS (
    SELECT task_id, MIN(status_order) AS status_order
    FROM effective_placements
    GROUP BY task_id
),
run_counts AS (
    SELECT r.task_id, COUNT(*) AS run_count
    FROM task_run_records r
    JOIN selected_tasks t ON t.id = r.task_id
    GROUP BY r.task_id
)
SELECT
    t.id, t.project_id, t.project_workflow_link_id, t.workflow_id, t.workflow_revision_seen, t.task_seq, t.short_id, t.title, t.body, t.source_url, t.source_workspace_id, t.managed_worktree_id, t.canceled_at_unix_ms, t.cancellation_reason, t.created_at_unix_ms, t.updated_at_unix_ms, t.metadata_json,
    CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) AS status_order,
    CAST(COALESCE(rc.run_count, 0) AS INTEGER) AS run_count,
    CASE
        WHEN t.canceled_at_unix_ms != 0 THEN 'canceled'
        WHEN EXISTS (SELECT 1 FROM effective_placements ep_done WHERE ep_done.task_id = t.id AND ep_done.node_kind = 'terminal') THEN 'done'
        WHEN EXISTS (SELECT 1 FROM effective_placements ep_waiting WHERE ep_waiting.task_id = t.id AND ep_waiting.state = 'waiting_approval')
          OR EXISTS (
              SELECT 1
              FROM task_run_records r
              JOIN effective_placements ep_run ON ep_run.placement_id = r.placement_id
              WHERE ep_run.task_id = t.id
                AND r.completed_at_unix_ms = 0
                AND (r.started_at_unix_ms != 0 OR r.interrupted_at_unix_ms != 0 OR trim(r.waiting_ask_id) != '')
          ) THEN 'running'
        ELSE 'open'
    END AS run_status,
    LOWER(t.title) AS title_sort
FROM selected_tasks t
CROSS JOIN args
LEFT JOIN per_task_status pts ON pts.task_id = t.id
LEFT JOIN run_counts rc ON rc.task_id = t.id
GROUP BY t.id
HAVING (
    args.status_filter_set = 0
    OR EXISTS (SELECT 1 FROM effective_placements ep_filter WHERE ep_filter.task_id = t.id AND ep_filter.node_key IN (SELECT value FROM json_each(args.status_keys_json)))
)
  AND (
    args.run_status_filter_set = 0
    OR run_status IN (SELECT value FROM json_each(args.run_statuses_json))
)
  AND (
    args.cursor_set = 0
    OR (((args.sort_1_field = 'created' AND ((args.sort_1_desc = 0 AND created_at_unix_ms > args.cursor_created_at_unix_ms) OR (args.sort_1_desc != 0 AND created_at_unix_ms < args.cursor_created_at_unix_ms))) OR (args.sort_1_field = 'updated' AND ((args.sort_1_desc = 0 AND updated_at_unix_ms > args.cursor_updated_at_unix_ms) OR (args.sort_1_desc != 0 AND updated_at_unix_ms < args.cursor_updated_at_unix_ms))) OR (args.sort_1_field = 'status' AND ((args.sort_1_desc = 0 AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) > args.cursor_status_order) OR (args.sort_1_desc != 0 AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) < args.cursor_status_order))) OR (args.sort_1_field = 'run_count' AND ((args.sort_1_desc = 0 AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) > args.cursor_run_count) OR (args.sort_1_desc != 0 AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) < args.cursor_run_count))) OR (args.sort_1_field = 'title' AND ((args.sort_1_desc = 0 AND title_sort > args.cursor_title_sort) OR (args.sort_1_desc != 0 AND title_sort < args.cursor_title_sort)))))
        OR ((args.sort_1_field = '' OR (args.sort_1_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_1_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_1_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_1_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_1_field = 'title' AND title_sort = args.cursor_title_sort)) AND ((args.sort_2_field = 'created' AND ((args.sort_2_desc = 0 AND created_at_unix_ms > args.cursor_created_at_unix_ms) OR (args.sort_2_desc != 0 AND created_at_unix_ms < args.cursor_created_at_unix_ms))) OR (args.sort_2_field = 'updated' AND ((args.sort_2_desc = 0 AND updated_at_unix_ms > args.cursor_updated_at_unix_ms) OR (args.sort_2_desc != 0 AND updated_at_unix_ms < args.cursor_updated_at_unix_ms))) OR (args.sort_2_field = 'status' AND ((args.sort_2_desc = 0 AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) > args.cursor_status_order) OR (args.sort_2_desc != 0 AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) < args.cursor_status_order))) OR (args.sort_2_field = 'run_count' AND ((args.sort_2_desc = 0 AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) > args.cursor_run_count) OR (args.sort_2_desc != 0 AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) < args.cursor_run_count))) OR (args.sort_2_field = 'title' AND ((args.sort_2_desc = 0 AND title_sort > args.cursor_title_sort) OR (args.sort_2_desc != 0 AND title_sort < args.cursor_title_sort)))))
        OR ((args.sort_1_field = '' OR (args.sort_1_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_1_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_1_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_1_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_1_field = 'title' AND title_sort = args.cursor_title_sort)) AND (args.sort_2_field = '' OR (args.sort_2_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_2_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_2_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_2_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_2_field = 'title' AND title_sort = args.cursor_title_sort)) AND ((args.sort_3_field = 'created' AND ((args.sort_3_desc = 0 AND created_at_unix_ms > args.cursor_created_at_unix_ms) OR (args.sort_3_desc != 0 AND created_at_unix_ms < args.cursor_created_at_unix_ms))) OR (args.sort_3_field = 'updated' AND ((args.sort_3_desc = 0 AND updated_at_unix_ms > args.cursor_updated_at_unix_ms) OR (args.sort_3_desc != 0 AND updated_at_unix_ms < args.cursor_updated_at_unix_ms))) OR (args.sort_3_field = 'status' AND ((args.sort_3_desc = 0 AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) > args.cursor_status_order) OR (args.sort_3_desc != 0 AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) < args.cursor_status_order))) OR (args.sort_3_field = 'run_count' AND ((args.sort_3_desc = 0 AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) > args.cursor_run_count) OR (args.sort_3_desc != 0 AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) < args.cursor_run_count))) OR (args.sort_3_field = 'title' AND ((args.sort_3_desc = 0 AND title_sort > args.cursor_title_sort) OR (args.sort_3_desc != 0 AND title_sort < args.cursor_title_sort)))))
        OR ((args.sort_1_field = '' OR (args.sort_1_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_1_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_1_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_1_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_1_field = 'title' AND title_sort = args.cursor_title_sort)) AND (args.sort_2_field = '' OR (args.sort_2_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_2_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_2_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_2_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_2_field = 'title' AND title_sort = args.cursor_title_sort)) AND (args.sort_3_field = '' OR (args.sort_3_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_3_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_3_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_3_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_3_field = 'title' AND title_sort = args.cursor_title_sort)) AND ((args.sort_4_field = 'created' AND ((args.sort_4_desc = 0 AND created_at_unix_ms > args.cursor_created_at_unix_ms) OR (args.sort_4_desc != 0 AND created_at_unix_ms < args.cursor_created_at_unix_ms))) OR (args.sort_4_field = 'updated' AND ((args.sort_4_desc = 0 AND updated_at_unix_ms > args.cursor_updated_at_unix_ms) OR (args.sort_4_desc != 0 AND updated_at_unix_ms < args.cursor_updated_at_unix_ms))) OR (args.sort_4_field = 'status' AND ((args.sort_4_desc = 0 AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) > args.cursor_status_order) OR (args.sort_4_desc != 0 AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) < args.cursor_status_order))) OR (args.sort_4_field = 'run_count' AND ((args.sort_4_desc = 0 AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) > args.cursor_run_count) OR (args.sort_4_desc != 0 AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) < args.cursor_run_count))) OR (args.sort_4_field = 'title' AND ((args.sort_4_desc = 0 AND title_sort > args.cursor_title_sort) OR (args.sort_4_desc != 0 AND title_sort < args.cursor_title_sort)))))
        OR ((args.sort_1_field = '' OR (args.sort_1_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_1_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_1_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_1_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_1_field = 'title' AND title_sort = args.cursor_title_sort)) AND (args.sort_2_field = '' OR (args.sort_2_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_2_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_2_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_2_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_2_field = 'title' AND title_sort = args.cursor_title_sort)) AND (args.sort_3_field = '' OR (args.sort_3_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_3_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_3_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_3_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_3_field = 'title' AND title_sort = args.cursor_title_sort)) AND (args.sort_4_field = '' OR (args.sort_4_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_4_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_4_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_4_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_4_field = 'title' AND title_sort = args.cursor_title_sort)) AND ((args.sort_5_field = 'created' AND ((args.sort_5_desc = 0 AND created_at_unix_ms > args.cursor_created_at_unix_ms) OR (args.sort_5_desc != 0 AND created_at_unix_ms < args.cursor_created_at_unix_ms))) OR (args.sort_5_field = 'updated' AND ((args.sort_5_desc = 0 AND updated_at_unix_ms > args.cursor_updated_at_unix_ms) OR (args.sort_5_desc != 0 AND updated_at_unix_ms < args.cursor_updated_at_unix_ms))) OR (args.sort_5_field = 'status' AND ((args.sort_5_desc = 0 AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) > args.cursor_status_order) OR (args.sort_5_desc != 0 AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) < args.cursor_status_order))) OR (args.sort_5_field = 'run_count' AND ((args.sort_5_desc = 0 AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) > args.cursor_run_count) OR (args.sort_5_desc != 0 AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) < args.cursor_run_count))) OR (args.sort_5_field = 'title' AND ((args.sort_5_desc = 0 AND title_sort > args.cursor_title_sort) OR (args.sort_5_desc != 0 AND title_sort < args.cursor_title_sort)))))
        OR ((args.sort_1_field = '' OR (args.sort_1_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_1_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_1_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_1_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_1_field = 'title' AND title_sort = args.cursor_title_sort)) AND (args.sort_2_field = '' OR (args.sort_2_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_2_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_2_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_2_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_2_field = 'title' AND title_sort = args.cursor_title_sort)) AND (args.sort_3_field = '' OR (args.sort_3_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_3_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_3_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_3_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_3_field = 'title' AND title_sort = args.cursor_title_sort)) AND (args.sort_4_field = '' OR (args.sort_4_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_4_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_4_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_4_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_4_field = 'title' AND title_sort = args.cursor_title_sort)) AND (args.sort_5_field = '' OR (args.sort_5_field = 'created' AND created_at_unix_ms = args.cursor_created_at_unix_ms) OR (args.sort_5_field = 'updated' AND updated_at_unix_ms = args.cursor_updated_at_unix_ms) OR (args.sort_5_field = 'status' AND CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) = args.cursor_status_order) OR (args.sort_5_field = 'run_count' AND CAST(COALESCE(rc.run_count, 0) AS INTEGER) = args.cursor_run_count) OR (args.sort_5_field = 'title' AND title_sort = args.cursor_title_sort)) AND id > args.cursor_task_id)
)
ORDER BY
    CASE WHEN args.sort_1_field = 'created' AND args.sort_1_desc = 0 THEN created_at_unix_ms WHEN args.sort_1_field = 'updated' AND args.sort_1_desc = 0 THEN updated_at_unix_ms WHEN args.sort_1_field = 'status' AND args.sort_1_desc = 0 THEN CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) WHEN args.sort_1_field = 'run_count' AND args.sort_1_desc = 0 THEN CAST(COALESCE(rc.run_count, 0) AS INTEGER) END ASC,
    CASE WHEN args.sort_1_field = 'title' AND args.sort_1_desc = 0 THEN title_sort END ASC,
    CASE WHEN args.sort_1_field = 'created' AND args.sort_1_desc != 0 THEN created_at_unix_ms WHEN args.sort_1_field = 'updated' AND args.sort_1_desc != 0 THEN updated_at_unix_ms WHEN args.sort_1_field = 'status' AND args.sort_1_desc != 0 THEN CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) WHEN args.sort_1_field = 'run_count' AND args.sort_1_desc != 0 THEN CAST(COALESCE(rc.run_count, 0) AS INTEGER) END DESC,
    CASE WHEN args.sort_1_field = 'title' AND args.sort_1_desc != 0 THEN title_sort END DESC,
    CASE WHEN args.sort_2_field = 'created' AND args.sort_2_desc = 0 THEN created_at_unix_ms WHEN args.sort_2_field = 'updated' AND args.sort_2_desc = 0 THEN updated_at_unix_ms WHEN args.sort_2_field = 'status' AND args.sort_2_desc = 0 THEN CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) WHEN args.sort_2_field = 'run_count' AND args.sort_2_desc = 0 THEN CAST(COALESCE(rc.run_count, 0) AS INTEGER) END ASC,
    CASE WHEN args.sort_2_field = 'title' AND args.sort_2_desc = 0 THEN title_sort END ASC,
    CASE WHEN args.sort_2_field = 'created' AND args.sort_2_desc != 0 THEN created_at_unix_ms WHEN args.sort_2_field = 'updated' AND args.sort_2_desc != 0 THEN updated_at_unix_ms WHEN args.sort_2_field = 'status' AND args.sort_2_desc != 0 THEN CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) WHEN args.sort_2_field = 'run_count' AND args.sort_2_desc != 0 THEN CAST(COALESCE(rc.run_count, 0) AS INTEGER) END DESC,
    CASE WHEN args.sort_2_field = 'title' AND args.sort_2_desc != 0 THEN title_sort END DESC,
    CASE WHEN args.sort_3_field = 'created' AND args.sort_3_desc = 0 THEN created_at_unix_ms WHEN args.sort_3_field = 'updated' AND args.sort_3_desc = 0 THEN updated_at_unix_ms WHEN args.sort_3_field = 'status' AND args.sort_3_desc = 0 THEN CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) WHEN args.sort_3_field = 'run_count' AND args.sort_3_desc = 0 THEN CAST(COALESCE(rc.run_count, 0) AS INTEGER) END ASC,
    CASE WHEN args.sort_3_field = 'title' AND args.sort_3_desc = 0 THEN title_sort END ASC,
    CASE WHEN args.sort_3_field = 'created' AND args.sort_3_desc != 0 THEN created_at_unix_ms WHEN args.sort_3_field = 'updated' AND args.sort_3_desc != 0 THEN updated_at_unix_ms WHEN args.sort_3_field = 'status' AND args.sort_3_desc != 0 THEN CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) WHEN args.sort_3_field = 'run_count' AND args.sort_3_desc != 0 THEN CAST(COALESCE(rc.run_count, 0) AS INTEGER) END DESC,
    CASE WHEN args.sort_3_field = 'title' AND args.sort_3_desc != 0 THEN title_sort END DESC,
    CASE WHEN args.sort_4_field = 'created' AND args.sort_4_desc = 0 THEN created_at_unix_ms WHEN args.sort_4_field = 'updated' AND args.sort_4_desc = 0 THEN updated_at_unix_ms WHEN args.sort_4_field = 'status' AND args.sort_4_desc = 0 THEN CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) WHEN args.sort_4_field = 'run_count' AND args.sort_4_desc = 0 THEN CAST(COALESCE(rc.run_count, 0) AS INTEGER) END ASC,
    CASE WHEN args.sort_4_field = 'title' AND args.sort_4_desc = 0 THEN title_sort END ASC,
    CASE WHEN args.sort_4_field = 'created' AND args.sort_4_desc != 0 THEN created_at_unix_ms WHEN args.sort_4_field = 'updated' AND args.sort_4_desc != 0 THEN updated_at_unix_ms WHEN args.sort_4_field = 'status' AND args.sort_4_desc != 0 THEN CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) WHEN args.sort_4_field = 'run_count' AND args.sort_4_desc != 0 THEN CAST(COALESCE(rc.run_count, 0) AS INTEGER) END DESC,
    CASE WHEN args.sort_4_field = 'title' AND args.sort_4_desc != 0 THEN title_sort END DESC,
    CASE WHEN args.sort_5_field = 'created' AND args.sort_5_desc = 0 THEN created_at_unix_ms WHEN args.sort_5_field = 'updated' AND args.sort_5_desc = 0 THEN updated_at_unix_ms WHEN args.sort_5_field = 'status' AND args.sort_5_desc = 0 THEN CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) WHEN args.sort_5_field = 'run_count' AND args.sort_5_desc = 0 THEN CAST(COALESCE(rc.run_count, 0) AS INTEGER) END ASC,
    CASE WHEN args.sort_5_field = 'title' AND args.sort_5_desc = 0 THEN title_sort END ASC,
    CASE WHEN args.sort_5_field = 'created' AND args.sort_5_desc != 0 THEN created_at_unix_ms WHEN args.sort_5_field = 'updated' AND args.sort_5_desc != 0 THEN updated_at_unix_ms WHEN args.sort_5_field = 'status' AND args.sort_5_desc != 0 THEN CAST(COALESCE(pts.status_order, args.sentinel_status_order) AS INTEGER) WHEN args.sort_5_field = 'run_count' AND args.sort_5_desc != 0 THEN CAST(COALESCE(rc.run_count, 0) AS INTEGER) END DESC,
    CASE WHEN args.sort_5_field = 'title' AND args.sort_5_desc != 0 THEN title_sort END DESC,
    id ASC
LIMIT (SELECT limit_rows FROM args);

-- name: ListBoardOpenTasks :many
WITH board_open_task_ids AS (
    SELECT
        t.id
    FROM task_node_placements p
    JOIN task_records t ON t.id = p.task_id
    JOIN workflow_nodes n ON n.id = p.node_id
    WHERE p.state IN ('active', 'waiting_approval')
      AND n.kind != 'terminal'
      AND t.project_id = sqlc.arg(project_id)
      AND t.workflow_id = sqlc.arg(workflow_id)
      AND (
          t.canceled_at_unix_ms = 0
          OR trim(sqlc.arg(canceled_terminal_node_id)) = ''
      )
    UNION
    SELECT
        t.id
    FROM task_transition_records tt
    JOIN task_records t ON t.id = tt.task_id
    JOIN workflow_nodes n ON n.id = tt.source_node_id
    WHERE tt.state = 'pending_approval'
      AND n.kind != 'terminal'
      AND t.project_id = sqlc.arg(project_id)
      AND t.workflow_id = sqlc.arg(workflow_id)
      AND (
          t.canceled_at_unix_ms = 0
          OR trim(sqlc.arg(canceled_terminal_node_id)) = ''
      )
)
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
    t.canceled_at_unix_ms,
    t.cancellation_reason,
    t.created_at_unix_ms,
    t.updated_at_unix_ms,
    t.metadata_json
FROM task_records t
WHERE t.id IN (SELECT id FROM board_open_task_ids)
  AND NOT EXISTS (
      SELECT 1
      FROM task_node_placements terminal_placement
      JOIN workflow_nodes terminal_node ON terminal_node.id = terminal_placement.node_id
      WHERE terminal_placement.task_id = t.id
        AND terminal_placement.state IN ('active', 'waiting_approval')
        AND terminal_node.kind = 'terminal'
  )
  AND (
    CAST(sqlc.arg(cursor_set) AS INTEGER) = 0
    OR t.updated_at_unix_ms < sqlc.arg(cursor_updated_at_unix_ms)
    OR (
        t.updated_at_unix_ms = sqlc.arg(cursor_updated_at_unix_ms)
        AND t.id < sqlc.arg(cursor_task_id)
    )
  )
ORDER BY t.updated_at_unix_ms DESC, t.id DESC
LIMIT sqlc.arg(limit_rows);

-- name: ListBoardDonePreviewTasks :many
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
    t.canceled_at_unix_ms,
    t.cancellation_reason,
    t.created_at_unix_ms,
    t.updated_at_unix_ms,
    t.metadata_json
FROM task_records t
WHERE t.project_id = sqlc.arg(project_id)
  AND t.workflow_id = sqlc.arg(workflow_id)
  AND (
      EXISTS (
          SELECT 1
          FROM task_node_placements p
          JOIN workflow_nodes n ON n.id = p.node_id
          WHERE p.task_id = t.id
            AND p.state IN ('active', 'waiting_approval')
            AND n.kind = 'terminal'
      )
      OR (
          t.canceled_at_unix_ms != 0
          AND trim(sqlc.arg(canceled_terminal_node_id)) != ''
      )
  )
ORDER BY t.updated_at_unix_ms DESC, t.id DESC
LIMIT sqlc.arg(limit_rows);

-- name: ListBoardNodeTasks :many
WITH board_node_task_ids AS (
    SELECT
        t.id
    FROM task_node_placements p
    JOIN task_records t ON t.id = p.task_id
    JOIN workflow_nodes n ON n.id = p.node_id
    WHERE p.node_id = sqlc.arg(node_id)
      AND p.state IN ('active', 'waiting_approval')
      AND t.project_id = sqlc.arg(project_id)
      AND t.workflow_id = sqlc.arg(workflow_id)
      AND (
        t.canceled_at_unix_ms = 0
        OR n.kind = 'terminal'
      )
    UNION
    SELECT
        t.id
    FROM task_transition_records tt
    JOIN task_records t ON t.id = tt.task_id
    WHERE tt.source_node_id = sqlc.arg(node_id)
      AND tt.state = 'pending_approval'
      AND t.project_id = sqlc.arg(project_id)
      AND t.workflow_id = sqlc.arg(workflow_id)
      AND t.canceled_at_unix_ms = 0
    UNION
    SELECT
        t.id
    FROM task_records t
    WHERE t.project_id = sqlc.arg(project_id)
      AND t.workflow_id = sqlc.arg(workflow_id)
      AND t.canceled_at_unix_ms != 0
      AND sqlc.arg(node_id) = sqlc.arg(canceled_terminal_node_id)
      AND EXISTS (
        SELECT 1
        FROM workflow_nodes n
        WHERE n.id = sqlc.arg(node_id)
          AND n.kind = 'terminal'
      )
      AND NOT EXISTS (
        SELECT 1
        FROM task_node_placements p
        JOIN workflow_nodes n ON n.id = p.node_id
        WHERE p.task_id = t.id
          AND p.state IN ('active', 'waiting_approval')
          AND n.kind = 'terminal'
      )
)
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
    t.canceled_at_unix_ms,
    t.cancellation_reason,
    t.created_at_unix_ms,
    t.updated_at_unix_ms,
    t.metadata_json
FROM task_records t
WHERE t.id IN (SELECT id FROM board_node_task_ids)
  AND (
    CAST(sqlc.arg(cursor_set) AS INTEGER) = 0
    OR t.updated_at_unix_ms < sqlc.arg(cursor_updated_at_unix_ms)
    OR (
        t.updated_at_unix_ms = sqlc.arg(cursor_updated_at_unix_ms)
        AND t.id < sqlc.arg(cursor_task_id)
    )
  )
ORDER BY t.updated_at_unix_ms DESC, t.id DESC
LIMIT sqlc.arg(limit_rows);

-- name: UpdateTaskEditableFields :execrows
UPDATE tasks
SET
    title = sqlc.arg(title),
    body = sqlc.arg(body),
    source_workspace_id = sqlc.narg(source_workspace_id),
    metadata_json = sqlc.arg(metadata_json),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: CountTaskRunsByTask :one
SELECT CAST(COUNT(*) AS INTEGER) AS run_count
FROM task_run_records
WHERE task_id = sqlc.arg(task_id);

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
JOIN task_node_placements p
    ON p.task_id = t.id
    AND p.state IN ('active', 'waiting_approval')
JOIN workflow_nodes n ON n.id = p.node_id
WHERE t.managed_worktree_id = sqlc.arg(managed_worktree_id)
  AND t.canceled_at_unix_ms = 0
  AND n.kind != 'terminal';

-- name: CountOtherNonTerminalTasksByManagedWorktree :one
SELECT CAST(COUNT(DISTINCT t.id) AS INTEGER) AS ref_count
FROM tasks t
JOIN task_node_placements p
    ON p.task_id = t.id
    AND p.state IN ('active', 'waiting_approval')
JOIN workflow_nodes n ON n.id = p.node_id
WHERE t.managed_worktree_id = sqlc.arg(managed_worktree_id)
  AND t.id != sqlc.arg(task_id)
  AND t.canceled_at_unix_ms = 0
  AND n.kind != 'terminal';

-- name: CountNonTerminalTasksBySourceWorkspace :one
SELECT CAST(COUNT(DISTINCT t.id) AS INTEGER) AS task_count
FROM tasks t
WHERE t.source_workspace_id = sqlc.arg(workspace_id)
  AND t.canceled_at_unix_ms = 0
  AND (
      EXISTS (
          SELECT 1
          FROM task_node_placements p
          JOIN workflow_nodes n ON n.id = p.node_id
          WHERE p.task_id = t.id
            AND p.state IN ('active', 'waiting_approval')
            AND n.kind != 'terminal'
      )
      OR EXISTS (
          SELECT 1
          FROM task_transitions tt
          WHERE tt.task_id = t.id
            AND tt.state = 'pending_approval'
      )
  );

-- name: InsertTaskNodePlacement :exec
INSERT INTO task_node_placements (
    id,
    task_id,
    node_id,
    state,
    parallel_batch_transition_id,
    parallel_branch_edge_id,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES (
    sqlc.arg(id),
    sqlc.arg(task_id),
    sqlc.arg(node_id),
    sqlc.arg(state),
    sqlc.narg(parallel_batch_transition_id),
    sqlc.narg(parallel_branch_edge_id),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms)
);

-- name: UpdateTaskNodePlacementState :execrows
UPDATE task_node_placements
SET
    state = sqlc.arg(state),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: ListTaskNodePlacements :many
SELECT
    id,
    task_id,
    node_id,
    state,
    created_by_transition_id,
    parallel_batch_transition_id,
    parallel_branch_edge_id,
    created_at_unix_ms,
    updated_at_unix_ms
FROM task_node_placement_records
WHERE task_id = sqlc.arg(task_id)
ORDER BY created_at_unix_ms ASC, (
    SELECT storage.rowid
    FROM task_node_placements storage
    WHERE storage.id = task_node_placement_records.id
) ASC;

-- name: ListTaskNodePlacementsByTasks :many
SELECT
    id,
    task_id,
    node_id,
    state,
    created_by_transition_id,
    parallel_batch_transition_id,
    parallel_branch_edge_id,
    created_at_unix_ms,
    updated_at_unix_ms
FROM task_node_placement_records
WHERE task_id IN (sqlc.slice('task_ids'))
ORDER BY task_id ASC, created_at_unix_ms ASC, (
    SELECT storage.rowid
    FROM task_node_placements storage
    WHERE storage.id = task_node_placement_records.id
) ASC;

-- name: ListPendingApprovalSourcePlacementsByTasks :many
SELECT
    CAST(COALESCE('pending-approval:' || id, '') AS TEXT) AS id,
    task_id,
    COALESCE(source_node_id, '') AS node_id,
    'waiting_approval' AS state,
    '' AS created_by_transition_id,
    CAST(NULL AS TEXT) AS parallel_batch_transition_id,
    CAST(NULL AS TEXT) AS parallel_branch_edge_id,
    created_at_unix_ms,
    created_at_unix_ms AS updated_at_unix_ms
FROM task_transition_records
WHERE task_id IN (sqlc.slice('task_ids'))
  AND state = 'pending_approval'
  AND trim(source_node_id) != ''
ORDER BY task_id ASC, created_at_unix_ms ASC, id ASC;

-- name: GetActiveStartPlacementForTask :one
SELECT
    p.id,
    p.task_id,
    p.node_id,
    p.state,
    p.created_by_transition_id,
    p.parallel_batch_transition_id,
    p.parallel_branch_edge_id,
    p.created_at_unix_ms,
    p.updated_at_unix_ms
FROM task_node_placement_records p
JOIN workflow_nodes n ON n.id = p.node_id
WHERE p.task_id = sqlc.arg(task_id)
  AND p.state = 'active'
  AND n.kind = 'start'
LIMIT 1;

-- name: InsertTaskRun :exec
INSERT INTO task_runs (
    id,
    placement_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json,
    waiting_ask_id,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
) VALUES (
    sqlc.arg(id),
    sqlc.arg(placement_id),
    sqlc.narg(session_id),
    sqlc.arg(run_generation),
    sqlc.arg(workflow_revision_seen),
    sqlc.arg(automation_requested_at_unix_ms),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms),
    sqlc.arg(started_at_unix_ms),
    sqlc.arg(completed_at_unix_ms),
    sqlc.arg(interrupted_at_unix_ms),
    sqlc.arg(interruption_reason),
    sqlc.arg(interruption_detail_json),
    sqlc.arg(waiting_ask_id),
    sqlc.arg(invalid_completion_count),
    sqlc.arg(run_start_snapshot_json),
    sqlc.arg(metadata_json)
);

-- name: UpdateTaskRunOutcome :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    completed_at_unix_ms = sqlc.arg(completed_at_unix_ms),
    interrupted_at_unix_ms = sqlc.arg(interrupted_at_unix_ms),
    interruption_reason = sqlc.arg(interruption_reason),
    interruption_detail_json = sqlc.arg(interruption_detail_json),
    waiting_ask_id = sqlc.arg(waiting_ask_id),
    invalid_completion_count = sqlc.arg(invalid_completion_count)
WHERE id = sqlc.arg(id);

-- name: ListTaskRuns :many
SELECT
    id,
    task_id,
    placement_id,
    node_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json,
    waiting_ask_id,
    effective_completion_mode,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
FROM task_run_records
WHERE task_id = sqlc.arg(task_id)
ORDER BY created_at_unix_ms ASC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = task_run_records.id
) ASC;

-- name: GetTaskRun :one
SELECT
    id,
    task_id,
    placement_id,
    node_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json,
    waiting_ask_id,
    effective_completion_mode,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
FROM task_run_records
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: ListRunnableWorkflowRuns :many
SELECT
    r.id,
    r.task_id,
    r.placement_id,
    r.node_id,
    r.session_id,
    r.run_generation,
    r.workflow_revision_seen,
    r.automation_requested_at_unix_ms,
    r.created_at_unix_ms,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.interruption_reason,
    r.interruption_detail_json,
    r.waiting_ask_id,
    r.effective_completion_mode,
    r.invalid_completion_count,
    r.run_start_snapshot_json,
    r.metadata_json
FROM task_run_records r
JOIN task_records t ON t.id = r.task_id
JOIN task_node_placements p ON p.id = r.placement_id
JOIN workflow_nodes n ON n.id = r.node_id
WHERE r.automation_requested_at_unix_ms > 0
  AND r.started_at_unix_ms = 0
  AND r.completed_at_unix_ms = 0
  AND r.interrupted_at_unix_ms = 0
  AND r.waiting_ask_id = ''
  AND t.canceled_at_unix_ms = 0
  AND p.state = 'active'
  AND n.kind = 'agent'
ORDER BY r.automation_requested_at_unix_ms ASC, r.id ASC
LIMIT sqlc.arg(limit);

-- name: ClaimWorkflowRun :one
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    started_at_unix_ms = sqlc.arg(started_at_unix_ms),
    run_generation = run_generation + 1
WHERE task_runs.id = sqlc.arg(id)
  AND run_generation = sqlc.arg(expected_generation)
  AND automation_requested_at_unix_ms > 0
  AND started_at_unix_ms = 0
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0
  AND waiting_ask_id = ''
  AND EXISTS (
      SELECT 1
      FROM tasks t
      JOIN task_node_placements p ON p.id = task_runs.placement_id
      JOIN workflow_nodes n ON n.id = p.node_id
      WHERE t.id = p.task_id
        AND t.canceled_at_unix_ms = 0
        AND p.state = 'active'
        AND n.kind = 'agent'
  )
RETURNING
    id,
    (
        SELECT p.task_id
        FROM task_node_placements p
        WHERE p.id = task_runs.placement_id
    ) AS task_id,
    placement_id,
    (
        SELECT p.node_id
        FROM task_node_placements p
        WHERE p.id = task_runs.placement_id
    ) AS node_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json,
    waiting_ask_id,
    effective_completion_mode,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json;

-- name: SetTaskRunEffectiveCompletionMode :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    effective_completion_mode = sqlc.arg(effective_completion_mode)
WHERE id = sqlc.arg(id)
  AND run_generation = sqlc.arg(expected_generation)
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0
  AND (
      effective_completion_mode = ''
      OR effective_completion_mode = sqlc.arg(effective_completion_mode)
  );

-- name: ListWaitingAskWorkflowRuns :many
SELECT
    id,
    task_id,
    placement_id,
    node_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json,
    waiting_ask_id,
    effective_completion_mode,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
FROM task_run_records
WHERE waiting_ask_id != ''
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0
ORDER BY updated_at_unix_ms ASC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = task_run_records.id
) ASC;

-- name: InterruptWorkflowRun :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    interrupted_at_unix_ms = sqlc.arg(interrupted_at_unix_ms),
    interruption_reason = sqlc.arg(interruption_reason),
    interruption_detail_json = sqlc.arg(interruption_detail_json),
    waiting_ask_id = ''
WHERE id = sqlc.arg(id)
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0;

-- name: InterruptStartedWorkflowRunsForRecovery :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    interrupted_at_unix_ms = sqlc.arg(interrupted_at_unix_ms),
    interruption_reason = sqlc.arg(interruption_reason),
    interruption_detail_json = sqlc.arg(interruption_detail_json)
WHERE started_at_unix_ms > 0
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0
  AND waiting_ask_id = '';

-- name: InterruptActiveTaskRuns :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    interrupted_at_unix_ms = sqlc.arg(interrupted_at_unix_ms),
    interruption_reason = sqlc.arg(interruption_reason),
    interruption_detail_json = sqlc.arg(interruption_detail_json),
    waiting_ask_id = ''
WHERE EXISTS (
      SELECT 1
      FROM task_node_placements p
      WHERE p.id = task_runs.placement_id
        AND p.task_id = sqlc.arg(task_id)
  )
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0;

-- name: CancelTask :execrows
UPDATE tasks
SET
    canceled_at_unix_ms = sqlc.arg(canceled_at_unix_ms),
    cancellation_reason = sqlc.arg(cancellation_reason),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(id);

-- name: InsertTaskTransition :exec
INSERT INTO task_transitions (
    id,
    task_id,
    source_run_id,
    source_placement_id,
    source_node_key,
    source_node_display_name,
    transition_id,
    transition_display_name,
    workflow_revision_seen,
    actor,
    state,
    commentary,
    output_values_json,
    created_at_unix_ms,
    applied_at_unix_ms
) VALUES (
    sqlc.arg(id),
    sqlc.arg(task_id),
    sqlc.narg(source_run_id),
    sqlc.narg(source_placement_id),
    sqlc.arg(source_node_key),
    sqlc.arg(source_node_display_name),
    sqlc.arg(transition_id),
    sqlc.arg(transition_display_name),
    sqlc.arg(workflow_revision_seen),
    sqlc.arg(actor),
    sqlc.arg(state),
    sqlc.arg(commentary),
    sqlc.arg(output_values_json),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(applied_at_unix_ms)
);

-- name: ListTaskTransitions :many
SELECT
    id,
    task_id,
    source_run_id,
    source_placement_id,
    source_node_id,
    source_node_key,
    source_node_display_name,
    transition_group_id,
    transition_id,
    transition_display_name,
    workflow_revision_seen,
    actor,
    state,
    commentary,
    output_values_json,
    created_at_unix_ms,
    applied_at_unix_ms
FROM task_transition_records
WHERE task_id = sqlc.arg(task_id)
ORDER BY created_at_unix_ms ASC, (
    SELECT storage.rowid
    FROM task_transitions storage
    WHERE storage.id = task_transition_records.id
) ASC;

-- name: InsertTaskTransitionEdge :exec
INSERT INTO task_transition_edges (
    id,
    task_transition_id,
    workflow_edge_id,
    edge_key,
    target_node_id,
    target_node_key,
    target_node_display_name,
    target_node_kind,
    target_placement_id,
    state,
    context_mode,
    requires_approval,
    input_bindings_json,
    output_requirements_json,
    metadata_json
) VALUES (
    sqlc.arg(id),
    sqlc.arg(task_transition_id),
    sqlc.narg(workflow_edge_id),
    sqlc.arg(edge_key),
    sqlc.narg(target_node_id),
    sqlc.arg(target_node_key),
    sqlc.arg(target_node_display_name),
    sqlc.arg(target_node_kind),
    sqlc.narg(target_placement_id),
    sqlc.arg(state),
    sqlc.arg(context_mode),
    sqlc.arg(requires_approval),
    sqlc.arg(input_bindings_json),
    sqlc.arg(output_requirements_json),
    sqlc.arg(metadata_json)
);

-- name: ListTaskTransitionEdges :many
SELECT
    id,
    task_transition_id,
    workflow_edge_id,
    edge_key,
    workflow_revision_seen,
    target_node_id,
    target_node_key,
    target_node_display_name,
    target_node_kind,
    target_placement_id,
    state,
    context_mode,
    requires_approval,
    input_bindings_json,
    output_requirements_json,
    metadata_json
FROM task_transition_edge_records
WHERE task_transition_id = sqlc.arg(task_transition_id)
ORDER BY (
    SELECT storage.rowid
    FROM task_transition_edges storage
    WHERE storage.id = task_transition_edge_records.id
) ASC;

-- name: GetTaskIdentityForTransition :one
SELECT t.id, t.project_id, t.workflow_id
FROM task_transitions tt
JOIN task_records t ON t.id = tt.task_id
WHERE tt.id = sqlc.arg(transition_id)
LIMIT 1;

-- name: GetTransitionApprovalState :one
SELECT task_id, source_run_id, state, workflow_revision_seen, created_at_unix_ms
FROM task_transition_records
WHERE id = sqlc.arg(transition_id)
LIMIT 1;

-- name: ApprovePendingTransition :execrows
UPDATE task_transitions
SET state = 'approved', applied_at_unix_ms = sqlc.arg(applied_at_unix_ms)
WHERE id = sqlc.arg(transition_id)
  AND state = 'pending_approval';

-- name: GetTaskTransitionState :one
SELECT state
FROM task_transitions
WHERE id = sqlc.arg(transition_id)
LIMIT 1;

-- name: ApplyPendingTransitionEdgeToJoin :execrows
UPDATE task_transition_edges
SET state = 'applied'
WHERE id = sqlc.arg(edge_id)
  AND state = 'pending';

-- name: ApplyPendingTransitionEdgeToPlacement :execrows
UPDATE task_transition_edges
SET state = 'applied',
    target_placement_id = sqlc.arg(target_placement_id)
WHERE id = sqlc.arg(edge_id)
  AND state = 'pending';

-- name: ListTaskRunIDsByPlacementForTransitionResult :many
SELECT id
FROM task_runs
WHERE placement_id = sqlc.arg(placement_id)
ORDER BY created_at_unix_ms ASC, rowid ASC;

-- name: GetContextSourceBatchScope :one
SELECT parallel_batch_transition_id
FROM task_node_placements
WHERE id = sqlc.arg(placement_id)
LIMIT 1;

-- name: GetLatestCompletedContextSourceRun :one
SELECT r.id
FROM task_runs r
JOIN task_node_placements p ON p.id = r.placement_id
WHERE p.task_id = sqlc.arg(task_id)
  AND p.node_id = sqlc.arg(node_id)
  AND r.completed_at_unix_ms > 0
  AND r.completed_at_unix_ms <= sqlc.arg(before_unix_ms)
ORDER BY r.completed_at_unix_ms DESC, r.rowid DESC
LIMIT 1;

-- name: GetLatestCompletedContextSourceRunInBatch :one
SELECT r.id
FROM task_runs r
JOIN task_node_placements p ON p.id = r.placement_id
WHERE p.task_id = sqlc.arg(task_id)
  AND p.node_id = sqlc.arg(node_id)
  AND p.parallel_batch_transition_id = sqlc.arg(batch_id)
  AND r.completed_at_unix_ms > 0
  AND r.completed_at_unix_ms <= sqlc.arg(before_unix_ms)
ORDER BY r.completed_at_unix_ms DESC, r.rowid DESC
LIMIT 1;

-- name: GetLatestTransitionOutputValues :one
SELECT tr.output_values_json
FROM task_transitions tr
WHERE tr.task_id = sqlc.arg(task_id)
  AND tr.transition_id = sqlc.arg(transition_id)
  AND tr.applied_at_unix_ms > 0
  AND tr.applied_at_unix_ms <= sqlc.arg(before_unix_ms)
  AND tr.state != 'rejected'
ORDER BY tr.applied_at_unix_ms DESC, tr.created_at_unix_ms DESC, tr.rowid DESC
LIMIT 1;

-- name: GetLatestTransitionOutputValuesInBatch :one
SELECT tr.output_values_json
FROM task_transitions tr
JOIN task_node_placements p ON p.id = tr.source_placement_id
WHERE tr.task_id = sqlc.arg(task_id)
  AND tr.transition_id = sqlc.arg(transition_id)
  AND p.parallel_batch_transition_id = sqlc.arg(batch_id)
  AND tr.applied_at_unix_ms > 0
  AND tr.applied_at_unix_ms <= sqlc.arg(before_unix_ms)
  AND tr.state != 'rejected'
ORDER BY tr.applied_at_unix_ms DESC, tr.created_at_unix_ms DESC, tr.rowid DESC
LIMIT 1;

-- name: GetExistingJoinPlacement :one
SELECT id
FROM task_node_placements
WHERE task_id = sqlc.arg(task_id)
  AND node_id = sqlc.arg(node_id)
  AND parallel_batch_transition_id = sqlc.arg(batch_id)
LIMIT 1;

-- name: ListJoinExpectedBranches :many
SELECT target_placement_id
FROM task_transition_edges
WHERE task_transition_id = sqlc.arg(batch_id)
  AND target_placement_id IS NOT NULL
ORDER BY rowid ASC;

-- name: ListJoinArrivals :many
SELECT
    p.id,
    p.parallel_branch_edge_id,
    te.workflow_edge_id,
    tr.source_node_key,
    tr.output_values_json
FROM task_node_placements p
JOIN task_transitions tr ON tr.source_placement_id = p.id
JOIN task_transition_edges te ON te.task_transition_id = tr.id
WHERE p.parallel_batch_transition_id = sqlc.arg(batch_id)
  AND p.state = 'completed'
  AND te.target_node_id = sqlc.arg(join_node_id)
  AND te.state = 'applied'
ORDER BY p.parallel_branch_edge_id ASC, tr.created_at_unix_ms ASC, te.rowid ASC;

-- name: GetLatestRunForPlacement :one
SELECT id, session_id
FROM task_runs
WHERE placement_id = sqlc.arg(placement_id)
ORDER BY created_at_unix_ms DESC, rowid DESC
LIMIT 1;

-- name: GetManualMovePreviousTransition :one
SELECT
    tr.transition_group_id,
    tr.transition_id,
    tr.transition_display_name,
    tr.output_values_json,
    tr.source_run_id,
    te.workflow_edge_id,
    te.edge_key,
    te.context_mode,
    te.requires_approval,
    te.input_bindings_json,
    te.output_requirements_json,
    te.metadata_json
FROM task_transition_records tr
JOIN task_transitions storage ON storage.id = tr.id
JOIN task_transition_edges te ON te.task_transition_id = tr.id
JOIN task_node_placements source_placement ON source_placement.id = tr.source_placement_id
WHERE te.target_placement_id = sqlc.arg(source_placement_id)
  AND source_placement.node_id = sqlc.arg(target_node_id)
ORDER BY tr.created_at_unix_ms DESC, storage.rowid DESC
LIMIT 1;

-- name: ListPendingApprovalManualMoveSources :many
SELECT tt.id, tt.source_placement_id, p.node_id
FROM task_transitions tt
JOIN task_node_placements p ON p.id = tt.source_placement_id
WHERE tt.task_id = sqlc.arg(task_id)
  AND tt.state = 'pending_approval'
ORDER BY tt.created_at_unix_ms DESC, tt.rowid DESC;

-- name: RejectPendingApprovalTransition :execrows
UPDATE task_transitions
SET state = 'rejected'
WHERE id = sqlc.arg(transition_id)
  AND state = 'pending_approval';

-- name: CompleteActiveManualMoveSourcePlacement :execrows
UPDATE task_node_placements
SET state = 'completed',
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE id = sqlc.arg(placement_id)
  AND state = 'active';

-- name: ListActiveManualMoveSources :many
SELECT id, node_id, parallel_batch_transition_id
FROM task_node_placements
WHERE task_id = sqlc.arg(task_id)
  AND state = 'active'
ORDER BY created_at_unix_ms DESC, rowid DESC;

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

-- name: ListTaskCommentsPage :many
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
    AND (
        sqlc.arg(has_cursor) = 0
        OR created_at_unix_ms < sqlc.arg(cursor_created_at_unix_ms)
        OR (
            created_at_unix_ms = sqlc.arg(cursor_created_at_unix_ms)
            AND id < sqlc.arg(cursor_id)
        )
    )
ORDER BY created_at_unix_ms DESC, id DESC
LIMIT sqlc.arg(limit_rows);

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

-- name: CountActiveSessionsByWorkspace :one
SELECT CAST(COUNT(*) AS INTEGER) AS session_count
FROM sessions
WHERE workspace_id = sqlc.arg(workspace_id)
  AND in_flight_step <> 0;

-- name: CountActiveTaskRunsByWorkspace :one
SELECT CAST(COUNT(DISTINCT r.id) AS INTEGER) AS run_count
FROM task_run_records r
JOIN task_records t ON t.id = r.task_id
LEFT JOIN sessions s ON s.id = r.session_id
WHERE r.completed_at_unix_ms = 0
  AND r.interrupted_at_unix_ms = 0
  AND (
      t.source_workspace_id = sqlc.arg(workspace_id)
      OR s.workspace_id = sqlc.arg(workspace_id)
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
    wt.created_at_unix_ms,
    wt.updated_at_unix_ms
FROM worktrees wt
JOIN workspaces w ON w.id = wt.workspace_id
WHERE wt.workspace_id = sqlc.arg(workspace_id)
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
    parent_session_id,
    created_at_unix_ms,
    updated_at_unix_ms,
    last_sequence,
    model_request_count,
    in_flight_step,
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
    sqlc.arg(parent_session_id),
    sqlc.arg(created_at_unix_ms),
    sqlc.arg(updated_at_unix_ms),
    sqlc.arg(last_sequence),
    sqlc.arg(model_request_count),
    sqlc.arg(in_flight_step),
    sqlc.arg(launch_visible),
    sqlc.arg(cwd_relpath),
    sqlc.arg(continuation_json),
    sqlc.arg(locked_json),
    sqlc.arg(usage_state_json),
    sqlc.arg(metadata_json)
)
ON CONFLICT(id) DO UPDATE SET
    project_id = excluded.project_id,
    workspace_id = excluded.workspace_id,
    worktree_id = excluded.worktree_id,
    artifact_relpath = excluded.artifact_relpath,
    name = excluded.name,
    first_prompt_preview = excluded.first_prompt_preview,
    input_draft = excluded.input_draft,
    parent_session_id = excluded.parent_session_id,
    updated_at_unix_ms = excluded.updated_at_unix_ms,
    last_sequence = excluded.last_sequence,
    model_request_count = excluded.model_request_count,
    in_flight_step = excluded.in_flight_step,
    launch_visible = CASE
        WHEN sessions.launch_visible <> 0 OR excluded.launch_visible <> 0 THEN 1
        ELSE 0
    END,
    cwd_relpath = excluded.cwd_relpath,
    continuation_json = excluded.continuation_json,
    locked_json = excluded.locked_json,
    usage_state_json = excluded.usage_state_json,
    metadata_json = excluded.metadata_json;

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
    COALESCE(default_workflow.id, '') AS default_workflow_id,
    COALESCE(default_workflow.name, '') AS default_workflow_name,
    CASE WHEN default_workflow.id IS NULL THEN 0 ELSE 1 END AS default_workflow_valid,
    CAST(MAX(
        p.updated_at_unix_ms,
        COALESCE(w.updated_at_unix_ms, 0),
        COALESCE((SELECT MAX(t.updated_at_unix_ms) FROM task_records t WHERE t.project_id = p.id), 0),
        COALESCE((
            SELECT MAX(tnp.updated_at_unix_ms)
            FROM task_node_placements tnp
            JOIN task_records placement_tasks ON placement_tasks.id = tnp.task_id
            WHERE placement_tasks.project_id = p.id
        ), 0),
        COALESCE((
            SELECT MAX(tr.updated_at_unix_ms)
            FROM task_run_records tr
            JOIN task_records run_tasks ON run_tasks.id = tr.task_id
            WHERE run_tasks.project_id = p.id
        ), 0),
        COALESCE((
            SELECT MAX(MAX(tt.created_at_unix_ms, tt.applied_at_unix_ms))
            FROM task_transitions tt
            JOIN task_records transition_tasks ON transition_tasks.id = tt.task_id
            WHERE transition_tasks.project_id = p.id
        ), 0),
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
        SELECT COUNT(DISTINCT attention_tasks.id)
        FROM task_records attention_tasks
        WHERE attention_tasks.project_id = p.id
          AND attention_tasks.canceled_at_unix_ms = 0
          AND (
              EXISTS (
                  SELECT 1
                  FROM task_node_placements tnp
                  WHERE tnp.task_id = attention_tasks.id
                    AND tnp.state = 'waiting_approval'
              )
              OR EXISTS (
                  SELECT 1
                  FROM task_transitions tt
                  WHERE tt.task_id = attention_tasks.id
                    AND tt.state = 'pending_approval'
              )
              OR EXISTS (
                  SELECT 1
                  FROM task_run_records tr
                  WHERE tr.task_id = attention_tasks.id
                    AND tr.completed_at_unix_ms = 0
                    AND (
                        tr.interrupted_at_unix_ms > 0
                        OR trim(tr.waiting_ask_id) <> ''
                    )
              )
          )
    ) AS INTEGER) AS attention_count,
    CAST((
        SELECT COUNT(*)
        FROM project_workflow_links pwl
        WHERE pwl.project_id = p.id
    ) AS INTEGER) AS workflow_count
FROM projects p
LEFT JOIN workspaces w ON w.id = p.primary_workspace_id AND w.project_id = p.id
LEFT JOIN project_workflow_links default_link
    ON default_link.id = p.default_project_workflow_link_id
   AND default_link.project_id = p.id
LEFT JOIN workflows default_workflow ON default_workflow.id = default_link.workflow_id
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
    COALESCE(MAX(s.updated_at_unix_ms), w.updated_at_unix_ms) AS latest_activity_unix_ms
FROM workspaces w
JOIN projects p ON p.id = w.project_id
LEFT JOIN sessions s ON s.workspace_id = w.id AND s.launch_visible <> 0
WHERE w.project_id = sqlc.arg(project_id)
GROUP BY w.id, w.canonical_root_path, p.primary_workspace_id, w.updated_at_unix_ms
ORDER BY CASE WHEN w.id = p.primary_workspace_id THEN 1 ELSE 0 END DESC, latest_activity_unix_ms DESC, w.created_at_unix_ms ASC, w.rowid ASC;

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
WHERE w.project_id = sqlc.arg(project_id)
GROUP BY w.id, w.canonical_root_path, p.primary_workspace_id, w.updated_at_unix_ms
ORDER BY CASE WHEN w.id = p.primary_workspace_id THEN 1 ELSE 0 END DESC, w.created_at_unix_ms DESC, w.rowid DESC
LIMIT sqlc.arg(limit_rows)
OFFSET sqlc.arg(offset_rows);

-- name: ListSessionsByProject :many
SELECT
    id,
    name,
    first_prompt_preview,
    updated_at_unix_ms
FROM sessions
WHERE project_id = sqlc.arg(project_id)
  AND launch_visible <> 0
ORDER BY updated_at_unix_ms DESC, rowid DESC;

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
    CAST((SELECT COUNT(*) FROM sessions s WHERE s.project_id = sqlc.arg(delete_project_id) AND s.in_flight_step <> 0) AS INTEGER) AS active_sessions,
    CAST((
        SELECT COUNT(DISTINCT id)
        FROM (
            SELECT t.id
            FROM task_records t
            JOIN task_node_placements p ON p.task_id = t.id AND p.state IN ('active', 'waiting_approval')
            JOIN workflow_nodes n ON n.id = p.node_id
            WHERE t.project_id = sqlc.arg(delete_project_id)
              AND t.canceled_at_unix_ms = 0
              -- Backlog/start-node tasks are drafts, not active project work.
              AND n.kind NOT IN ('start', 'terminal')
            UNION
            SELECT t.id
            FROM task_records t
            JOIN task_transitions tt ON tt.task_id = t.id AND tt.state = 'pending_approval'
            WHERE t.project_id = sqlc.arg(delete_project_id)
              AND t.canceled_at_unix_ms = 0
        )
    ) AS INTEGER) AS non_terminal_tasks,
    CAST((
        SELECT COUNT(DISTINCT r.id)
        FROM task_run_records r
        JOIN task_records t ON t.id = r.task_id
        JOIN task_node_placements p ON p.id = r.placement_id
        JOIN workflow_nodes n ON n.id = r.node_id
        WHERE t.project_id = sqlc.arg(delete_project_id)
          AND t.canceled_at_unix_ms = 0
          AND r.started_at_unix_ms > 0
          AND r.completed_at_unix_ms = 0
          AND r.interrupted_at_unix_ms = 0
          AND p.state = 'active'
          AND n.kind = 'agent'
    ) AS INTEGER) AS active_runs,
    CAST((
        SELECT COUNT(DISTINCT r.id)
        FROM task_run_records r
        JOIN task_records t ON t.id = r.task_id
        JOIN task_node_placements p ON p.id = r.placement_id
        JOIN workflow_nodes n ON n.id = r.node_id
        WHERE t.project_id = sqlc.arg(delete_project_id)
          AND t.canceled_at_unix_ms = 0
          AND r.automation_requested_at_unix_ms > 0
          AND r.started_at_unix_ms = 0
          AND r.completed_at_unix_ms = 0
          AND r.interrupted_at_unix_ms = 0
          AND r.waiting_ask_id = ''
          AND p.state = 'active'
          AND n.kind = 'agent'
    ) AS INTEGER) AS runnable_runs;

-- name: AcquireProjectDeleteWriteLock :execrows
UPDATE projects
SET updated_at_unix_ms = updated_at_unix_ms
WHERE id = sqlc.arg(project_id);

-- name: DeleteProjectTasks :exec
DELETE FROM tasks
WHERE id IN (
    SELECT id FROM task_records WHERE project_id = sqlc.arg(project_id)
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
    s.parent_session_id,
    s.created_at_unix_ms,
    s.updated_at_unix_ms,
    s.last_sequence,
    s.model_request_count,
    s.in_flight_step,
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
    s.project_id,
    COALESCE(s.workspace_id, '') AS workspace_id,
    CAST(COALESCE(json_extract(s.metadata_json, '$.workspace_container'), '') AS TEXT) AS workspace_snapshot_name,
    COALESCE(w.canonical_root_path, json_extract(s.metadata_json, '$.workspace_root'), '') AS workspace_root,
    s.worktree_id,
    COALESCE(wt.canonical_root_path, '') AS worktree_root,
    s.cwd_relpath
FROM sessions s
LEFT JOIN workspaces w ON w.id = s.workspace_id
LEFT JOIN worktrees wt ON wt.id = s.worktree_id
WHERE s.id = sqlc.arg(session_id)
LIMIT 1;

-- name: UpdateSessionExecutionTargetByID :execrows
UPDATE sessions
SET
    workspace_id = sqlc.arg(workspace_id),
    worktree_id = sqlc.narg(worktree_id),
    cwd_relpath = sqlc.arg(cwd_relpath),
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
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
FROM session_prompt_history_entries
WHERE session_id = sqlc.arg(session_id)
ORDER BY sequence ASC;

-- name: ListWorkflowAttentionCandidates :many
WITH attention_candidates(
    kind,
    id,
    project_id,
    workflow_id,
    task_id,
    short_id,
    title,
    run_id,
    session_id,
    ask_id,
    task_transition_id,
    interruption_reason,
    interruption_detail_json,
    occurred_at_unix_ms
) AS (
    SELECT
        'approval' AS kind,
        CAST('approval:' || tt.id AS TEXT) AS id,
        t.project_id,
        t.workflow_id,
        t.id AS task_id,
        t.short_id,
        t.title,
        '' AS run_id,
        '' AS session_id,
        '' AS ask_id,
        tt.id AS task_transition_id,
        '' AS interruption_reason,
        '' AS interruption_detail_json,
        tt.created_at_unix_ms AS occurred_at_unix_ms
    FROM task_transitions tt
    JOIN task_records t ON t.id = tt.task_id
    WHERE tt.state = 'pending_approval'
      AND t.canceled_at_unix_ms = 0
      AND (sqlc.arg(project_id) = '' OR t.project_id = sqlc.arg(project_id))
      AND (sqlc.arg(task_id) = '' OR t.id = sqlc.arg(task_id))
      AND (
          CAST(sqlc.arg(cursor_active) AS INTEGER) = 0
          OR tt.created_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (tt.created_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms) AND ('approval:' || tt.id) < sqlc.arg(cursor_item_id))
      )
    UNION ALL
    SELECT
        'question' AS kind,
        CAST('question:' || r.id || ':' || r.waiting_ask_id AS TEXT) AS id,
        t.project_id,
        t.workflow_id,
        t.id AS task_id,
        t.short_id,
        t.title,
        r.id AS run_id,
        COALESCE(r.session_id, '') AS session_id,
        r.waiting_ask_id AS ask_id,
        '' AS task_transition_id,
        '' AS interruption_reason,
        '' AS interruption_detail_json,
        r.updated_at_unix_ms AS occurred_at_unix_ms
    FROM task_run_records r
    JOIN task_records t ON t.id = r.task_id
    WHERE trim(r.waiting_ask_id) != ''
      AND r.completed_at_unix_ms = 0
      AND r.interrupted_at_unix_ms = 0
      AND t.canceled_at_unix_ms = 0
      AND (sqlc.arg(project_id) = '' OR t.project_id = sqlc.arg(project_id))
      AND (sqlc.arg(task_id) = '' OR t.id = sqlc.arg(task_id))
      AND (
          CAST(sqlc.arg(cursor_active) AS INTEGER) = 0
          OR r.updated_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (r.updated_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms) AND ('question:' || r.id || ':' || r.waiting_ask_id) < sqlc.arg(cursor_item_id))
      )
    UNION ALL
    SELECT
        'interrupted_run' AS kind,
        CAST('interrupted_run:' || r.id AS TEXT) AS id,
        t.project_id,
        t.workflow_id,
        t.id AS task_id,
        t.short_id,
        t.title,
        r.id AS run_id,
        COALESCE(r.session_id, '') AS session_id,
        '' AS ask_id,
        '' AS task_transition_id,
        r.interruption_reason,
        r.interruption_detail_json,
        r.interrupted_at_unix_ms AS occurred_at_unix_ms
    FROM task_run_records r
    JOIN task_records t ON t.id = r.task_id
    JOIN task_node_placements p ON p.id = r.placement_id
    WHERE r.interrupted_at_unix_ms > 0
      AND r.completed_at_unix_ms = 0
      AND p.state IN ('active', 'waiting_approval')
      AND t.canceled_at_unix_ms = 0
      AND (sqlc.arg(project_id) = '' OR t.project_id = sqlc.arg(project_id))
      AND (sqlc.arg(task_id) = '' OR t.id = sqlc.arg(task_id))
      AND (
          CAST(sqlc.arg(cursor_active) AS INTEGER) = 0
          OR r.interrupted_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (r.interrupted_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms) AND ('interrupted_run:' || r.id) < sqlc.arg(cursor_item_id))
      )
    UNION ALL
    SELECT
        'validation_blocker' AS kind,
        CAST('validation_blocker:' || project_id || ':' || workflow_id AS TEXT) AS id,
        project_id,
        workflow_id,
        '' AS task_id,
        '' AS short_id,
        '' AS title,
        '' AS run_id,
        '' AS session_id,
        '' AS ask_id,
        '' AS task_transition_id,
        '' AS interruption_reason,
        '' AS interruption_detail_json,
        updated_at_unix_ms AS occurred_at_unix_ms
    FROM project_workflow_links
    WHERE (sqlc.arg(project_id) = '' OR project_id = sqlc.arg(project_id))
      AND sqlc.arg(task_id) = ''
      AND (
          CAST(sqlc.arg(cursor_active) AS INTEGER) = 0
          OR updated_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (updated_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms) AND ('validation_blocker:' || project_id || ':' || workflow_id) < sqlc.arg(cursor_item_id))
      )
)
SELECT
    kind,
    id,
    project_id,
    workflow_id,
    task_id,
    short_id,
    title,
    run_id,
    session_id,
    ask_id,
    task_transition_id,
    interruption_reason,
    interruption_detail_json,
    occurred_at_unix_ms
FROM attention_candidates
ORDER BY occurred_at_unix_ms DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListWorkflowApprovalAttentionItems :many
SELECT tt.id AS task_transition_id, t.project_id, t.workflow_id, t.id AS task_id, t.short_id, t.title, tt.created_at_unix_ms
FROM task_transitions tt
JOIN task_records t ON t.id = tt.task_id
WHERE tt.state = 'pending_approval'
  AND t.canceled_at_unix_ms = 0
  AND (sqlc.arg(project_id) = '' OR t.project_id = sqlc.arg(project_id))
  AND (sqlc.arg(task_id) = '' OR t.id = sqlc.arg(task_id))
ORDER BY tt.created_at_unix_ms DESC, tt.rowid DESC;

-- name: ListWorkflowQuestionAttentionItems :many
SELECT r.id AS run_id, COALESCE(r.session_id, '') AS session_id, r.waiting_ask_id, t.project_id, t.workflow_id, t.id AS task_id, t.short_id, t.title, r.updated_at_unix_ms
FROM task_run_records r
JOIN task_records t ON t.id = r.task_id
WHERE trim(r.waiting_ask_id) != ''
  AND r.completed_at_unix_ms = 0
  AND r.interrupted_at_unix_ms = 0
  AND t.canceled_at_unix_ms = 0
  AND (sqlc.arg(project_id) = '' OR t.project_id = sqlc.arg(project_id))
  AND (sqlc.arg(task_id) = '' OR t.id = sqlc.arg(task_id))
ORDER BY r.updated_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = r.id
) DESC;

-- name: ListWorkflowInterruptedRunAttentionItems :many
SELECT r.id AS run_id, COALESCE(r.session_id, '') AS session_id, r.interruption_reason, r.interruption_detail_json, t.project_id, t.workflow_id, t.id AS task_id, t.short_id, t.title, r.interrupted_at_unix_ms
FROM task_run_records r
JOIN task_records t ON t.id = r.task_id
JOIN task_node_placements p ON p.id = r.placement_id
WHERE r.interrupted_at_unix_ms > 0
  AND r.completed_at_unix_ms = 0
  AND p.state IN ('active', 'waiting_approval')
  AND t.canceled_at_unix_ms = 0
  AND (sqlc.arg(project_id) = '' OR t.project_id = sqlc.arg(project_id))
  AND (sqlc.arg(task_id) = '' OR t.id = sqlc.arg(task_id))
ORDER BY r.interrupted_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = r.id
) DESC;

-- name: ListWorkflowValidationAttentionItems :many
SELECT project_id, workflow_id, updated_at_unix_ms
FROM project_workflow_links
WHERE (sqlc.arg(project_id) = '' OR project_id = sqlc.arg(project_id))
ORDER BY updated_at_unix_ms DESC, rowid DESC;

-- name: ListWorkflowTaskActivityRows :many
WITH activity(
    activity_id,
    kind,
    source_id,
    occurred_at_unix_ms,
    updated_at_unix_ms,
    actor
) AS (
    SELECT
        CAST('comment:' || c.id AS TEXT) AS activity_id,
        'comment' AS kind,
        c.id AS source_id,
        c.updated_at_unix_ms AS occurred_at_unix_ms,
        c.updated_at_unix_ms AS updated_at_unix_ms,
        c.author_kind AS actor
    FROM task_comments c
    WHERE c.task_id = sqlc.arg(task_id)
      AND (
          sqlc.arg(cursor_active) = 0
          OR c.updated_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (c.updated_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms) AND ('comment:' || c.id) < sqlc.arg(cursor_activity_id))
      )

    UNION ALL

    SELECT
        CAST('transition:' || tt.id AS TEXT) AS activity_id,
        'transition' AS kind,
        tt.id AS source_id,
        tt.created_at_unix_ms AS occurred_at_unix_ms,
        tt.applied_at_unix_ms AS updated_at_unix_ms,
        tt.actor AS actor
    FROM task_transitions tt
    WHERE tt.task_id = sqlc.arg(task_id)
      AND (
          sqlc.arg(cursor_active) = 0
          OR tt.created_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (tt.created_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms) AND ('transition:' || tt.id) < sqlc.arg(cursor_activity_id))
      )

    UNION ALL

    SELECT
        CAST('run_started:' || r.id AS TEXT) AS activity_id,
        'run_started' AS kind,
        r.id AS source_id,
        r.started_at_unix_ms AS occurred_at_unix_ms,
        r.updated_at_unix_ms AS updated_at_unix_ms,
        '' AS actor
    FROM task_run_records r
    WHERE r.task_id = sqlc.arg(task_id)
      AND r.started_at_unix_ms > 0
      AND (
          sqlc.arg(cursor_active) = 0
          OR r.started_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (r.started_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms) AND ('run_started:' || r.id) < sqlc.arg(cursor_activity_id))
      )

    UNION ALL

    SELECT
        CAST('run_completed:' || r.id AS TEXT) AS activity_id,
        'run_completed' AS kind,
        r.id AS source_id,
        r.completed_at_unix_ms AS occurred_at_unix_ms,
        r.updated_at_unix_ms AS updated_at_unix_ms,
        '' AS actor
    FROM task_run_records r
    WHERE r.task_id = sqlc.arg(task_id)
      AND r.completed_at_unix_ms > 0
      AND (
          sqlc.arg(cursor_active) = 0
          OR r.completed_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (r.completed_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms) AND ('run_completed:' || r.id) < sqlc.arg(cursor_activity_id))
      )

    UNION ALL

    SELECT
        CAST('run_interrupted:' || r.id AS TEXT) AS activity_id,
        'run_interrupted' AS kind,
        r.id AS source_id,
        r.interrupted_at_unix_ms AS occurred_at_unix_ms,
        r.updated_at_unix_ms AS updated_at_unix_ms,
        '' AS actor
    FROM task_run_records r
    WHERE r.task_id = sqlc.arg(task_id)
      AND r.interrupted_at_unix_ms > 0
      AND (
          sqlc.arg(cursor_active) = 0
          OR r.interrupted_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (r.interrupted_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms) AND ('run_interrupted:' || r.id) < sqlc.arg(cursor_activity_id))
      )

    UNION ALL

    SELECT
        CAST('task_canceled:' || t.id AS TEXT) AS activity_id,
        'task_canceled' AS kind,
        t.id AS source_id,
        t.canceled_at_unix_ms AS occurred_at_unix_ms,
        t.updated_at_unix_ms AS updated_at_unix_ms,
        '' AS actor
    FROM task_records t
    WHERE t.id = sqlc.arg(task_id)
      AND t.canceled_at_unix_ms > 0
      AND (
          sqlc.arg(cursor_active) = 0
          OR t.canceled_at_unix_ms < sqlc.arg(cursor_occurred_at_unix_ms)
          OR (t.canceled_at_unix_ms = sqlc.arg(cursor_occurred_at_unix_ms) AND ('task_canceled:' || t.id) < sqlc.arg(cursor_activity_id))
      )
)
SELECT activity_id, kind, source_id, occurred_at_unix_ms, updated_at_unix_ms, actor
FROM activity
ORDER BY occurred_at_unix_ms DESC, activity_id DESC
LIMIT sqlc.arg(page_limit);

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

-- name: ListTaskTransitionsByIDs :many
SELECT
    id,
    task_id,
    source_run_id,
    source_placement_id,
    source_node_id,
    source_node_key,
    source_node_display_name,
    transition_group_id,
    transition_id,
    transition_display_name,
    workflow_revision_seen,
    actor,
    state,
    commentary,
    output_values_json,
    created_at_unix_ms,
    applied_at_unix_ms
FROM task_transition_records
WHERE id IN (sqlc.slice('ids'));

-- name: ListTaskRunsByIDs :many
SELECT
    id,
    task_id,
    placement_id,
    node_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json,
    waiting_ask_id,
    effective_completion_mode,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
FROM task_run_records
WHERE id IN (sqlc.slice('ids'));

-- name: ListTaskTransitionEdgesByTransitionIDs :many
SELECT
    id,
    task_transition_id,
    workflow_edge_id,
    edge_key,
    workflow_revision_seen,
    target_node_id,
    target_node_key,
    target_node_display_name,
    target_node_kind,
    target_placement_id,
    state,
    context_mode,
    requires_approval,
    input_bindings_json,
    output_requirements_json,
    metadata_json
FROM task_transition_edge_records
WHERE task_transition_id IN (sqlc.slice('transition_ids'))
ORDER BY task_transition_id ASC, (
    SELECT storage.rowid
    FROM task_transition_edges storage
    WHERE storage.id = task_transition_edge_records.id
) ASC;

-- name: ListSessionNamesByIDs :many
SELECT id, name
FROM sessions
WHERE id IN (sqlc.slice('ids'));

-- name: InterruptRunGeneration :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    interrupted_at_unix_ms = sqlc.arg(interrupted_at_unix_ms),
    interruption_reason = sqlc.arg(interruption_reason),
    interruption_detail_json = sqlc.arg(interruption_detail_json),
    waiting_ask_id = ''
WHERE id = sqlc.arg(run_id)
  AND run_generation = sqlc.arg(run_generation)
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0;

-- name: ListInterruptTaskRunCandidates :many
SELECT
    r.id,
    r.task_id,
    r.placement_id,
    r.node_id,
    r.session_id,
    r.run_generation,
    r.workflow_revision_seen,
    r.automation_requested_at_unix_ms,
    r.created_at_unix_ms,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.interruption_reason,
    r.interruption_detail_json,
    r.waiting_ask_id,
    r.effective_completion_mode,
    r.invalid_completion_count,
    r.run_start_snapshot_json,
    r.metadata_json
FROM task_run_records r
JOIN task_node_placements p ON p.id = r.placement_id
JOIN workflow_nodes n ON n.id = r.node_id
WHERE r.task_id = sqlc.arg(task_id)
  AND (sqlc.arg(run_id) = '' OR r.id = sqlc.arg(run_id))
  AND r.started_at_unix_ms > 0
  AND r.completed_at_unix_ms = 0
  AND r.interrupted_at_unix_ms = 0
  AND p.state = 'active'
  AND n.kind = 'agent'
ORDER BY r.started_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = r.id
) DESC;

-- name: ListResumeTaskRunCandidates :many
SELECT
    r.id,
    r.run_start_snapshot_json
FROM task_run_records r
JOIN task_node_placements p ON p.id = r.placement_id
JOIN workflow_nodes n ON n.id = r.node_id
WHERE r.task_id = sqlc.arg(task_id)
  AND (sqlc.arg(run_id) = '' OR r.id = sqlc.arg(run_id))
  AND r.completed_at_unix_ms = 0
  AND r.interrupted_at_unix_ms > 0
  AND p.state = 'active'
  AND n.kind = 'agent'
ORDER BY r.interrupted_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = r.id
) DESC;

-- name: ResumeTaskRun :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    started_at_unix_ms = 0,
    interrupted_at_unix_ms = 0,
    interruption_reason = '',
    interruption_detail_json = '{}',
    waiting_ask_id = '',
    run_generation = run_generation + 1
WHERE id = sqlc.arg(run_id)
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms > 0;

-- name: GetRunTransitionContext :one
SELECT
    te.context_mode,
    tr.source_run_id,
    tr.source_node_display_name,
    te.target_node_display_name
FROM task_node_placements p
JOIN task_transition_edges te ON te.target_placement_id = p.id
JOIN task_transitions tr ON tr.id = te.task_transition_id
WHERE p.id = sqlc.arg(placement_id)
ORDER BY te.rowid ASC
LIMIT 1;

-- name: GetRunInputValues :one
SELECT
    tr.commentary,
    tr.output_values_json,
    te.input_bindings_json
FROM task_node_placements p
JOIN task_transition_edges te ON te.target_placement_id = p.id
JOIN task_transitions tr ON tr.id = te.task_transition_id
WHERE p.id = sqlc.arg(placement_id)
ORDER BY te.rowid ASC
LIMIT 1;

-- name: AttachRunSession :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    session_id = sqlc.arg(session_id)
WHERE id = sqlc.arg(run_id)
  AND run_generation = sqlc.arg(run_generation)
  AND started_at_unix_ms > 0
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0
  AND (session_id IS NULL OR session_id = sqlc.arg(session_id));

-- name: SetRunWaitingAsk :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    waiting_ask_id = sqlc.arg(ask_id)
WHERE id = sqlc.arg(run_id)
  AND run_generation = sqlc.arg(run_generation)
  AND started_at_unix_ms > 0
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0
  AND waiting_ask_id = '';

-- name: ClearRunWaitingAsk :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    waiting_ask_id = ''
WHERE id = sqlc.arg(run_id)
  AND run_generation = sqlc.arg(run_generation)
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0
  AND waiting_ask_id = sqlc.arg(ask_id);

-- name: GetRunWaitingAskEventIdentity :one
SELECT t.project_id, t.workflow_id, t.id AS task_id
FROM task_runs r
JOIN task_node_placements p ON p.id = r.placement_id
JOIN task_records t ON t.id = p.task_id
WHERE r.id = sqlc.arg(run_id);

-- name: ResolveTaskWaitingAsk :many
SELECT
    id,
    task_id,
    placement_id,
    node_id,
    session_id,
    run_generation,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json,
    waiting_ask_id,
    effective_completion_mode,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
FROM task_run_records
WHERE task_id = sqlc.arg(task_id)
  AND waiting_ask_id = sqlc.arg(ask_id)
  AND (sqlc.arg(run_id) = '' OR id = sqlc.arg(run_id))
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0
  AND trim(COALESCE(session_id, '')) != ''
ORDER BY updated_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = task_run_records.id
) DESC;

-- name: CompleteRunUpdateRun :execrows
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    completed_at_unix_ms = sqlc.arg(completed_at_unix_ms),
    waiting_ask_id = ''
WHERE id = sqlc.arg(run_id)
  AND run_generation = sqlc.arg(run_generation)
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0;

-- name: StartTaskCompleteStartPlacement :execrows
UPDATE task_node_placements
SET state = sqlc.arg(state), updated_at_unix_ms = sqlc.arg(updated_at_unix_ms)
WHERE task_node_placements.id = sqlc.arg(placement_id)
  AND state = 'active'
  AND task_id IN (
      SELECT tasks.id
      FROM tasks
      WHERE tasks.id = sqlc.arg(task_id)
        AND tasks.canceled_at_unix_ms = 0
  );

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

-- name: RecordInvalidCompletionProtocolViolation :one
UPDATE task_runs
SET
    updated_at_unix_ms = sqlc.arg(updated_at_unix_ms),
    invalid_completion_count = invalid_completion_count + 1,
    interrupted_at_unix_ms = CASE WHEN invalid_completion_count + 1 >= sqlc.arg(max_count) THEN sqlc.arg(interrupted_at_unix_ms) ELSE interrupted_at_unix_ms END,
    interruption_reason = CASE WHEN invalid_completion_count + 1 >= sqlc.arg(max_count) THEN 'workflow_protocol_violation_limit' ELSE interruption_reason END,
    interruption_detail_json = CASE WHEN invalid_completion_count + 1 >= sqlc.arg(max_count) THEN sqlc.arg(interruption_detail_json) ELSE interruption_detail_json END
WHERE id = sqlc.arg(run_id)
  AND completed_at_unix_ms = 0
  AND interrupted_at_unix_ms = 0
  AND (sqlc.arg(require_generation) = 0 OR run_generation = sqlc.arg(expected_generation))
RETURNING invalid_completion_count, interrupted_at_unix_ms;

-- name: ResolveActiveRunCompletionTargetByRunID :many
SELECT
    r.id,
    r.task_id,
    r.placement_id,
    r.node_id,
    r.session_id,
    r.run_generation,
    r.workflow_revision_seen,
    r.automation_requested_at_unix_ms,
    r.created_at_unix_ms,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.interruption_reason,
    r.interruption_detail_json,
    r.waiting_ask_id,
    r.effective_completion_mode,
    r.invalid_completion_count,
    r.run_start_snapshot_json,
    r.metadata_json
FROM task_run_records r
JOIN task_records t ON t.id = r.task_id
JOIN task_node_placements p ON p.id = r.placement_id
JOIN workflow_nodes n ON n.id = r.node_id
WHERE r.started_at_unix_ms > 0
  AND r.completed_at_unix_ms = 0
  AND r.interrupted_at_unix_ms = 0
  AND trim(COALESCE(r.session_id, '')) != ''
  AND t.canceled_at_unix_ms = 0
  AND p.state = 'active'
  AND n.kind = 'agent'
  AND r.id = sqlc.arg(run_id)
ORDER BY r.started_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = r.id
) DESC;

-- name: ResolveActiveRunCompletionTargetBySessionID :many
SELECT
    r.id,
    r.task_id,
    r.placement_id,
    r.node_id,
    r.session_id,
    r.run_generation,
    r.workflow_revision_seen,
    r.automation_requested_at_unix_ms,
    r.created_at_unix_ms,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.interruption_reason,
    r.interruption_detail_json,
    r.waiting_ask_id,
    r.effective_completion_mode,
    r.invalid_completion_count,
    r.run_start_snapshot_json,
    r.metadata_json
FROM task_run_records r
JOIN task_records t ON t.id = r.task_id
JOIN task_node_placements p ON p.id = r.placement_id
JOIN workflow_nodes n ON n.id = r.node_id
WHERE r.started_at_unix_ms > 0
  AND r.completed_at_unix_ms = 0
  AND r.interrupted_at_unix_ms = 0
  AND trim(COALESCE(r.session_id, '')) != ''
  AND t.canceled_at_unix_ms = 0
  AND p.state = 'active'
  AND n.kind = 'agent'
  AND r.session_id = sqlc.arg(session_id)
ORDER BY r.started_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = r.id
) DESC;

-- name: ResolveActiveRunCompletionTargetByTaskID :many
SELECT
    r.id,
    r.task_id,
    r.placement_id,
    r.node_id,
    r.session_id,
    r.run_generation,
    r.workflow_revision_seen,
    r.automation_requested_at_unix_ms,
    r.created_at_unix_ms,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.interruption_reason,
    r.interruption_detail_json,
    r.waiting_ask_id,
    r.effective_completion_mode,
    r.invalid_completion_count,
    r.run_start_snapshot_json,
    r.metadata_json
FROM task_run_records r
JOIN task_records t ON t.id = r.task_id
JOIN task_node_placements p ON p.id = r.placement_id
JOIN workflow_nodes n ON n.id = r.node_id
WHERE r.started_at_unix_ms > 0
  AND r.completed_at_unix_ms = 0
  AND r.interrupted_at_unix_ms = 0
  AND trim(COALESCE(r.session_id, '')) != ''
  AND t.canceled_at_unix_ms = 0
  AND p.state = 'active'
  AND n.kind = 'agent'
  AND t.id = sqlc.arg(task_id)
ORDER BY r.started_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = r.id
) DESC;

-- name: ResolveActiveRunCompletionTargetByProjectShortID :many
SELECT
    r.id,
    r.task_id,
    r.placement_id,
    r.node_id,
    r.session_id,
    r.run_generation,
    r.workflow_revision_seen,
    r.automation_requested_at_unix_ms,
    r.created_at_unix_ms,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.interruption_reason,
    r.interruption_detail_json,
    r.waiting_ask_id,
    r.effective_completion_mode,
    r.invalid_completion_count,
    r.run_start_snapshot_json,
    r.metadata_json
FROM task_run_records r
JOIN task_records t ON t.id = r.task_id
JOIN task_node_placements p ON p.id = r.placement_id
JOIN workflow_nodes n ON n.id = r.node_id
WHERE r.started_at_unix_ms > 0
  AND r.completed_at_unix_ms = 0
  AND r.interrupted_at_unix_ms = 0
  AND trim(COALESCE(r.session_id, '')) != ''
  AND t.canceled_at_unix_ms = 0
  AND p.state = 'active'
  AND n.kind = 'agent'
  AND t.short_id = sqlc.arg(short_id)
  AND t.project_id = sqlc.arg(project_id)
ORDER BY r.started_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = r.id
) DESC;

-- name: ResolveActiveRunCompletionTargetByShortID :many
SELECT
    r.id,
    r.task_id,
    r.placement_id,
    r.node_id,
    r.session_id,
    r.run_generation,
    r.workflow_revision_seen,
    r.automation_requested_at_unix_ms,
    r.created_at_unix_ms,
    r.updated_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.interruption_reason,
    r.interruption_detail_json,
    r.waiting_ask_id,
    r.effective_completion_mode,
    r.invalid_completion_count,
    r.run_start_snapshot_json,
    r.metadata_json
FROM task_run_records r
JOIN task_records t ON t.id = r.task_id
JOIN task_node_placements p ON p.id = r.placement_id
JOIN workflow_nodes n ON n.id = r.node_id
WHERE r.started_at_unix_ms > 0
  AND r.completed_at_unix_ms = 0
  AND r.interrupted_at_unix_ms = 0
  AND trim(COALESCE(r.session_id, '')) != ''
  AND t.canceled_at_unix_ms = 0
  AND p.state = 'active'
  AND n.kind = 'agent'
  AND t.short_id = sqlc.arg(short_id)
ORDER BY r.started_at_unix_ms DESC, (
    SELECT storage.rowid
    FROM task_runs storage
    WHERE storage.id = r.id
) DESC;
