CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);

CREATE TABLE project_labels (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0), ordinal INTEGER NOT NULL DEFAULT 1
CHECK (ordinal BETWEEN 1 AND 200),
    UNIQUE (project_id, name COLLATE kent_label_casefold_v1)
);

CREATE TABLE "project_workflow_links" (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workflow_id BLOB NOT NULL REFERENCES workflows(id) ON DELETE RESTRICT
        CHECK (typeof(workflow_id) = 'blob' AND length(workflow_id) = 16),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    UNIQUE (project_id, id),
    UNIQUE (project_id, workflow_id)
);

CREATE TABLE "projects" (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    project_key TEXT NOT NULL DEFAULT '',
    next_task_seq INTEGER NOT NULL DEFAULT 1 CHECK (next_task_seq >= 1),
    default_project_workflow_link_id TEXT,
    primary_workspace_id TEXT NOT NULL DEFAULT ''
);

CREATE TABLE session_prompt_history_entries (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL CHECK (trim(source_id) <> ''),
    text TEXT NOT NULL CHECK (trim(text) <> ''),
    created_at_unix_ms INTEGER NOT NULL
);

CREATE TABLE "session_workflow_node_associations" (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    node_id BLOB NOT NULL
        CHECK (typeof(node_id) = 'blob' AND length(node_id) = 16 AND node_id != zeroblob(16)),
    transition_branch_key TEXT
        CHECK (transition_branch_key IS NULL OR length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    association_status TEXT NOT NULL CHECK (association_status IN ('current', 'historical')),
    source_session_id TEXT REFERENCES sessions(id) ON DELETE RESTRICT,
    associated_at_unix_ms INTEGER NOT NULL CHECK (associated_at_unix_ms > 0),
    CHECK (
        (association_status = 'current' AND source_session_id IS NOT NULL)
        OR association_status = 'historical'
    )
);

CREATE TABLE "sessions" (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
    worktree_id TEXT REFERENCES worktrees(id) ON DELETE SET NULL,
    artifact_relpath TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    first_prompt_preview TEXT NOT NULL DEFAULT '',
    input_draft TEXT NOT NULL DEFAULT '',
    category TEXT CHECK (category IS NULL OR category IN ('main', 'subagent')),
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL,
    last_sequence INTEGER NOT NULL DEFAULT 0,
    model_request_count INTEGER NOT NULL DEFAULT 0,
    launch_visible INTEGER NOT NULL DEFAULT 0,
    cwd_relpath TEXT NOT NULL DEFAULT '.',
    continuation_json TEXT NOT NULL DEFAULT '{}',
    locked_json TEXT NOT NULL DEFAULT '{}',
    usage_state_json TEXT NOT NULL DEFAULT '{}',
    metadata_json TEXT NOT NULL DEFAULT '{}'
, previous_session_id TEXT
    CHECK (previous_session_id IS NULL OR length(trim(previous_session_id)) > 0), parent_agent_session_id TEXT
    CHECK (parent_agent_session_id IS NULL OR length(trim(parent_agent_session_id)) > 0), task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL, completed_compaction_count INTEGER
CHECK (completed_compaction_count IS NULL OR completed_compaction_count >= 0), manual_compact_eligible INTEGER
CHECK (manual_compact_eligible IS NULL OR manual_compact_eligible IN (0, 1)));

CREATE TABLE task_active_fanout_branches (
    task_id TEXT NOT NULL REFERENCES task_active_fanouts(task_id) ON DELETE CASCADE,
    transition_branch_key TEXT NOT NULL CHECK (length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    arrival_state TEXT NOT NULL CHECK (arrival_state IN ('pending', 'arrived')),
    arrival_values_json TEXT
        CHECK (arrival_values_json IS NULL OR (json_valid(arrival_values_json) AND json_type(arrival_values_json) = 'object')), continuation_source_kind TEXT
    CHECK (continuation_source_kind IS NULL OR continuation_source_kind IN (
        'exact',
        'deferred_self',
        'absent'
    )), continuation_source_session_id TEXT REFERENCES sessions(id) ON DELETE RESTRICT, legacy_materialized INTEGER NOT NULL DEFAULT 1
    CHECK (legacy_materialized IN (0, 1)),
    PRIMARY KEY (task_id, transition_branch_key),
    CHECK (
        (arrival_state = 'pending' AND arrival_values_json IS NULL)
        OR
        (arrival_state = 'arrived' AND arrival_values_json IS NOT NULL)
    )
);

CREATE TABLE task_active_fanouts (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE "task_comments" (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    body TEXT NOT NULL CHECK (length(body) <= 262144),
    author_kind TEXT NOT NULL CHECK (author_kind IN ('user', 'agent')),
    author_id TEXT NOT NULL DEFAULT '',
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0)
);

CREATE TABLE "task_current_nodes" (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    node_id BLOB NOT NULL REFERENCES "workflow_nodes"(id) ON DELETE RESTRICT
        CHECK (typeof(node_id) = 'blob' AND length(node_id) = 16 AND node_id != zeroblob(16)),
    transition_branch_key TEXT
        CHECK (transition_branch_key IS NULL OR length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    current_input_values_json TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(current_input_values_json) AND json_type(current_input_values_json) = 'object'),
    prior_node_values_json TEXT NOT NULL DEFAULT '{"transition_parameters":{}}'
        CHECK (json_valid(prior_node_values_json) AND json_type(prior_node_values_json) = 'object'),
    session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    scheduling_state TEXT
        CHECK (scheduling_state IS NULL OR scheduling_state IN ('ready', 'admitted', 'interrupted', 'failed')),
    interruption_reason TEXT
        CHECK (interruption_reason IS NULL OR length(trim(interruption_reason)) > 0),
    interruption_detail_json TEXT
        CHECK (
            interruption_detail_json IS NULL
            OR (json_valid(interruption_detail_json) AND json_type(interruption_detail_json) = 'object')
        ),
    interrupted_at_unix_ms INTEGER
        CHECK (interrupted_at_unix_ms IS NULL OR interrupted_at_unix_ms > 0),
    entered_by_edge_id BLOB
        CHECK (
            entered_by_edge_id IS NULL
            OR (
                typeof(entered_by_edge_id) = 'blob'
                AND length(entered_by_edge_id) = 16
                AND entered_by_edge_id != zeroblob(16)
            )
        ),
    effective_assignee TEXT
        CHECK (effective_assignee IS NULL OR length(trim(effective_assignee)) > 0),
    effective_thinking TEXT
        CHECK (effective_thinking IS NULL OR length(trim(effective_thinking)) > 0),
    assignee_origin TEXT
        CHECK (assignee_origin IS NULL OR assignee_origin IN (
            'configured_fallback',
            'transition_selected',
            'retained_session'
        )), continuation_source_kind TEXT
    CHECK (continuation_source_kind IS NULL OR continuation_source_kind IN (
        'exact',
        'deferred_self',
        'absent'
    )), continuation_source_session_id TEXT REFERENCES sessions(id) ON DELETE RESTRICT, legacy_materialized INTEGER NOT NULL DEFAULT 1
    CHECK (legacy_materialized IN (0, 1)),
    FOREIGN KEY (task_id, transition_branch_key)
        REFERENCES task_active_fanout_branches(task_id, transition_branch_key)
        ON DELETE RESTRICT,
    CHECK (
        (
            scheduling_state = 'interrupted'
            AND interruption_reason IS NOT NULL
            AND interruption_detail_json IS NOT NULL
            AND interrupted_at_unix_ms IS NOT NULL
        )
        OR (
            scheduling_state IS NULL OR scheduling_state != 'interrupted'
        )
        AND interruption_reason IS NULL
        AND interruption_detail_json IS NULL
        AND interrupted_at_unix_ms IS NULL
    )
);

CREATE TABLE task_dependencies (
    blocker_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    blocked_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (blocker_task_id, blocked_task_id)
);

CREATE TABLE task_label_assignments (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    label_id TEXT NOT NULL REFERENCES project_labels(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, label_id)
);

CREATE TABLE "task_pending_approval_branches" (
    approval_id TEXT NOT NULL REFERENCES "task_pending_approvals"(id) ON DELETE CASCADE,
    transition_branch_key TEXT NOT NULL CHECK (length(trim(transition_branch_key)) BETWEEN 1 AND 64),
    target_snapshot_json TEXT NOT NULL
        CHECK (json_valid(target_snapshot_json) AND json_type(target_snapshot_json) = 'object'),
    effective_edge_configuration_json TEXT NOT NULL
        CHECK (json_valid(effective_edge_configuration_json) AND json_type(effective_edge_configuration_json) = 'object'),
    context_source_resolution_json TEXT NOT NULL
        CHECK (json_valid(context_source_resolution_json) AND json_type(context_source_resolution_json) = 'object'),
    PRIMARY KEY (approval_id, transition_branch_key)
);

CREATE TABLE "task_pending_approvals" (
    id TEXT PRIMARY KEY,
    source_task_id TEXT NOT NULL,
    source_node_id BLOB NOT NULL
        CHECK (
            typeof(source_node_id) = 'blob'
            AND length(source_node_id) = 16
            AND source_node_id != zeroblob(16)
        ),
    source_transition_branch_key TEXT
        CHECK (source_transition_branch_key IS NULL OR length(trim(source_transition_branch_key)) BETWEEN 1 AND 64),
    source_session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    workflow_version INTEGER NOT NULL CHECK (workflow_version >= 1),
    transition_snapshot_json TEXT NOT NULL
        CHECK (json_valid(transition_snapshot_json) AND json_type(transition_snapshot_json) = 'object'),
    materialized_values_json TEXT NOT NULL
        CHECK (json_valid(materialized_values_json) AND json_type(materialized_values_json) = 'object'),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms > 0)
);

CREATE TABLE task_search_documents (
    document_id INTEGER PRIMARY KEY,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('short_id', 'title', 'body', 'comment')),
    task_id TEXT REFERENCES tasks(id) ON DELETE CASCADE,
    comment_id TEXT REFERENCES task_comments(id) ON DELETE CASCADE,
    CHECK (
        (source_kind IN ('short_id', 'title', 'body') AND task_id IS NOT NULL AND comment_id IS NULL)
        OR
        (source_kind = 'comment' AND task_id IS NULL AND comment_id IS NOT NULL)
    )
);

