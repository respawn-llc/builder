-- +goose Up

ALTER TABLE projects ADD COLUMN project_key TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN next_task_seq INTEGER NOT NULL DEFAULT 1 CHECK (next_task_seq >= 1);

CREATE UNIQUE INDEX projects_project_key_idx
    ON projects(project_key)
    WHERE project_key != '';

CREATE INDEX sessions_worktree_updated_idx
    ON sessions(worktree_id, updated_at_unix_ms DESC)
    WHERE worktree_id IS NOT NULL;

CREATE TABLE workflows (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
    description TEXT NOT NULL DEFAULT '',
    graph_revision INTEGER NOT NULL DEFAULT 1 CHECK (graph_revision >= 1),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json))
);

CREATE TABLE workflow_nodes (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    node_key TEXT NOT NULL CHECK (length(node_key) BETWEEN 1 AND 64),
    kind TEXT NOT NULL CHECK (kind IN ('start', 'agent', 'script', 'join', 'terminal')),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    subagent_role TEXT NOT NULL DEFAULT '',
    prompt_template TEXT NOT NULL DEFAULT '',
    output_fields_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(output_fields_json)),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json)),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, node_key)
);

CREATE UNIQUE INDEX workflow_nodes_one_start_idx
    ON workflow_nodes(workflow_id)
    WHERE kind = 'start';

CREATE INDEX workflow_nodes_workflow_sort_idx
    ON workflow_nodes(workflow_id, sort_order);

CREATE TABLE workflow_transition_groups (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    source_node_id TEXT NOT NULL,
    transition_id TEXT NOT NULL CHECK (length(transition_id) BETWEEN 1 AND 64),
    display_name TEXT NOT NULL DEFAULT '' CHECK (length(display_name) <= 120),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json)),
    UNIQUE (workflow_id, id),
    UNIQUE (source_node_id, transition_id),
    FOREIGN KEY (workflow_id, source_node_id) REFERENCES workflow_nodes(workflow_id, id) ON DELETE CASCADE
);

CREATE INDEX workflow_transition_groups_source_transition_idx
    ON workflow_transition_groups(source_node_id, transition_id);

CREATE TABLE workflow_edges (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    transition_group_id TEXT NOT NULL,
    edge_key TEXT NOT NULL CHECK (length(edge_key) BETWEEN 1 AND 64),
    target_node_id TEXT NOT NULL,
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    context_mode TEXT NOT NULL CHECK (context_mode IN ('new_session', 'continue_session', 'compact_and_continue_session')),
    input_bindings_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_bindings_json)),
    output_requirements_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_requirements_json)),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json)),
    UNIQUE (workflow_id, id),
    UNIQUE (transition_group_id, edge_key),
    FOREIGN KEY (workflow_id, transition_group_id) REFERENCES workflow_transition_groups(workflow_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workflow_id, target_node_id) REFERENCES workflow_nodes(workflow_id, id) ON DELETE CASCADE
);

CREATE INDEX workflow_edges_transition_group_sort_idx
    ON workflow_edges(transition_group_id, sort_order);

CREATE INDEX workflow_edges_target_node_idx
    ON workflow_edges(target_node_id);

CREATE TABLE project_workflow_links (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE RESTRICT,
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    unlinked_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (unlinked_at_unix_ms >= 0),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    UNIQUE (project_id, id),
    UNIQUE (project_id, id, workflow_id)
);

CREATE UNIQUE INDEX project_workflow_links_active_workflow_idx
    ON project_workflow_links(project_id, workflow_id)
    WHERE unlinked_at_unix_ms = 0;

CREATE UNIQUE INDEX project_workflow_links_default_idx
    ON project_workflow_links(project_id)
    WHERE is_default = 1 AND unlinked_at_unix_ms = 0;

CREATE INDEX project_workflow_links_project_active_idx
    ON project_workflow_links(project_id, is_default)
    WHERE unlinked_at_unix_ms = 0;

CREATE INDEX project_workflow_links_workflow_idx
    ON project_workflow_links(workflow_id);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    project_workflow_link_id TEXT NOT NULL,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE RESTRICT,
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    task_seq INTEGER NOT NULL CHECK (task_seq >= 1),
    short_id TEXT NOT NULL,
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    body TEXT NOT NULL CHECK (length(trim(body)) > 0),
    source_url TEXT NOT NULL DEFAULT '',
    managed_worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    canceled_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (canceled_at_unix_ms >= 0),
    cancellation_reason TEXT NOT NULL DEFAULT '',
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json)),
    UNIQUE (project_id, task_seq),
    UNIQUE (project_id, short_id),
    FOREIGN KEY (project_id, project_workflow_link_id, workflow_id) REFERENCES project_workflow_links(project_id, id, workflow_id) ON DELETE RESTRICT
);

CREATE INDEX tasks_project_workflow_link_idx
    ON tasks(project_workflow_link_id);

CREATE INDEX tasks_managed_worktree_idx
    ON tasks(managed_worktree_id);

CREATE INDEX tasks_project_updated_idx
    ON tasks(project_id, updated_at_unix_ms DESC);

