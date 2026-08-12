-- +goose Up

-- table project_workflow_links
CREATE TABLE "project_workflow_links" (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE RESTRICT,
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    UNIQUE (project_id, id),
    UNIQUE (project_id, workflow_id)
);


-- table projects
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}'
, project_key TEXT NOT NULL DEFAULT '', next_task_seq INTEGER NOT NULL DEFAULT 1 CHECK (next_task_seq >= 1), default_project_workflow_link_id TEXT NOT NULL DEFAULT '', primary_workspace_id TEXT NOT NULL DEFAULT '');


-- table runtime_leases
CREATE TABLE "runtime_leases" (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    created_at_unix_ms INTEGER NOT NULL
, released_at_unix_ms INTEGER NOT NULL DEFAULT 0);


-- table sessions
CREATE TABLE "sessions" (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    artifact_relpath TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    first_prompt_preview TEXT NOT NULL DEFAULT '',
    input_draft TEXT NOT NULL DEFAULT '',
    parent_session_id TEXT NOT NULL DEFAULT '',
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL,
    last_sequence INTEGER NOT NULL DEFAULT 0,
    model_request_count INTEGER NOT NULL DEFAULT 0,
    in_flight_step INTEGER NOT NULL DEFAULT 0,
    launch_visible INTEGER NOT NULL DEFAULT 0,
    cwd_relpath TEXT NOT NULL DEFAULT '.',
    continuation_json TEXT NOT NULL DEFAULT '{}',
    locked_json TEXT NOT NULL DEFAULT '{}',
    usage_state_json TEXT NOT NULL DEFAULT '{}',
    metadata_json TEXT NOT NULL DEFAULT '{}'
);


-- table task_comments
CREATE TABLE "task_comments" (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    body TEXT NOT NULL CHECK (length(body) <= 262144),
    author_kind TEXT NOT NULL CHECK (author_kind IN ('user', 'agent')),
    author_id TEXT NOT NULL DEFAULT '',
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0)
);


-- table task_node_placements
CREATE TABLE "task_node_placements" (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('active', 'waiting_approval', 'completed', 'superseded')),
    parallel_batch_transition_id TEXT,
    parallel_branch_edge_id TEXT REFERENCES workflow_edges(id) ON DELETE SET NULL,
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    FOREIGN KEY (parallel_batch_transition_id) REFERENCES task_transitions(id) ON DELETE SET NULL
);


-- table task_runs
CREATE TABLE "task_runs" (
    id TEXT PRIMARY KEY,
    placement_id TEXT NOT NULL REFERENCES task_node_placements(id) ON DELETE CASCADE,
    session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    run_generation INTEGER NOT NULL DEFAULT 0 CHECK (run_generation >= 0),
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    automation_requested_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (automation_requested_at_unix_ms >= 0),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    started_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (started_at_unix_ms >= 0),
    completed_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (completed_at_unix_ms >= 0),
    interrupted_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (interrupted_at_unix_ms >= 0),
    interruption_reason TEXT NOT NULL DEFAULT '',
    interruption_detail_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(interruption_detail_json)),
    waiting_ask_id TEXT NOT NULL DEFAULT '',
    final_answer_violation_count INTEGER NOT NULL DEFAULT 0 CHECK (final_answer_violation_count >= 0),
    invalid_completion_count INTEGER NOT NULL DEFAULT 0 CHECK (invalid_completion_count >= 0),
    run_start_snapshot_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(run_start_snapshot_json)),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json))
);


-- table task_transition_edges
CREATE TABLE "task_transition_edges" (
    id TEXT PRIMARY KEY,
    task_transition_id TEXT NOT NULL REFERENCES task_transitions(id) ON DELETE CASCADE,
    workflow_edge_id TEXT REFERENCES workflow_edges(id) ON DELETE SET NULL,
    edge_key TEXT NOT NULL DEFAULT '',
    target_node_id TEXT REFERENCES workflow_nodes(id) ON DELETE SET NULL,
    target_node_key TEXT NOT NULL DEFAULT '',
    target_node_display_name TEXT NOT NULL DEFAULT '',
    target_node_kind TEXT NOT NULL DEFAULT '',
    target_placement_id TEXT REFERENCES task_node_placements(id) ON DELETE SET NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'applied', 'completed', 'blocked')),
    context_mode TEXT NOT NULL DEFAULT '' CHECK (context_mode IN ('', 'new_session', 'continue_session', 'compact_and_continue_session')),
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    input_bindings_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(input_bindings_json) AND json_type(input_bindings_json) = 'array'),
    output_requirements_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(output_requirements_json) AND json_type(output_requirements_json) = 'array'),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json))
);