CREATE VIRTUAL TABLE task_search_fts
USING fts5(
    title,
    body,
    comment,
    content = 'task_search_content',
    content_rowid = 'document_id',
    tokenize = 'trigram case_sensitive 0 remove_diacritics 1'
);

CREATE TABLE 'task_search_fts_config'(k PRIMARY KEY, v) WITHOUT ROWID;

CREATE TABLE 'task_search_fts_data'(id INTEGER PRIMARY KEY, block BLOB);

CREATE TABLE 'task_search_fts_docsize'(id INTEGER PRIMARY KEY, sz BLOB);

CREATE TABLE 'task_search_fts_idx'(segid, term, pgno, PRIMARY KEY(segid, term)) WITHOUT ROWID;

CREATE VIRTUAL TABLE task_search_short_id_fts
USING fts5(
    short_id,
    content = 'task_search_content',
    content_rowid = 'document_id',
    tokenize = 'trigram case_sensitive 0 remove_diacritics 1'
);

CREATE TABLE 'task_search_short_id_fts_config'(k PRIMARY KEY, v) WITHOUT ROWID;

CREATE TABLE 'task_search_short_id_fts_data'(id INTEGER PRIMARY KEY, block BLOB);

CREATE TABLE 'task_search_short_id_fts_docsize'(id INTEGER PRIMARY KEY, sz BLOB);

CREATE TABLE 'task_search_short_id_fts_idx'(segid, term, pgno, PRIMARY KEY(segid, term)) WITHOUT ROWID;

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
    execution_target_mode TEXT
        CHECK (execution_target_mode IS NULL OR execution_target_mode IN ('none', 'head', 'default_branch', 'custom_ref')),
    execution_target_requested_ref TEXT
        CHECK (execution_target_requested_ref IS NULL OR length(trim(execution_target_requested_ref)) > 0),
    execution_target_resolved_ref TEXT
        CHECK (execution_target_resolved_ref IS NULL OR length(trim(execution_target_resolved_ref)) > 0),
    execution_target_commit_oid TEXT
        CHECK (execution_target_commit_oid IS NULL OR length(trim(execution_target_commit_oid)) > 0),
    execution_target_provenance TEXT
        CHECK (execution_target_provenance IS NULL OR execution_target_provenance IN ('resolved', 'legacy_observed')),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json)), pending_initial_managed_branch_name TEXT
    CHECK (
        pending_initial_managed_branch_name IS NULL
        OR (
            length(trim(pending_initial_managed_branch_name)) > 0
            AND pending_initial_managed_branch_name = trim(pending_initial_managed_branch_name)
            AND execution_target_mode IS NULL
            AND managed_worktree_id IS NULL
        )
    ),
    CHECK (
        (
            execution_target_mode IS NULL
            AND execution_target_requested_ref IS NULL
            AND execution_target_resolved_ref IS NULL
            AND execution_target_commit_oid IS NULL
            AND execution_target_provenance IS NULL
        )
        OR (
            execution_target_mode = 'none'
            AND execution_target_requested_ref IS NULL
            AND execution_target_resolved_ref IS NULL
            AND execution_target_commit_oid IS NULL
            AND execution_target_provenance = 'resolved'
            AND managed_worktree_id IS NULL
        )
        OR (
            execution_target_mode IN ('head', 'default_branch', 'custom_ref')
            AND execution_target_requested_ref IS NOT NULL
            AND execution_target_commit_oid IS NOT NULL
            AND execution_target_provenance IN ('resolved', 'legacy_observed')
        )
    )
);