CREATE INDEX tasks_project_short_id_idx
    ON tasks(project_id, short_id);

CREATE TABLE task_node_placements (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('active', 'waiting_approval', 'completed', 'superseded')),
    created_by_transition_id TEXT,
    parallel_batch_transition_id TEXT,
    parallel_branch_edge_id TEXT REFERENCES workflow_edges(id) ON DELETE SET NULL,
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    FOREIGN KEY (created_by_transition_id) REFERENCES task_transitions(id) ON DELETE SET NULL,
    FOREIGN KEY (parallel_batch_transition_id) REFERENCES task_transitions(id) ON DELETE SET NULL
);

CREATE INDEX task_node_placements_task_state_idx
    ON task_node_placements(task_id, state);

CREATE INDEX task_node_placements_node_state_idx
    ON task_node_placements(node_id, state);

CREATE INDEX task_node_placements_parallel_batch_idx
    ON task_node_placements(parallel_batch_transition_id, parallel_branch_edge_id, state);

CREATE TABLE task_runs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    placement_id TEXT NOT NULL REFERENCES task_node_placements(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE RESTRICT,
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

CREATE INDEX task_runs_placement_idx
    ON task_runs(placement_id);

CREATE INDEX task_runs_session_idx
    ON task_runs(session_id);

CREATE INDEX task_runs_runnable_idx
    ON task_runs(automation_requested_at_unix_ms, id)
    WHERE automation_requested_at_unix_ms > 0 AND completed_at_unix_ms = 0 AND interrupted_at_unix_ms = 0;

CREATE INDEX task_runs_outcome_idx
    ON task_runs(started_at_unix_ms, completed_at_unix_ms, interrupted_at_unix_ms);

CREATE INDEX task_runs_task_created_idx
    ON task_runs(task_id, created_at_unix_ms DESC);

CREATE TABLE task_transitions (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    source_run_id TEXT REFERENCES task_runs(id) ON DELETE SET NULL,
    source_placement_id TEXT REFERENCES task_node_placements(id) ON DELETE SET NULL,
    source_node_id TEXT REFERENCES workflow_nodes(id) ON DELETE SET NULL,
    source_node_key TEXT NOT NULL DEFAULT '',
    source_node_display_name TEXT NOT NULL DEFAULT '',
    transition_group_id TEXT REFERENCES workflow_transition_groups(id) ON DELETE SET NULL,
    transition_id TEXT NOT NULL,
    transition_display_name TEXT NOT NULL DEFAULT '',
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    actor TEXT NOT NULL CHECK (actor IN ('agent', 'script', 'user', 'system')),
    state TEXT NOT NULL CHECK (state IN ('pending_approval', 'approved', 'applied', 'rejected', 'invalid')),
    commentary TEXT NOT NULL DEFAULT '' CHECK (length(commentary) <= 65536),
    output_values_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_values_json)),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    applied_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (applied_at_unix_ms >= 0)
);

CREATE INDEX task_transitions_task_created_idx
    ON task_transitions(task_id, created_at_unix_ms DESC);

CREATE TABLE task_transition_edges (
    id TEXT PRIMARY KEY,
    task_transition_id TEXT NOT NULL REFERENCES task_transitions(id) ON DELETE CASCADE,
    workflow_edge_id TEXT REFERENCES workflow_edges(id) ON DELETE SET NULL,
    edge_key TEXT NOT NULL DEFAULT '',
    workflow_revision_seen INTEGER NOT NULL CHECK (workflow_revision_seen >= 1),
    target_node_id TEXT REFERENCES workflow_nodes(id) ON DELETE SET NULL,
    target_node_key TEXT NOT NULL DEFAULT '',
    target_node_display_name TEXT NOT NULL DEFAULT '',
    target_node_kind TEXT NOT NULL DEFAULT '',
    target_placement_id TEXT REFERENCES task_node_placements(id) ON DELETE SET NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'applied', 'completed', 'blocked')),
    context_mode TEXT NOT NULL DEFAULT '' CHECK (context_mode IN ('', 'new_session', 'continue_session', 'compact_and_continue_session')),
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    input_bindings_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_bindings_json)),
    output_requirements_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_requirements_json)),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json))
);

CREATE INDEX task_transition_edges_transition_state_idx
    ON task_transition_edges(task_transition_id, state);

CREATE INDEX task_transition_edges_workflow_edge_idx
    ON task_transition_edges(workflow_edge_id);

CREATE INDEX task_transition_edges_target_placement_idx
    ON task_transition_edges(target_placement_id);

CREATE TABLE task_comments (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    body TEXT NOT NULL CHECK (length(body) <= 262144),
    author_kind TEXT NOT NULL CHECK (author_kind IN ('user', 'agent')),
    author_id TEXT NOT NULL DEFAULT '',
    source_run_id TEXT REFERENCES task_runs(id) ON DELETE SET NULL,
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    deleted_at_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK (deleted_at_unix_ms >= 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json))
);

CREATE INDEX task_comments_task_visible_updated_idx
    ON task_comments(task_id, deleted_at_unix_ms, updated_at_unix_ms DESC);