-- table task_transitions
CREATE TABLE "task_transitions" (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    source_run_id TEXT REFERENCES task_runs(id) ON DELETE SET NULL,
    source_placement_id TEXT REFERENCES task_node_placements(id) ON DELETE SET NULL,
    source_node_key TEXT NOT NULL DEFAULT '',
    source_node_display_name TEXT NOT NULL DEFAULT '',
    transition_id TEXT NOT NULL,
    transition_display_name TEXT NOT NULL DEFAULT '',
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    actor TEXT NOT NULL CHECK (actor IN ('agent', 'user', 'system')),
    state TEXT NOT NULL CHECK (state IN ('pending_approval', 'approved', 'applied', 'rejected', 'invalid')),
    commentary TEXT NOT NULL DEFAULT '' CHECK (length(commentary) <= 65536),
    output_values_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_values_json)),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    applied_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (applied_at_unix_ms >= 0)
);


-- table tasks
CREATE TABLE "tasks" (
    id TEXT PRIMARY KEY,
    project_workflow_link_id TEXT NOT NULL REFERENCES project_workflow_links(id) ON DELETE RESTRICT,
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    task_seq INTEGER NOT NULL CHECK (task_seq >= 1),
    short_id TEXT NOT NULL,
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    body TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL DEFAULT '',
    source_workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    managed_worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    canceled_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (canceled_at_unix_ms >= 0),
    cancellation_reason TEXT NOT NULL DEFAULT '',
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json))
);


-- table workflow_edges
CREATE TABLE "workflow_edges" (
    id TEXT PRIMARY KEY,
    transition_group_id TEXT NOT NULL REFERENCES workflow_transition_groups(id) ON DELETE CASCADE,
    edge_key TEXT NOT NULL CHECK (length(edge_key) BETWEEN 1 AND 64),
    target_node_id TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE CASCADE,
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    context_mode TEXT NOT NULL CHECK (context_mode IN ('new_session', 'continue_session', 'compact_and_continue_session')),
    input_bindings_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_bindings_json)),
    output_requirements_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_requirements_json)),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    context_source_kind TEXT NOT NULL DEFAULT 'immediate_source'
        CHECK (context_source_kind IN ('immediate_source', 'selected_node', 'previous_target')),
    context_source_node_key TEXT NOT NULL DEFAULT ''
        CHECK (
            ((context_source_kind = 'immediate_source' OR context_source_kind = 'previous_target') AND context_source_node_key = '')
            OR (context_source_kind = 'selected_node' AND length(context_source_node_key) BETWEEN 1 AND 64)
        ), prompt_template TEXT NOT NULL DEFAULT '', parameters_json TEXT NOT NULL DEFAULT '[]'
        CHECK (json_valid(parameters_json) AND json_type(parameters_json) = 'array'),
    UNIQUE (transition_group_id, edge_key)
);


-- table workflow_node_groups
CREATE TABLE "workflow_node_groups" (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    group_key TEXT NOT NULL CHECK (length(group_key) BETWEEN 1 AND 64),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, group_key)
);


-- table workflow_nodes
CREATE TABLE "workflow_nodes" (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    node_key TEXT NOT NULL CHECK (length(node_key) BETWEEN 1 AND 64),
    kind TEXT NOT NULL CHECK (kind IN ('start', 'agent', 'join', 'terminal')),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    subagent_role TEXT NOT NULL DEFAULT '',
    prompt_template TEXT NOT NULL DEFAULT '',
    output_fields_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(output_fields_json)),
    group_id TEXT REFERENCES workflow_node_groups(id) ON DELETE SET NULL,
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0), input_fields_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(input_fields_json)), join_input_providers_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(join_input_providers_json)),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, node_key)
);


-- table workflow_transition_groups
CREATE TABLE "workflow_transition_groups" (
    id TEXT PRIMARY KEY,
    source_node_id TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE CASCADE,
    transition_id TEXT NOT NULL CHECK (length(transition_id) BETWEEN 1 AND 64),
    display_name TEXT NOT NULL DEFAULT '' CHECK (length(display_name) <= 120),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0), description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    UNIQUE (source_node_id, transition_id)
);


-- table workflows
CREATE TABLE "workflows" (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0)
);


-- table workspaces
CREATE TABLE "workspaces" (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    canonical_root_path TEXT NOT NULL,
    git_metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL
);


-- table worktrees
CREATE TABLE "worktrees" (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    canonical_root_path TEXT NOT NULL UNIQUE,
    managed INTEGER NOT NULL DEFAULT 0,
    created_branch INTEGER NOT NULL DEFAULT 0,
    origin_session_id TEXT NOT NULL DEFAULT '',
    git_metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL
);


-- view project_workflow_link_records
CREATE VIEW project_workflow_link_records AS
SELECT
    pwl.id,
    pwl.project_id,
    pwl.workflow_id,
    CASE WHEN p.default_project_workflow_link_id = pwl.id THEN 1 ELSE 0 END AS is_default,
    pwl.created_at_unix_ms,
    pwl.updated_at_unix_ms
FROM project_workflow_links pwl
JOIN projects p ON p.id = pwl.project_id;