CREATE TABLE "workflow_edges" (
    id BLOB PRIMARY KEY
        CHECK (typeof(id) = 'blob' AND length(id) = 16 AND id != zeroblob(16)),
    transition_group_id BLOB NOT NULL REFERENCES "workflow_transition_groups"(id) ON DELETE CASCADE
        CHECK (
            typeof(transition_group_id) = 'blob'
            AND length(transition_group_id) = 16
            AND transition_group_id != zeroblob(16)
        ),
    edge_key TEXT NOT NULL CHECK (length(edge_key) BETWEEN 1 AND 64),
    target_node_id BLOB NOT NULL REFERENCES "workflow_nodes"(id) ON DELETE CASCADE
        CHECK (typeof(target_node_id) = 'blob' AND length(target_node_id) = 16 AND target_node_id != zeroblob(16)),
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    context_mode TEXT NOT NULL CHECK (context_mode IN ('new_session', 'continue_session', 'compact_and_continue_session')),
    input_bindings_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_bindings_json)),
    output_requirements_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_requirements_json)),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    context_source_kind TEXT NOT NULL DEFAULT 'immediate_source'
        CHECK (context_source_kind IN ('immediate_source', 'selected_node', 'previous_target', 'previous_target_or_new')),
    context_source_node_key TEXT NOT NULL DEFAULT ''
        CHECK (
            (
                context_source_kind IN ('immediate_source', 'previous_target', 'previous_target_or_new')
                AND context_source_node_key = ''
            )
            OR (context_source_kind = 'selected_node' AND length(context_source_node_key) BETWEEN 1 AND 64)
        ),
    prompt_template TEXT NOT NULL DEFAULT '',
    parameters_json TEXT NOT NULL DEFAULT '[]'
        CHECK (json_valid(parameters_json) AND json_type(parameters_json) = 'array'),
    assignee_selection TEXT NOT NULL DEFAULT 'configured'
        CHECK (assignee_selection IN ('configured', 'previous_node')),
    thinking_selection TEXT NOT NULL DEFAULT 'configured'
        CHECK (thinking_selection IN ('configured', 'previous_node')),
    UNIQUE (transition_group_id, edge_key)
);

CREATE TABLE "workflow_node_groups" (
    id BLOB PRIMARY KEY
        CHECK (typeof(id) = 'blob' AND length(id) = 16 AND id != zeroblob(16)),
    workflow_id BLOB NOT NULL REFERENCES workflows(id) ON DELETE CASCADE
        CHECK (typeof(workflow_id) = 'blob' AND length(workflow_id) = 16),
    group_key TEXT NOT NULL CHECK (length(group_key) BETWEEN 1 AND 64),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, group_key)
);

CREATE TABLE "workflow_nodes" (
    id BLOB PRIMARY KEY
        CHECK (typeof(id) = 'blob' AND length(id) = 16 AND id != zeroblob(16)),
    workflow_id BLOB NOT NULL REFERENCES workflows(id) ON DELETE CASCADE
        CHECK (typeof(workflow_id) = 'blob' AND length(workflow_id) = 16),
    node_key TEXT NOT NULL CHECK (length(node_key) BETWEEN 1 AND 64),
    kind TEXT NOT NULL CHECK (kind IN ('start', 'agent', 'script', 'join', 'terminal')),
    display_name TEXT NOT NULL CHECK (length(trim(display_name)) BETWEEN 1 AND 120),
    subagent_role TEXT NOT NULL DEFAULT '',
    group_id BLOB REFERENCES "workflow_node_groups"(id) ON DELETE SET NULL
        CHECK (
            group_id IS NULL
            OR (typeof(group_id) = 'blob' AND length(group_id) = 16 AND group_id != zeroblob(16))
        ),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    join_input_providers_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(join_input_providers_json)),
    completion_mode TEXT NOT NULL DEFAULT ''
        CHECK (
            completion_mode IN ('', 'auto', 'structured_output', 'tool', 'shell_command', 'unstructured_output')
            AND (completion_mode = '' OR kind = 'agent')
        ),
    script_path TEXT CHECK (
        script_path IS NULL OR (kind = 'script' AND length(trim(script_path)) > 0)
    ),
    UNIQUE (workflow_id, id),
    UNIQUE (workflow_id, node_key)
);

CREATE TABLE "workflow_transition_groups" (
    id BLOB PRIMARY KEY
        CHECK (typeof(id) = 'blob' AND length(id) = 16 AND id != zeroblob(16)),
    source_node_id BLOB NOT NULL REFERENCES "workflow_nodes"(id) ON DELETE CASCADE
        CHECK (typeof(source_node_id) = 'blob' AND length(source_node_id) = 16 AND source_node_id != zeroblob(16)),
    transition_id TEXT NOT NULL CHECK (length(transition_id) BETWEEN 1 AND 64),
    display_name TEXT NOT NULL DEFAULT '' CHECK (length(display_name) <= 120),
    sort_order INTEGER NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    UNIQUE (source_node_id, transition_id)
);

CREATE TABLE "workflows" (
    id BLOB NOT NULL PRIMARY KEY CHECK (typeof(id) = 'blob' AND length(id) = 16),
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    execution_target_policy TEXT NOT NULL DEFAULT 'ask_on_first_execution'
        CHECK (execution_target_policy IN ('none', 'head', 'default_branch', 'custom_ref', 'ask_on_first_execution')),
    execution_target_custom_ref TEXT
        CHECK (execution_target_custom_ref IS NULL OR length(trim(execution_target_custom_ref)) > 0),
    created_at_unix_ms INTEGER NOT NULL CHECK (created_at_unix_ms >= 0),
    updated_at_unix_ms INTEGER NOT NULL CHECK (updated_at_unix_ms >= 0),
    CHECK (execution_target_policy = 'custom_ref' OR execution_target_custom_ref IS NULL)
);

CREATE TABLE "workspaces" (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    canonical_root_path TEXT NOT NULL,
    git_metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL
, chat_draft_json TEXT
CHECK (chat_draft_json IS NULL OR json_valid(chat_draft_json)));

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
, creation_base_commit_oid TEXT
    CHECK (creation_base_commit_oid IS NULL OR length(trim(creation_base_commit_oid)) > 0));

CREATE VIEW project_default_workflow_identity AS
SELECT
    CAST('project_id' AS TEXT) AS project_id,
    CAST(NULL AS BLOB) AS workflow_id,
    CAST(NULL AS TEXT) AS workflow_name