-- view task_node_placement_records
CREATE VIEW task_node_placement_records AS
SELECT
    p.id,
    p.task_id,
    p.node_id,
    p.state,
    CAST(COALESCE((
        SELECT te.task_transition_id
        FROM task_transition_edges te
        WHERE te.target_placement_id = p.id
        ORDER BY te.rowid ASC
        LIMIT 1
    ), '') AS TEXT) AS created_by_transition_id,
    p.parallel_batch_transition_id,
    p.parallel_branch_edge_id,
    p.created_at_unix_ms,
    p.updated_at_unix_ms
FROM task_node_placements p;


-- view task_records
CREATE VIEW task_records AS
SELECT
    t.id,
    pwl.project_id,
    t.project_workflow_link_id,
    pwl.workflow_id,
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
FROM tasks t
JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id;


-- view task_run_records
CREATE VIEW task_run_records AS
SELECT
    r.id,
    p.task_id,
    r.placement_id,
    p.node_id,
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
    r.final_answer_violation_count,
    r.invalid_completion_count,
    r.run_start_snapshot_json,
    r.metadata_json
FROM task_runs r
JOIN task_node_placements p ON p.id = r.placement_id;


-- view task_transition_edge_records
CREATE VIEW task_transition_edge_records AS
SELECT
    te.id,
    te.task_transition_id,
    te.workflow_edge_id,
    te.edge_key,
    tt.workflow_revision_seen,
    te.target_node_id,
    te.target_node_key,
    te.target_node_display_name,
    te.target_node_kind,
    te.target_placement_id,
    te.state,
    te.context_mode,
    te.requires_approval,
    te.input_bindings_json,
    te.output_requirements_json,
    te.metadata_json
FROM task_transition_edges te
JOIN task_transitions tt ON tt.id = te.task_transition_id;


-- view task_transition_records
CREATE VIEW task_transition_records AS
SELECT
    tt.id,
    tt.task_id,
    tt.source_run_id,
    tt.source_placement_id,
    p.node_id AS source_node_id,
    tt.source_node_key,
    tt.source_node_display_name,
    derived_group_edge.transition_group_id,
    tt.transition_id,
    tt.transition_display_name,
    tt.workflow_revision_seen,
    tt.actor,
    tt.state,
    tt.commentary,
    tt.output_values_json,
    tt.created_at_unix_ms,
    tt.applied_at_unix_ms
FROM task_transitions tt
LEFT JOIN task_node_placements p ON p.id = tt.source_placement_id
LEFT JOIN task_transition_edges derived_transition_edge ON derived_transition_edge.id = (
    SELECT te.id
    FROM task_transition_edges te
    JOIN workflow_edges e ON e.id = te.workflow_edge_id
    WHERE te.task_transition_id = tt.id
      AND NOT EXISTS (
          SELECT 1
          FROM task_transition_edges other_te
          JOIN workflow_edges other_e ON other_e.id = other_te.workflow_edge_id
          WHERE other_te.task_transition_id = tt.id
            AND other_e.transition_group_id != e.transition_group_id
      )
    ORDER BY te.rowid ASC
    LIMIT 1
)
LEFT JOIN workflow_edges derived_group_edge ON derived_group_edge.id = derived_transition_edge.workflow_edge_id;


-- index project_workflow_links_workflow_idx
CREATE INDEX project_workflow_links_workflow_idx
    ON project_workflow_links(workflow_id);


-- index projects_primary_workspace_idx
CREATE INDEX projects_primary_workspace_idx
    ON projects(primary_workspace_id)
    WHERE primary_workspace_id != '';


-- index projects_project_key_idx
CREATE UNIQUE INDEX projects_project_key_idx
    ON projects(project_key)
    WHERE project_key != '';


-- index sessions_artifact_relpath_idx
CREATE UNIQUE INDEX sessions_artifact_relpath_idx ON sessions(artifact_relpath);


-- index sessions_project_idx
CREATE INDEX sessions_project_idx ON sessions(project_id, updated_at_unix_ms DESC);


-- index sessions_workspace_idx
CREATE INDEX sessions_workspace_idx ON sessions(workspace_id, updated_at_unix_ms DESC);


-- index task_comments_task_created_idx
CREATE INDEX task_comments_task_created_idx
    ON task_comments(task_id, created_at_unix_ms DESC, id DESC);


-- index task_comments_task_updated_idx
CREATE INDEX task_comments_task_updated_idx
    ON task_comments(task_id, updated_at_unix_ms DESC);


-- index task_node_placements_node_state_idx
CREATE INDEX task_node_placements_node_state_idx
    ON task_node_placements(node_id, state);


-- index task_node_placements_parallel_batch_idx
CREATE INDEX task_node_placements_parallel_batch_idx
    ON task_node_placements(parallel_batch_transition_id, parallel_branch_edge_id, state);


-- index task_node_placements_task_state_idx
CREATE INDEX task_node_placements_task_state_idx
    ON task_node_placements(task_id, state);


-- index task_runs_outcome_idx
CREATE INDEX task_runs_outcome_idx
    ON task_runs(started_at_unix_ms, completed_at_unix_ms, interrupted_at_unix_ms);


-- index task_runs_placement_created_idx
CREATE INDEX task_runs_placement_created_idx
    ON task_runs(placement_id, created_at_unix_ms DESC);


-- index task_runs_placement_idx
CREATE INDEX task_runs_placement_idx
    ON task_runs(placement_id);


-- index task_runs_runnable_idx
CREATE INDEX task_runs_runnable_idx
    ON task_runs(automation_requested_at_unix_ms, id)
    WHERE automation_requested_at_unix_ms > 0 AND completed_at_unix_ms = 0 AND interrupted_at_unix_ms = 0;


-- index task_runs_session_idx
CREATE INDEX task_runs_session_idx
    ON task_runs(session_id);


-- index task_transition_edges_target_placement_idx
CREATE INDEX task_transition_edges_target_placement_idx
    ON task_transition_edges(target_placement_id);


-- index task_transition_edges_target_placement_unique_idx
CREATE UNIQUE INDEX task_transition_edges_target_placement_unique_idx
    ON task_transition_edges(target_placement_id)
    WHERE target_placement_id IS NOT NULL
      AND trim(target_placement_id) != '';


-- index task_transition_edges_transition_state_idx
CREATE INDEX task_transition_edges_transition_state_idx
    ON task_transition_edges(task_transition_id, state);


-- index task_transition_edges_workflow_edge_idx
CREATE INDEX task_transition_edges_workflow_edge_idx
    ON task_transition_edges(workflow_edge_id);


-- index task_transitions_task_created_idx
CREATE INDEX task_transitions_task_created_idx
    ON task_transitions(task_id, created_at_unix_ms DESC);


-- index tasks_managed_worktree_idx
CREATE INDEX tasks_managed_worktree_idx
    ON tasks(managed_worktree_id);


-- index tasks_project_workflow_link_idx
CREATE INDEX tasks_project_workflow_link_idx
    ON tasks(project_workflow_link_id);


-- index tasks_project_workflow_link_updated_idx
CREATE INDEX tasks_project_workflow_link_updated_idx
    ON tasks(project_workflow_link_id, updated_at_unix_ms DESC, id DESC);


-- index tasks_short_id_idx
CREATE INDEX tasks_short_id_idx
    ON tasks(short_id);


-- index tasks_source_workspace_idx
CREATE INDEX tasks_source_workspace_idx
    ON tasks(source_workspace_id);


-- index workflow_edges_target_node_idx
CREATE INDEX workflow_edges_target_node_idx
    ON workflow_edges(target_node_id);


-- index workflow_edges_transition_group_sort_idx
CREATE INDEX workflow_edges_transition_group_sort_idx
    ON workflow_edges(transition_group_id, sort_order);


-- index workflow_node_groups_workflow_sort_idx
CREATE INDEX workflow_node_groups_workflow_sort_idx
    ON workflow_node_groups(workflow_id, sort_order);


-- index workflow_nodes_one_start_idx
CREATE UNIQUE INDEX workflow_nodes_one_start_idx
    ON workflow_nodes(workflow_id)
    WHERE kind = 'start';


-- index workflow_nodes_workflow_sort_idx
CREATE INDEX workflow_nodes_workflow_sort_idx
    ON workflow_nodes(workflow_id, sort_order);


-- index workspaces_project_canonical_root_idx
CREATE UNIQUE INDEX workspaces_project_canonical_root_idx ON workspaces(project_id, canonical_root_path);


-- index worktrees_workspace_idx
CREATE INDEX worktrees_workspace_idx ON worktrees(workspace_id);


-- trigger project_workflow_links_default_delete
-- +goose StatementBegin
CREATE TRIGGER project_workflow_links_default_delete
AFTER DELETE ON project_workflow_links
FOR EACH ROW
BEGIN
    UPDATE projects
    SET default_project_workflow_link_id = ''
    WHERE id = OLD.project_id
      AND default_project_workflow_link_id = OLD.id;
END;

-- +goose StatementEnd
-- trigger projects_default_workflow_link_insert
-- +goose StatementBegin
CREATE TRIGGER projects_default_workflow_link_insert
BEFORE INSERT ON projects
FOR EACH ROW
WHEN NEW.default_project_workflow_link_id != ''
 AND NOT EXISTS (
    SELECT 1
    FROM project_workflow_links pwl
    WHERE pwl.id = NEW.default_project_workflow_link_id
      AND pwl.project_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'default workflow link must belong to project');
END;

-- +goose StatementEnd

-- trigger projects_default_workflow_link_update
-- +goose StatementBegin
CREATE TRIGGER projects_default_workflow_link_update
BEFORE UPDATE OF id, default_project_workflow_link_id ON projects
FOR EACH ROW
WHEN NEW.default_project_workflow_link_id != ''
 AND NOT EXISTS (
    SELECT 1
    FROM project_workflow_links pwl
    WHERE pwl.id = NEW.default_project_workflow_link_id
      AND pwl.project_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'default workflow link must belong to project');
END;

-- +goose StatementEnd

-- trigger projects_primary_workspace_insert
-- +goose StatementBegin
CREATE TRIGGER projects_primary_workspace_insert
BEFORE INSERT ON projects
FOR EACH ROW
WHEN NEW.primary_workspace_id != ''
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    WHERE w.id = NEW.primary_workspace_id
      AND w.project_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'primary workspace must belong to project');