WHERE 0
UNION ALL
SELECT
    p.id AS project_id,
    default_workflow.id AS workflow_id,
    default_workflow.name AS workflow_name
FROM projects p
LEFT JOIN project_workflow_links default_link
    ON default_link.id = p.default_project_workflow_link_id
   AND default_link.project_id = p.id
LEFT JOIN workflows default_workflow ON default_workflow.id = default_link.workflow_id;

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

CREATE VIEW task_records AS
SELECT
    task.id,
    link.project_id,
    task.project_workflow_link_id,
    link.workflow_id,
    task.workflow_revision_seen,
    task.task_seq,
    task.short_id,
    task.title,
    task.body,
    task.source_url,
    task.source_workspace_id,
    task.managed_worktree_id,
    task.pending_initial_managed_branch_name,
    task.execution_target_mode,
    task.execution_target_requested_ref,
    task.execution_target_resolved_ref,
    task.execution_target_commit_oid,
    task.execution_target_provenance,
    task.created_at_unix_ms,
    task.updated_at_unix_ms,
    task.metadata_json
FROM tasks task
JOIN project_workflow_links link ON link.id = task.project_workflow_link_id;

CREATE VIEW task_search_content AS
SELECT
    document.document_id,
    CASE WHEN document.source_kind = 'short_id' THEN task.short_id END AS short_id,
    CASE WHEN document.source_kind = 'title' THEN task.title END AS title,
    CASE WHEN document.source_kind = 'body' THEN task.body END AS body,
    CASE WHEN document.source_kind = 'comment' THEN comment.body END AS comment
FROM task_search_documents document
LEFT JOIN tasks task ON task.id = document.task_id
LEFT JOIN task_comments comment ON comment.id = document.comment_id;

CREATE VIEW workflow_task_status_records AS
WITH current_positions AS (
    SELECT
        task_id,
        node_id,
        scheduling_state,
        interruption_reason
    FROM task_current_nodes
), status_inputs AS (
    SELECT
        task.id AS task_id,
        EXISTS (
            SELECT 1
            FROM current_positions position
            JOIN workflow_nodes node ON node.id = position.node_id
            WHERE position.task_id = task.id
              AND node.kind = 'terminal'
        ) AS has_done,
        EXISTS (
            SELECT 1
            FROM task_pending_approvals approval
            WHERE approval.source_task_id = task.id
        ) AS has_waiting_approval,
        EXISTS (
            SELECT 1
            FROM current_positions position
            WHERE position.task_id = task.id
              AND position.scheduling_state = 'interrupted'
        ) AS has_interrupted,
        EXISTS (
            SELECT 1
            FROM current_positions position
            WHERE position.task_id = task.id
              AND position.scheduling_state = 'interrupted'
              AND position.interruption_reason NOT IN (
                  'user_interrupt',
                  'workflow_runtime_canceled'
              )
        ) AS has_interrupted_attention,
        EXISTS (
            SELECT 1
            FROM current_positions position
            JOIN workflow_nodes node ON node.id = position.node_id
            WHERE position.task_id = task.id
              AND node.kind = 'start'
        ) AS has_backlog
    FROM task_records task
)
SELECT
    task.id AS task_id,
    CAST(input.has_done AS INTEGER) AS is_done,
    CASE
        WHEN input.has_done THEN 'done'
        WHEN input.has_waiting_approval THEN 'waiting_approval'
        WHEN input.has_interrupted THEN 'interrupted'
        WHEN input.has_backlog THEN 'backlog'
        ELSE 'active'
    END AS kind,
    CASE
        WHEN input.has_done THEN 1
        WHEN input.has_waiting_approval THEN 3
        WHEN input.has_interrupted THEN 4
        WHEN input.has_backlog THEN 7
        ELSE 8
    END AS primary_status_rank,
    COALESCE((
        SELECT json_group_array(node_id)
        FROM (
            SELECT kent_graph_entity_id_text_v1(position.node_id) AS node_id
            FROM current_positions position
            WHERE position.task_id = task.id
            ORDER BY position.node_id
        )
    ), '[]') AS node_ids_json,
    COALESCE((
        SELECT json_group_array(attention_type)
        FROM (
            SELECT 'approval' AS attention_type
            WHERE input.has_waiting_approval
            UNION
            SELECT 'interrupted'
            WHERE input.has_interrupted_attention
            ORDER BY attention_type
        )
    ), '[]') AS attention_types_json
FROM task_records task
JOIN status_inputs input ON input.task_id = task.id;

CREATE UNIQUE INDEX project_labels_project_ordinal_idx
    ON project_labels(project_id, ordinal);

CREATE INDEX project_workflow_links_workflow_idx ON project_workflow_links(workflow_id);

CREATE INDEX projects_primary_workspace_idx
    ON projects(primary_workspace_id)
    WHERE primary_workspace_id != '';

CREATE UNIQUE INDEX projects_project_key_idx
    ON projects(project_key)
    WHERE project_key != '';

CREATE INDEX session_prompt_history_entries_session_sequence_idx
    ON session_prompt_history_entries(session_id, sequence);

CREATE UNIQUE INDEX session_prompt_history_entries_source_idx
    ON session_prompt_history_entries(session_id, source_id);

CREATE UNIQUE INDEX session_workflow_node_associations_branch_unique_idx
    ON session_workflow_node_associations(session_id, node_id, transition_branch_key)
    WHERE transition_branch_key IS NOT NULL;

CREATE UNIQUE INDEX session_workflow_node_associations_current_branch_unique_idx
    ON session_workflow_node_associations(task_id, node_id, transition_branch_key)
    WHERE association_status = 'current' AND transition_branch_key IS NOT NULL;

CREATE UNIQUE INDEX session_workflow_node_associations_current_serial_unique_idx
    ON session_workflow_node_associations(task_id, node_id)
    WHERE association_status = 'current' AND transition_branch_key IS NULL;

CREATE INDEX session_workflow_node_associations_history_lookup_idx
    ON session_workflow_node_associations(task_id, node_id, transition_branch_key)
    WHERE association_status = 'historical';

CREATE UNIQUE INDEX session_workflow_node_associations_serial_unique_idx
    ON session_workflow_node_associations(session_id, node_id)
    WHERE transition_branch_key IS NULL;

CREATE INDEX session_workflow_node_associations_session_recency_idx
    ON session_workflow_node_associations(session_id, associated_at_unix_ms DESC, node_id DESC);

CREATE UNIQUE INDEX sessions_artifact_relpath_idx ON sessions(artifact_relpath);

CREATE INDEX sessions_project_idx ON sessions(project_id, updated_at_unix_ms DESC);

CREATE INDEX sessions_task_activity_idx
    ON sessions(task_id, created_at_unix_ms DESC, CAST('session_started:' || id AS TEXT) DESC)
    WHERE task_id IS NOT NULL;