END;

-- +goose StatementEnd

-- trigger projects_primary_workspace_update
-- +goose StatementBegin
CREATE TRIGGER projects_primary_workspace_update
BEFORE UPDATE OF id, primary_workspace_id ON projects
FOR EACH ROW
WHEN NEW.primary_workspace_id != ''
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    WHERE w.id = NEW.primary_workspace_id
      AND w.project_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'primary workspace must belong to project');
END;

-- +goose StatementEnd

-- trigger sessions_workspace_project_insert
-- +goose StatementBegin
CREATE TRIGGER sessions_workspace_project_insert
BEFORE INSERT ON sessions
FOR EACH ROW
WHEN NEW.workspace_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    WHERE w.id = NEW.workspace_id
      AND w.project_id = NEW.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'session workspace must belong to project');
END;

-- +goose StatementEnd

-- trigger sessions_workspace_project_update
-- +goose StatementBegin
CREATE TRIGGER sessions_workspace_project_update
BEFORE UPDATE OF project_id, workspace_id ON sessions
FOR EACH ROW
WHEN NEW.workspace_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    WHERE w.id = NEW.workspace_id
      AND w.project_id = NEW.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'session workspace must belong to project');
END;

-- +goose StatementEnd

-- trigger sessions_worktree_workspace_insert
-- +goose StatementBegin
CREATE TRIGGER sessions_worktree_workspace_insert
BEFORE INSERT ON sessions
FOR EACH ROW
WHEN NEW.worktree_id IS NOT NULL
 AND (
    NEW.workspace_id IS NULL
    OR NOT EXISTS (
        SELECT 1
        FROM worktrees wt
        WHERE wt.id = NEW.worktree_id
          AND wt.workspace_id = NEW.workspace_id
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'session worktree must belong to session workspace');
END;

-- +goose StatementEnd

-- trigger sessions_worktree_workspace_update
-- +goose StatementBegin
CREATE TRIGGER sessions_worktree_workspace_update
BEFORE UPDATE OF workspace_id, worktree_id ON sessions
FOR EACH ROW
WHEN NEW.worktree_id IS NOT NULL
 AND (
    NEW.workspace_id IS NULL
    OR NOT EXISTS (
        SELECT 1
        FROM worktrees wt
        WHERE wt.id = NEW.worktree_id
          AND wt.workspace_id = NEW.workspace_id
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'session worktree must belong to session workspace');
END;

-- +goose StatementEnd

-- trigger task_node_placements_runtime_insert
-- +goose StatementBegin
CREATE TRIGGER task_node_placements_runtime_insert
BEFORE INSERT ON task_node_placements
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_records t
    JOIN workflow_nodes n ON n.id = NEW.node_id
    WHERE t.id = NEW.task_id
      AND n.workflow_id = t.workflow_id
)
OR (
    NEW.parallel_batch_transition_id IS NOT NULL
    AND trim(NEW.parallel_batch_transition_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        WHERE tt.id = NEW.parallel_batch_transition_id
          AND tt.task_id = NEW.task_id
    )
)
OR (
    NEW.parallel_branch_edge_id IS NOT NULL
    AND trim(NEW.parallel_branch_edge_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_records t
        JOIN workflow_edges e ON e.id = NEW.parallel_branch_edge_id
        JOIN workflow_transition_groups tg ON tg.id = e.transition_group_id
        JOIN workflow_nodes source ON source.id = tg.source_node_id
        WHERE t.id = NEW.task_id
          AND source.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task node placement references must stay within one task workflow');
END;

-- +goose StatementEnd

-- trigger task_node_placements_runtime_update
-- +goose StatementBegin
CREATE TRIGGER task_node_placements_runtime_update
BEFORE UPDATE OF task_id, node_id, parallel_batch_transition_id, parallel_branch_edge_id ON task_node_placements
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_records t
    JOIN workflow_nodes n ON n.id = NEW.node_id
    WHERE t.id = NEW.task_id
      AND n.workflow_id = t.workflow_id
)
OR (
    NEW.parallel_batch_transition_id IS NOT NULL
    AND trim(NEW.parallel_batch_transition_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        WHERE tt.id = NEW.parallel_batch_transition_id
          AND tt.task_id = NEW.task_id
    )
)
OR (
    NEW.parallel_branch_edge_id IS NOT NULL
    AND trim(NEW.parallel_branch_edge_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_records t
        JOIN workflow_edges e ON e.id = NEW.parallel_branch_edge_id
        JOIN workflow_transition_groups tg ON tg.id = e.transition_group_id
        JOIN workflow_nodes source ON source.id = tg.source_node_id
        WHERE t.id = NEW.task_id
          AND source.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task node placement references must stay within one task workflow');
END;

-- +goose StatementEnd

-- trigger task_transition_edges_runtime_insert
-- +goose StatementBegin
CREATE TRIGGER task_transition_edges_runtime_insert
BEFORE INSERT ON task_transition_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_transitions tt
    WHERE tt.id = NEW.task_transition_id
)
OR (
    NEW.target_placement_id IS NOT NULL
    AND trim(NEW.target_placement_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_node_placements p ON p.id = NEW.target_placement_id
        WHERE tt.id = NEW.task_transition_id
          AND p.task_id = tt.task_id
          AND (
              NEW.target_node_id IS NULL
              OR trim(NEW.target_node_id) = ''
              OR p.node_id = NEW.target_node_id
          )
    )
)
OR (
    NEW.target_node_id IS NOT NULL
    AND trim(NEW.target_node_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_records t ON t.id = tt.task_id
        JOIN workflow_nodes n ON n.id = NEW.target_node_id
        WHERE tt.id = NEW.task_transition_id
          AND n.workflow_id = t.workflow_id
    )
)
OR (
    NEW.workflow_edge_id IS NOT NULL
    AND trim(NEW.workflow_edge_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_records t ON t.id = tt.task_id
        JOIN workflow_edges e ON e.id = NEW.workflow_edge_id
        JOIN workflow_transition_groups tg ON tg.id = e.transition_group_id
        JOIN workflow_nodes source ON source.id = tg.source_node_id
        WHERE tt.id = NEW.task_transition_id
          AND source.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task transition edge references must stay within one task workflow');
END;

-- +goose StatementEnd

-- trigger task_transition_edges_runtime_update
-- +goose StatementBegin
CREATE TRIGGER task_transition_edges_runtime_update
BEFORE UPDATE OF task_transition_id, workflow_edge_id, target_node_id, target_placement_id ON task_transition_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_transitions tt
    WHERE tt.id = NEW.task_transition_id
)
OR (
    NEW.target_placement_id IS NOT NULL
    AND trim(NEW.target_placement_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_node_placements p ON p.id = NEW.target_placement_id
        WHERE tt.id = NEW.task_transition_id
          AND p.task_id = tt.task_id
          AND (
              NEW.target_node_id IS NULL
              OR trim(NEW.target_node_id) = ''
              OR p.node_id = NEW.target_node_id
          )
    )
)
OR (
    NEW.target_node_id IS NOT NULL
    AND trim(NEW.target_node_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_records t ON t.id = tt.task_id
        JOIN workflow_nodes n ON n.id = NEW.target_node_id
        WHERE tt.id = NEW.task_transition_id
          AND n.workflow_id = t.workflow_id
    )
)
OR (
    NEW.workflow_edge_id IS NOT NULL
    AND trim(NEW.workflow_edge_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_transitions tt
        JOIN task_records t ON t.id = tt.task_id
        JOIN workflow_edges e ON e.id = NEW.workflow_edge_id
        JOIN workflow_transition_groups tg ON tg.id = e.transition_group_id
        JOIN workflow_nodes source ON source.id = tg.source_node_id
        WHERE tt.id = NEW.task_transition_id
          AND source.workflow_id = t.workflow_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task transition edge references must stay within one task workflow');
END;

-- +goose StatementEnd

-- trigger task_transitions_runtime_insert
-- +goose StatementBegin
CREATE TRIGGER task_transitions_runtime_insert
BEFORE INSERT ON task_transitions
FOR EACH ROW
WHEN (
    NEW.source_run_id IS NOT NULL
    AND trim(NEW.source_run_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_run_records r
        WHERE r.id = NEW.source_run_id
          AND r.task_id = NEW.task_id
    )
)
OR (
    NEW.source_placement_id IS NOT NULL
    AND trim(NEW.source_placement_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_node_placements p
        WHERE p.id = NEW.source_placement_id
          AND p.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task transition references must stay within one task workflow');
END;

-- +goose StatementEnd

-- trigger task_transitions_runtime_update
-- +goose StatementBegin
CREATE TRIGGER task_transitions_runtime_update
BEFORE UPDATE OF task_id, source_run_id, source_placement_id, transition_id ON task_transitions
FOR EACH ROW
WHEN (
    NEW.source_run_id IS NOT NULL
    AND trim(NEW.source_run_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_run_records r
        WHERE r.id = NEW.source_run_id
          AND r.task_id = NEW.task_id
    )
)
OR (
    NEW.source_placement_id IS NOT NULL
    AND trim(NEW.source_placement_id) != ''
    AND NOT EXISTS (
        SELECT 1
        FROM task_node_placements p
        WHERE p.id = NEW.source_placement_id
          AND p.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'task transition references must stay within one task workflow');
END;

-- +goose StatementEnd

-- trigger tasks_managed_worktree_context_insert
-- +goose StatementBegin
CREATE TRIGGER tasks_managed_worktree_context_insert
BEFORE INSERT ON tasks
FOR EACH ROW
WHEN NEW.managed_worktree_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM worktrees wt
    JOIN workspaces source_workspace ON source_workspace.id = NEW.source_workspace_id
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE wt.id = NEW.managed_worktree_id
      AND wt.workspace_id = NEW.source_workspace_id
      AND source_workspace.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'managed worktree must belong to task source workspace');
END;

-- +goose StatementEnd

-- trigger tasks_managed_worktree_context_update
-- +goose StatementBegin
CREATE TRIGGER tasks_managed_worktree_context_update
BEFORE UPDATE OF project_workflow_link_id, source_workspace_id, managed_worktree_id ON tasks
FOR EACH ROW
WHEN NEW.managed_worktree_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM worktrees wt
    JOIN workspaces source_workspace ON source_workspace.id = NEW.source_workspace_id
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE wt.id = NEW.managed_worktree_id
      AND wt.workspace_id = NEW.source_workspace_id
      AND source_workspace.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'managed worktree must belong to task source workspace');
END;

-- +goose StatementEnd

-- trigger tasks_project_short_id_insert
-- +goose StatementBegin
CREATE TRIGGER tasks_project_short_id_insert
BEFORE INSERT ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM tasks existing
    JOIN project_workflow_links existing_link ON existing_link.id = existing.project_workflow_link_id
    JOIN project_workflow_links new_link ON new_link.id = NEW.project_workflow_link_id
    WHERE existing_link.project_id = new_link.project_id
      AND existing.short_id = NEW.short_id
)
BEGIN
    SELECT RAISE(ABORT, 'task short id must be unique within project');
END;

-- +goose StatementEnd

-- trigger tasks_project_short_id_update
-- +goose StatementBegin
CREATE TRIGGER tasks_project_short_id_update
BEFORE UPDATE OF project_workflow_link_id, short_id ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM tasks existing
    JOIN project_workflow_links existing_link ON existing_link.id = existing.project_workflow_link_id
    JOIN project_workflow_links new_link ON new_link.id = NEW.project_workflow_link_id
    WHERE existing.id != OLD.id
      AND existing_link.project_id = new_link.project_id
      AND existing.short_id = NEW.short_id
)
BEGIN
    SELECT RAISE(ABORT, 'task short id must be unique within project');
END;

-- +goose StatementEnd

-- trigger tasks_project_task_seq_insert
-- +goose StatementBegin
CREATE TRIGGER tasks_project_task_seq_insert
BEFORE INSERT ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM tasks existing
    JOIN project_workflow_links existing_link ON existing_link.id = existing.project_workflow_link_id
    JOIN project_workflow_links new_link ON new_link.id = NEW.project_workflow_link_id
    WHERE existing_link.project_id = new_link.project_id
      AND existing.task_seq = NEW.task_seq
)
BEGIN
    SELECT RAISE(ABORT, 'task sequence must be unique within project');
END;

-- +goose StatementEnd

-- trigger tasks_project_task_seq_update
-- +goose StatementBegin
CREATE TRIGGER tasks_project_task_seq_update
BEFORE UPDATE OF project_workflow_link_id, task_seq ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM tasks existing
    JOIN project_workflow_links existing_link ON existing_link.id = existing.project_workflow_link_id
    JOIN project_workflow_links new_link ON new_link.id = NEW.project_workflow_link_id
    WHERE existing.id != OLD.id
      AND existing_link.project_id = new_link.project_id
      AND existing.task_seq = NEW.task_seq
)
BEGIN
    SELECT RAISE(ABORT, 'task sequence must be unique within project');
END;

-- +goose StatementEnd

-- trigger tasks_source_workspace_project_insert
-- +goose StatementBegin
CREATE TRIGGER tasks_source_workspace_project_insert
BEFORE INSERT ON tasks
FOR EACH ROW
WHEN NEW.source_workspace_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE w.id = NEW.source_workspace_id
      AND w.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'source workspace must belong to task project');
END;

-- +goose StatementEnd

-- trigger tasks_source_workspace_project_update
-- +goose StatementBegin
CREATE TRIGGER tasks_source_workspace_project_update
BEFORE UPDATE OF project_workflow_link_id, source_workspace_id ON tasks
FOR EACH ROW
WHEN NEW.source_workspace_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM workspaces w
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE w.id = NEW.source_workspace_id
      AND w.project_id = pwl.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'source workspace must belong to task project');
END;

-- +goose StatementEnd

-- trigger workflow_edges_target_workflow_insert
-- +goose StatementBegin
CREATE TRIGGER workflow_edges_target_workflow_insert
BEFORE INSERT ON workflow_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM workflow_transition_groups tg
    JOIN workflow_nodes source ON source.id = tg.source_node_id
    JOIN workflow_nodes target ON target.id = NEW.target_node_id
    WHERE tg.id = NEW.transition_group_id
      AND target.workflow_id = source.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow edge target node must belong to transition group workflow');
END;

-- +goose StatementEnd

-- trigger workflow_edges_target_workflow_update
-- +goose StatementBegin
CREATE TRIGGER workflow_edges_target_workflow_update
BEFORE UPDATE OF transition_group_id, target_node_id ON workflow_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM workflow_transition_groups tg
    JOIN workflow_nodes source ON source.id = tg.source_node_id
    JOIN workflow_nodes target ON target.id = NEW.target_node_id
    WHERE tg.id = NEW.transition_group_id
      AND target.workflow_id = source.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow edge target node must belong to transition group workflow');
END;

-- +goose StatementEnd

-- trigger workflow_nodes_group_workflow_insert
-- +goose StatementBegin
CREATE TRIGGER workflow_nodes_group_workflow_insert
BEFORE INSERT ON workflow_nodes
FOR EACH ROW
WHEN NEW.group_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM workflow_node_groups g
    WHERE g.id = NEW.group_id
      AND g.workflow_id = NEW.workflow_id
  )