CREATE INDEX sessions_visible_category_recency_idx
ON sessions(project_id, COALESCE(category, 'main'), updated_at_unix_ms DESC, id DESC)
WHERE launch_visible <> 0;

CREATE INDEX sessions_workspace_idx ON sessions(workspace_id, updated_at_unix_ms DESC);

CREATE INDEX sessions_worktree_updated_idx
ON sessions(worktree_id, updated_at_unix_ms DESC)
WHERE worktree_id IS NOT NULL;

CREATE INDEX task_comments_task_activity_idx
    ON task_comments(task_id, updated_at_unix_ms DESC, CAST('comment:' || id AS TEXT) DESC);

CREATE INDEX task_comments_task_created_idx
    ON task_comments(task_id, created_at_unix_ms DESC, id DESC);

CREATE UNIQUE INDEX task_current_nodes_parallel_branch_unique_idx
    ON task_current_nodes(task_id, transition_branch_key)
    WHERE transition_branch_key IS NOT NULL;

CREATE UNIQUE INDEX task_current_nodes_serial_task_unique_idx
    ON task_current_nodes(task_id)
    WHERE transition_branch_key IS NULL;

CREATE INDEX task_dependencies_reverse_idx
    ON task_dependencies(blocked_task_id, blocker_task_id);

CREATE INDEX task_label_assignments_label_task_idx
    ON task_label_assignments(label_id, task_id);

CREATE UNIQUE INDEX task_pending_approvals_parallel_source_unique_idx
    ON task_pending_approvals(source_task_id, source_node_id, source_transition_branch_key)
    WHERE source_transition_branch_key IS NOT NULL;

CREATE UNIQUE INDEX task_pending_approvals_serial_source_unique_idx
    ON task_pending_approvals(source_task_id, source_node_id)
    WHERE source_transition_branch_key IS NULL;

CREATE UNIQUE INDEX task_search_documents_comment_unique
    ON task_search_documents(comment_id)
    WHERE source_kind = 'comment';

CREATE UNIQUE INDEX task_search_documents_task_body_unique
    ON task_search_documents(task_id)
    WHERE source_kind = 'body';

CREATE UNIQUE INDEX task_search_documents_task_short_id_unique
    ON task_search_documents(task_id)
    WHERE source_kind = 'short_id';

CREATE UNIQUE INDEX task_search_documents_task_title_unique
    ON task_search_documents(task_id)
    WHERE source_kind = 'title';

CREATE INDEX tasks_managed_worktree_idx
    ON tasks(managed_worktree_id);

CREATE INDEX tasks_project_workflow_link_idx
    ON tasks(project_workflow_link_id);

CREATE INDEX tasks_project_workflow_link_updated_idx
    ON tasks(project_workflow_link_id, updated_at_unix_ms DESC, id DESC);

CREATE INDEX tasks_short_id_idx
    ON tasks(short_id);

CREATE INDEX tasks_source_workspace_idx
    ON tasks(source_workspace_id);

CREATE INDEX workflow_edges_target_node_idx
    ON workflow_edges(target_node_id);

CREATE INDEX workflow_edges_transition_group_sort_idx
    ON workflow_edges(transition_group_id, sort_order);

CREATE INDEX workflow_node_groups_workflow_sort_idx
    ON workflow_node_groups(workflow_id, sort_order);

CREATE UNIQUE INDEX workflow_nodes_one_start_idx
    ON workflow_nodes(workflow_id)
    WHERE kind = 'start';

CREATE INDEX workflow_nodes_workflow_sort_idx
    ON workflow_nodes(workflow_id, sort_order);

CREATE UNIQUE INDEX workspaces_project_canonical_root_idx ON workspaces(project_id, canonical_root_path);

CREATE INDEX worktrees_workspace_idx ON worktrees(workspace_id);

CREATE TRIGGER project_labels_assignment_project_update
BEFORE UPDATE OF project_id ON project_labels
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_label_assignments tla
    JOIN tasks t ON t.id = tla.task_id
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    WHERE tla.label_id = OLD.id
      AND pwl.project_id != NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'assigned label must stay within the task project');
END;

CREATE TRIGGER project_workflow_links_default_delete
AFTER DELETE ON project_workflow_links
FOR EACH ROW
BEGIN
    UPDATE projects
    SET default_project_workflow_link_id = NULL
    WHERE id = OLD.project_id
      AND default_project_workflow_link_id = OLD.id;
END;

CREATE TRIGGER projects_default_workflow_link_insert
BEFORE INSERT ON projects
FOR EACH ROW
WHEN NEW.default_project_workflow_link_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM project_workflow_links pwl
    WHERE pwl.id = NEW.default_project_workflow_link_id
      AND pwl.project_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'default workflow link must belong to project');
END;

CREATE TRIGGER projects_default_workflow_link_update
BEFORE UPDATE OF id, default_project_workflow_link_id ON projects
FOR EACH ROW
WHEN NEW.default_project_workflow_link_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM project_workflow_links pwl
    WHERE pwl.id = NEW.default_project_workflow_link_id
      AND pwl.project_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'default workflow link must belong to project');
END;

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

CREATE TRIGGER session_workflow_node_associations_owner_insert
BEFORE INSERT ON session_workflow_node_associations
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM sessions retained_session
    JOIN task_records task ON task.id = NEW.task_id
    JOIN workflow_nodes node ON node.id = NEW.node_id
    WHERE retained_session.id = NEW.session_id
      AND retained_session.task_id = NEW.task_id
      AND node.workflow_id = task.workflow_id
)
OR (
    NEW.source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'session node association must belong to one task workflow');
END;

CREATE TRIGGER session_workflow_node_associations_owner_update
BEFORE UPDATE OF task_id, session_id, node_id, source_session_id ON session_workflow_node_associations
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM sessions retained_session
    JOIN task_records task ON task.id = NEW.task_id
    JOIN workflow_nodes node ON node.id = NEW.node_id
    WHERE retained_session.id = NEW.session_id
      AND retained_session.task_id = NEW.task_id
      AND node.workflow_id = task.workflow_id
)
OR (
    NEW.source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'session node association must belong to one task workflow');
END;

CREATE TRIGGER sessions_task_owner_clear_associations
AFTER UPDATE OF task_id ON sessions
FOR EACH ROW
WHEN NEW.task_id IS NULL
BEGIN
    DELETE FROM session_workflow_node_associations
    WHERE session_id = NEW.id
       OR source_session_id = NEW.id;
END;

CREATE TRIGGER sessions_task_owner_insert
BEFORE INSERT ON sessions
FOR EACH ROW
WHEN NEW.task_id IS NOT NULL
AND NOT EXISTS (
    SELECT 1
    FROM task_records task
    WHERE task.id = NEW.task_id
      AND task.project_id = NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'session task owner must belong to session project');
END;

CREATE TRIGGER sessions_task_owner_update
BEFORE UPDATE OF task_id, project_id ON sessions
FOR EACH ROW
WHEN NEW.task_id IS NOT NULL
AND NOT EXISTS (
    SELECT 1
    FROM task_records task
    WHERE task.id = NEW.task_id
      AND task.project_id = NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'session task owner must belong to session project');
END;

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

CREATE TRIGGER task_active_fanout_branches_continuation_source_insert
BEFORE INSERT ON task_active_fanout_branches
FOR EACH ROW
WHEN NOT (
    (
        NEW.legacy_materialized = 1
        AND NEW.continuation_source_kind IS NULL
        AND NEW.continuation_source_session_id IS NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND NEW.continuation_source_kind = 'exact'
        AND NEW.continuation_source_session_id IS NOT NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND NEW.continuation_source_kind IN ('deferred_self', 'absent')
        AND NEW.continuation_source_session_id IS NULL
    )
)
OR (
    NEW.continuation_source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.continuation_source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'Fan-Out branch continuation source is invalid');
END;

CREATE TRIGGER task_active_fanout_branches_continuation_source_update
BEFORE UPDATE OF task_id, continuation_source_kind, continuation_source_session_id, legacy_materialized ON task_active_fanout_branches
FOR EACH ROW
WHEN NOT (
    (
        NEW.legacy_materialized = 1
        AND NEW.continuation_source_kind IS NULL
        AND NEW.continuation_source_session_id IS NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND NEW.continuation_source_kind = 'exact'
        AND NEW.continuation_source_session_id IS NOT NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND NEW.continuation_source_kind IN ('deferred_self', 'absent')
        AND NEW.continuation_source_session_id IS NULL
    )
)
OR (
    NEW.continuation_source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.continuation_source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'Fan-Out branch continuation source is invalid');
END;

CREATE TRIGGER task_active_fanouts_serial_current_node_insert
BEFORE INSERT ON task_active_fanouts
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_current_nodes current_node
    WHERE current_node.task_id = NEW.task_id
      AND current_node.transition_branch_key IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'active fan-out cannot coexist with serial current node');
END;

CREATE TRIGGER task_active_fanouts_serial_current_node_update
BEFORE UPDATE OF task_id ON task_active_fanouts
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_current_nodes current_node
    WHERE current_node.task_id = NEW.task_id
      AND current_node.transition_branch_key IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'active fan-out cannot coexist with serial current node');
END;

CREATE TRIGGER task_current_nodes_continuation_source_insert
BEFORE INSERT ON task_current_nodes
FOR EACH ROW
WHEN NOT (
    (
        NEW.legacy_materialized = 1
        AND NEW.continuation_source_kind IS NULL
        AND NEW.continuation_source_session_id IS NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND (
            (
                NEW.continuation_source_kind = 'exact'
                AND NEW.continuation_source_session_id IS NOT NULL
            )
            OR (
                NEW.continuation_source_kind IN ('deferred_self', 'absent')
                AND NEW.continuation_source_session_id IS NULL
            )
        )
    )
)
OR (
    NEW.continuation_source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.continuation_source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'current node continuation source is invalid');
END;

CREATE TRIGGER task_current_nodes_continuation_source_update
BEFORE UPDATE OF task_id, continuation_source_kind, continuation_source_session_id, legacy_materialized ON task_current_nodes
FOR EACH ROW
WHEN NOT (
    (
        NEW.legacy_materialized = 1
        AND NEW.continuation_source_kind IS NULL
        AND NEW.continuation_source_session_id IS NULL
    )
    OR (
        NEW.legacy_materialized = 0
        AND (
            (
                NEW.continuation_source_kind = 'exact'
                AND NEW.continuation_source_session_id IS NOT NULL
            )
            OR (
                NEW.continuation_source_kind IN ('deferred_self', 'absent')
                AND NEW.continuation_source_session_id IS NULL
            )
        )
    )
)
OR (
    NEW.continuation_source_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions source_session
        WHERE source_session.id = NEW.continuation_source_session_id
          AND source_session.task_id = NEW.task_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'current node continuation source is invalid');
END;

CREATE TRIGGER task_current_nodes_pending_approval_delete
BEFORE DELETE ON task_current_nodes
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_pending_approvals approval
    WHERE approval.source_task_id = OLD.task_id
      AND approval.source_node_id = OLD.node_id
      AND (
          (
              approval.source_transition_branch_key IS NULL
              AND OLD.transition_branch_key IS NULL
          )
          OR approval.source_transition_branch_key = OLD.transition_branch_key
      )
)
BEGIN
    SELECT RAISE(ABORT, 'current node with pending approval cannot be deleted');
END;

CREATE TRIGGER task_current_nodes_pending_approval_reference_update
BEFORE UPDATE OF task_id, node_id, transition_branch_key ON task_current_nodes
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_pending_approvals approval
    WHERE approval.source_task_id = OLD.task_id
      AND approval.source_node_id = OLD.node_id
      AND (
          (
              approval.source_transition_branch_key IS NULL
              AND OLD.transition_branch_key IS NULL
          )
          OR approval.source_transition_branch_key = OLD.transition_branch_key
      )
)
BEGIN
    SELECT RAISE(ABORT, 'current node with pending approval cannot change identity');
END;

CREATE TRIGGER task_current_nodes_prior_transition_parameters_insert
BEFORE INSERT ON task_current_nodes
FOR EACH ROW
WHEN json_type(NEW.prior_node_values_json, '$.transition_parameters') IS NOT 'object'
  OR json(json_remove(NEW.prior_node_values_json, '$.transition_parameters')) != '{}'
BEGIN
    SELECT RAISE(ABORT, 'current node prior values must contain exactly one Transition parameter object');
END;

CREATE TRIGGER task_current_nodes_prior_transition_parameters_update
BEFORE UPDATE OF prior_node_values_json ON task_current_nodes
FOR EACH ROW
WHEN json_type(NEW.prior_node_values_json, '$.transition_parameters') IS NOT 'object'
  OR json(json_remove(NEW.prior_node_values_json, '$.transition_parameters')) != '{}'
BEGIN
    SELECT RAISE(ABORT, 'current node prior values must contain exactly one Transition parameter object');
END;

CREATE TRIGGER task_current_nodes_serial_active_fanout_insert
BEFORE INSERT ON task_current_nodes
FOR EACH ROW
WHEN NEW.transition_branch_key IS NULL
AND EXISTS (
    SELECT 1
    FROM task_active_fanouts fanout
    WHERE fanout.task_id = NEW.task_id
)
BEGIN
    SELECT RAISE(ABORT, 'serial current node cannot coexist with active fan-out');