BEGIN
    SELECT RAISE(ABORT, 'workflow_nodes.group_id must belong to node workflow');
END;

-- +goose StatementEnd

-- trigger workflow_nodes_group_workflow_update
-- +goose StatementBegin
CREATE TRIGGER workflow_nodes_group_workflow_update
BEFORE UPDATE OF workflow_id, group_id ON workflow_nodes
FOR EACH ROW
WHEN NEW.group_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM workflow_node_groups g
    WHERE g.id = NEW.group_id
      AND g.workflow_id = NEW.workflow_id
  )
BEGIN
    SELECT RAISE(ABORT, 'workflow_nodes.group_id must belong to node workflow');
END;

-- +goose StatementEnd

-- trigger workspaces_child_refs_delete_cleanup
-- +goose StatementBegin
CREATE TRIGGER workspaces_child_refs_delete_cleanup
BEFORE DELETE ON workspaces
FOR EACH ROW
BEGIN
    UPDATE sessions
    SET worktree_id = NULL
    WHERE workspace_id = OLD.id
      AND worktree_id IN (
          SELECT wt.id
          FROM worktrees wt
          WHERE wt.workspace_id = OLD.id
      );

    UPDATE tasks
    SET managed_worktree_id = NULL
    WHERE source_workspace_id = OLD.id
      AND managed_worktree_id IN (
          SELECT wt.id
          FROM worktrees wt
          WHERE wt.workspace_id = OLD.id
      );