END;

CREATE TRIGGER task_current_nodes_serial_active_fanout_update
BEFORE UPDATE OF task_id, transition_branch_key ON task_current_nodes
FOR EACH ROW
WHEN NEW.transition_branch_key IS NULL
AND EXISTS (
    SELECT 1
    FROM task_active_fanouts fanout
    WHERE fanout.task_id = NEW.task_id
)
BEGIN
    SELECT RAISE(ABORT, 'serial current node cannot coexist with active fan-out');
END;

CREATE TRIGGER task_current_nodes_task_workflow_insert
BEFORE INSERT ON task_current_nodes
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_records task
    JOIN workflow_nodes node ON node.id = NEW.node_id
    WHERE task.id = NEW.task_id
      AND node.workflow_id = task.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'current node must belong to task workflow');
END;

CREATE TRIGGER task_current_nodes_task_workflow_update
BEFORE UPDATE OF task_id, node_id ON task_current_nodes
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_records task
    JOIN workflow_nodes node ON node.id = NEW.node_id
    WHERE task.id = NEW.task_id
      AND node.workflow_id = task.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'current node must belong to task workflow');
END;

CREATE TRIGGER task_label_assignments_project_insert
BEFORE INSERT ON task_label_assignments
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM tasks t
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    JOIN project_labels pl ON pl.id = NEW.label_id
    WHERE t.id = NEW.task_id
      AND pwl.project_id = pl.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'task label assignment must stay within one project');
END;

CREATE TRIGGER task_label_assignments_project_update
BEFORE UPDATE OF task_id, label_id ON task_label_assignments
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM tasks t
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    JOIN project_labels pl ON pl.id = NEW.label_id
    WHERE t.id = NEW.task_id
      AND pwl.project_id = pl.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'task label assignment must stay within one project');
END;

CREATE TRIGGER task_pending_approval_branches_prior_transition_parameters_insert
BEFORE INSERT ON task_pending_approval_branches
FOR EACH ROW
WHEN json_type(NEW.target_snapshot_json, '$.prior_values') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_values.transition_parameters') IS NOT 'object'
  OR json(
      json_remove(
          json_extract(NEW.target_snapshot_json, '$.prior_values'),
          '$.transition_parameters'
      )
  ) != '{}'
  OR json_type(NEW.target_snapshot_json, '$.prior_node_values') IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'pending approval target prior values must contain exactly one Transition parameter object');
END;

CREATE TRIGGER task_pending_approval_branches_prior_transition_parameters_update
BEFORE UPDATE OF target_snapshot_json ON task_pending_approval_branches
FOR EACH ROW
WHEN json_type(NEW.target_snapshot_json, '$.prior_values') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_values.transition_parameters') IS NOT 'object'
  OR json(
      json_remove(
          json_extract(NEW.target_snapshot_json, '$.prior_values'),
          '$.transition_parameters'
      )
  ) != '{}'
  OR json_type(NEW.target_snapshot_json, '$.prior_node_values') IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'pending approval target prior values must contain exactly one Transition parameter object');
END;

CREATE TRIGGER task_pending_approvals_source_current_insert
BEFORE INSERT ON task_pending_approvals
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_current_nodes current_node
    WHERE current_node.task_id = NEW.source_task_id
      AND current_node.node_id = NEW.source_node_id
      AND (
          (
              current_node.transition_branch_key IS NULL
              AND NEW.source_transition_branch_key IS NULL
          )
          OR current_node.transition_branch_key = NEW.source_transition_branch_key
      )
)
BEGIN
    SELECT RAISE(ABORT, 'pending approval source must be a current node');
END;

CREATE TRIGGER task_pending_approvals_source_current_update
BEFORE UPDATE OF source_task_id, source_node_id, source_transition_branch_key ON task_pending_approvals
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM task_current_nodes current_node
    WHERE current_node.task_id = NEW.source_task_id
      AND current_node.node_id = NEW.source_node_id
      AND (
          (
              current_node.transition_branch_key IS NULL
              AND NEW.source_transition_branch_key IS NULL
          )
          OR current_node.transition_branch_key = NEW.source_transition_branch_key
      )
)
BEGIN
    SELECT RAISE(ABORT, 'pending approval source must be a current node');
END;

CREATE TRIGGER task_search_comment_body_after_update
AFTER UPDATE OF body ON task_comments
BEGIN
    INSERT INTO task_search_fts(rowid, title, body, comment)
    SELECT document_id, NULL, NULL, NEW.body
    FROM task_search_documents
    WHERE comment_id = NEW.id;
END;

CREATE TRIGGER task_search_comment_body_before_update
BEFORE UPDATE OF body ON task_comments
BEGIN
    INSERT INTO task_search_fts(task_search_fts, rowid, title, body, comment)
    SELECT 'delete', document_id, NULL, NULL, OLD.body
    FROM task_search_documents
    WHERE comment_id = OLD.id;
END;

CREATE TRIGGER task_search_comment_delete
BEFORE DELETE ON task_comments
BEGIN
    DELETE FROM task_search_documents
    WHERE comment_id = OLD.id;
END;

CREATE TRIGGER task_search_comment_insert
AFTER INSERT ON task_comments
BEGIN
    INSERT INTO task_search_documents (source_kind, comment_id)
    VALUES ('comment', NEW.id);
END;

CREATE TRIGGER task_search_short_id_document_delete
BEFORE DELETE ON task_search_documents
WHEN OLD.source_kind = 'short_id'
BEGIN
    INSERT INTO task_search_short_id_fts(task_search_short_id_fts, rowid, short_id)
    SELECT 'delete', document_id, short_id
    FROM task_search_content
    WHERE document_id = OLD.document_id;
END;

CREATE TRIGGER task_search_short_id_document_insert
AFTER INSERT ON task_search_documents
WHEN NEW.source_kind = 'short_id'
BEGIN
    INSERT INTO task_search_short_id_fts(rowid, short_id)
    SELECT document_id, short_id
    FROM task_search_content
    WHERE document_id = NEW.document_id;
END;

CREATE TRIGGER task_search_task_body_after_update
AFTER UPDATE OF body ON tasks
BEGIN
    INSERT INTO task_search_fts(rowid, title, body, comment)
    SELECT document_id, NULL, NEW.body, NULL
    FROM task_search_documents
    WHERE task_id = NEW.id
      AND source_kind = 'body';
END;

CREATE TRIGGER task_search_task_body_before_update
BEFORE UPDATE OF body ON tasks
BEGIN
    INSERT INTO task_search_fts(task_search_fts, rowid, title, body, comment)
    SELECT 'delete', document_id, NULL, OLD.body, NULL
    FROM task_search_documents
    WHERE task_id = OLD.id
      AND source_kind = 'body';
END;