END;

-- +goose StatementEnd

-- trigger workspaces_primary_workspace_delete
-- +goose StatementBegin
CREATE TRIGGER workspaces_primary_workspace_delete
AFTER DELETE ON workspaces
FOR EACH ROW
BEGIN
    UPDATE projects
    SET primary_workspace_id = ''
    WHERE id = OLD.project_id
      AND primary_workspace_id = OLD.id;
END;

-- +goose StatementEnd

-- trigger workspaces_primary_workspace_update
-- +goose StatementBegin
CREATE TRIGGER workspaces_primary_workspace_update
BEFORE UPDATE OF id, project_id ON workspaces
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM projects p
    WHERE p.primary_workspace_id = OLD.id
      AND (
          p.id != NEW.project_id
          OR OLD.id != NEW.id
      )
)
BEGIN
    SELECT RAISE(ABORT, 'primary workspace must belong to project');
END;

-- +goose StatementEnd

-- trigger workspaces_session_project_update
-- +goose StatementBegin
CREATE TRIGGER workspaces_session_project_update
BEFORE UPDATE OF id, project_id ON workspaces
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM sessions s
    WHERE s.workspace_id = OLD.id
      AND (
          OLD.id != NEW.id
          OR s.project_id != NEW.project_id
      )
)
BEGIN
    SELECT RAISE(ABORT, 'session workspace must belong to project');
END;

-- +goose StatementEnd

-- trigger workspaces_task_source_project_update
-- +goose StatementBegin
CREATE TRIGGER workspaces_task_source_project_update
BEFORE UPDATE OF id, project_id ON workspaces
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM tasks t
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    WHERE t.source_workspace_id = OLD.id
      AND (
          OLD.id != NEW.id
          OR pwl.project_id != NEW.project_id
      )
)
BEGIN
    SELECT RAISE(ABORT, 'source workspace must belong to task project');
END;

-- +goose StatementEnd

-- trigger worktrees_managed_task_workspace_update
-- +goose StatementBegin
CREATE TRIGGER worktrees_managed_task_workspace_update
BEFORE UPDATE OF id, workspace_id ON worktrees
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM tasks t
    WHERE t.managed_worktree_id = OLD.id
      AND (
          OLD.id != NEW.id
          OR t.source_workspace_id IS NULL
          OR t.source_workspace_id != NEW.workspace_id
      )
)
BEGIN
    SELECT RAISE(ABORT, 'managed worktree must belong to task source workspace');
END;

-- +goose StatementEnd

-- trigger worktrees_session_workspace_update
-- +goose StatementBegin
CREATE TRIGGER worktrees_session_workspace_update
BEFORE UPDATE OF id, workspace_id ON worktrees
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM sessions s
    WHERE s.worktree_id = OLD.id
      AND (
          OLD.id != NEW.id
          OR s.workspace_id IS NULL
          OR s.workspace_id != NEW.workspace_id
      )
)
BEGIN
    SELECT RAISE(ABORT, 'session worktree must belong to session workspace');
END;

-- +goose StatementEnd