CREATE TRIGGER task_search_task_delete
BEFORE DELETE ON tasks
BEGIN
    DELETE FROM task_search_documents
    WHERE task_id = OLD.id
       OR comment_id IN (
            SELECT id
            FROM task_comments
            WHERE task_id = OLD.id
       );
END;

CREATE TRIGGER task_search_task_insert
AFTER INSERT ON tasks
BEGIN
    INSERT INTO task_search_documents (source_kind, task_id)
    VALUES ('short_id', NEW.id), ('title', NEW.id), ('body', NEW.id);
END;

CREATE TRIGGER task_search_task_title_after_update
AFTER UPDATE OF title ON tasks
BEGIN
    INSERT INTO task_search_fts(rowid, title, body, comment)
    SELECT document_id, NEW.title, NULL, NULL
    FROM task_search_documents
    WHERE task_id = NEW.id
      AND source_kind = 'title';
END;

CREATE TRIGGER task_search_task_title_before_update
BEFORE UPDATE OF title ON tasks
BEGIN
    INSERT INTO task_search_fts(task_search_fts, rowid, title, body, comment)
    SELECT 'delete', document_id, OLD.title, NULL, NULL
    FROM task_search_documents
    WHERE task_id = OLD.id
      AND source_kind = 'title';
END;

CREATE TRIGGER task_search_text_document_delete
BEFORE DELETE ON task_search_documents
WHEN OLD.source_kind != 'short_id'
BEGIN
    INSERT INTO task_search_fts(task_search_fts, rowid, title, body, comment)
    SELECT 'delete', document_id, title, body, comment
    FROM task_search_content
    WHERE document_id = OLD.document_id;
END;

CREATE TRIGGER task_search_text_document_insert
AFTER INSERT ON task_search_documents
WHEN NEW.source_kind != 'short_id'
BEGIN
    INSERT INTO task_search_fts(rowid, title, body, comment)
    SELECT document_id, title, body, comment
    FROM task_search_content
    WHERE document_id = NEW.document_id;
END;

CREATE TRIGGER tasks_label_assignment_project_update
BEFORE UPDATE OF project_workflow_link_id ON tasks
FOR EACH ROW
WHEN EXISTS (
    SELECT 1
    FROM task_label_assignments tla
    JOIN project_labels pl ON pl.id = tla.label_id
    JOIN project_workflow_links pwl ON pwl.id = NEW.project_workflow_link_id
    WHERE tla.task_id = OLD.id
      AND pl.project_id != pwl.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'task labels must stay within the task project');
END;

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

CREATE TRIGGER workflow_edges_target_workflow_insert
BEFORE INSERT ON workflow_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM workflow_transition_groups transition_group
    JOIN workflow_nodes source ON source.id = transition_group.source_node_id
    JOIN workflow_nodes target ON target.id = NEW.target_node_id
    WHERE transition_group.id = NEW.transition_group_id
      AND target.workflow_id = source.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow edge target node must belong to transition group workflow');
END;

CREATE TRIGGER workflow_edges_target_workflow_update
BEFORE UPDATE OF transition_group_id, target_node_id ON workflow_edges
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM workflow_transition_groups transition_group
    JOIN workflow_nodes source ON source.id = transition_group.source_node_id
    JOIN workflow_nodes target ON target.id = NEW.target_node_id
    WHERE transition_group.id = NEW.transition_group_id
      AND target.workflow_id = source.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow edge target node must belong to transition group workflow');
END;

CREATE TRIGGER workflow_nodes_current_task_anchor_delete
BEFORE DELETE ON workflow_nodes
FOR EACH ROW
WHEN EXISTS (
    SELECT 1 FROM task_pending_approvals approval
    WHERE approval.source_node_id = OLD.id
) OR EXISTS (
    SELECT 1 FROM task_pending_approval_branches branch
    WHERE kent_graph_entity_id_blob_v1(
        json_extract(branch.target_snapshot_json, '$.node_id')
    ) = OLD.id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node has current task references');
END;

CREATE TRIGGER workflow_nodes_group_workflow_insert
BEFORE INSERT ON workflow_nodes
FOR EACH ROW
WHEN NEW.group_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM workflow_node_groups node_group
    WHERE node_group.id = NEW.group_id
      AND node_group.workflow_id = NEW.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow_nodes.group_id must belong to node workflow');
END;

CREATE TRIGGER workflow_nodes_group_workflow_update
BEFORE UPDATE OF workflow_id, group_id ON workflow_nodes
FOR EACH ROW
WHEN NEW.group_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM workflow_node_groups node_group
    WHERE node_group.id = NEW.group_id
      AND node_group.workflow_id = NEW.workflow_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow_nodes.group_id must belong to node workflow');
END;

CREATE TRIGGER workflow_nodes_task_reference_kind_update
BEFORE UPDATE OF kind ON workflow_nodes
FOR EACH ROW
WHEN NEW.kind != OLD.kind
AND (
    EXISTS (
        SELECT 1 FROM task_current_nodes current_node
        WHERE current_node.node_id = OLD.id
    )
    OR EXISTS (
        SELECT 1 FROM task_pending_approvals approval
        WHERE approval.source_node_id = OLD.id
    )
    OR EXISTS (
        SELECT 1 FROM task_pending_approval_branches branch
        WHERE kent_graph_entity_id_blob_v1(
            json_extract(branch.target_snapshot_json, '$.node_id')
        ) = OLD.id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'workflow node kind changes are blocked for nodes referenced by current task state');
END;

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

CREATE TRIGGER workspaces_primary_workspace_delete
AFTER DELETE ON workspaces
FOR EACH ROW
BEGIN
    UPDATE projects
    SET primary_workspace_id = ''
    WHERE id = OLD.project_id
      AND primary_workspace_id = OLD.id;
END;

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

CREATE TRIGGER worktrees_creation_base_commit_oid_immutable
BEFORE UPDATE OF creation_base_commit_oid ON worktrees
FOR EACH ROW
WHEN OLD.creation_base_commit_oid IS NOT NEW.creation_base_commit_oid
BEGIN
    SELECT RAISE(ABORT, 'worktree creation base commit oid is immutable');
END;

CREATE TRIGGER worktrees_creation_base_commit_oid_insert_conflict
BEFORE INSERT ON worktrees
FOR EACH ROW
WHEN NEW.creation_base_commit_oid IS NOT NULL
 AND EXISTS (
    SELECT 1
    FROM worktrees existing
    WHERE existing.canonical_root_path = NEW.canonical_root_path
      AND existing.creation_base_commit_oid IS NOT NULL
      AND existing.creation_base_commit_oid != NEW.creation_base_commit_oid
 )
BEGIN
    SELECT RAISE(ABORT, 'worktree creation base commit oid is immutable');
END;

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
