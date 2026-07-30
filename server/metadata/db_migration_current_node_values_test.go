package metadata

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenProjectsMigratesSequentialCurrentNodeValueEnvironment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-value-environment-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-value-environment-migration", now)
	execSeed(t, db, "value environment graph", `
UPDATE workflow_nodes
SET node_key = 'plan',
    display_name = 'Plan',
    prompt_template = 'Plan.',
    output_fields_json = '[{"name":"summary","description":"Plan summary."}]'
WHERE id = 'node-agent';

INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role,
    prompt_template, input_fields_json, output_fields_json
) VALUES
    ('node-review', 'workflow-1', 'review', 'agent', 'Review', 'coder',
     'Review.', '[{"name":"summary","description":"Plan summary."}]', '[]'),
    ('node-audit', 'workflow-1', 'audit', 'agent', 'Audit', 'coder',
     'Audit.', '[]', '[]');

DELETE FROM workflow_edges
WHERE id = 'edge-done-1';

UPDATE workflow_transition_groups
SET transition_id = 'review',
    display_name = 'Review'
WHERE id = 'group-done';

INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES
    ('group-review-audit', 'node-review', 'audit', 'Audit'),
    ('group-audit-done', 'node-audit', 'done', 'Done');

INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode,
    prompt_template, parameters_json, input_bindings_json, output_requirements_json
) VALUES
    ('edge-plan-review', 'group-done', 'review', 'node-review', 'new_session',
     'Review {{.Inputs.summary}}.',
     '[{"key":"summary","description":"Plan summary."}]',
     '[{"name":"summary","source":"transition_output","field":"summary"}]',
     '[]'),
    ('edge-review-audit', 'group-review-audit', 'audit', 'node-audit', 'new_session',
     'Audit {{.Nodes.plan.summary}} and {{.Params.review.summary}}.', '[]', '[]', '[]'),
    ('edge-audit-done', 'group-audit-done', 'done', 'node-done', 'new_session',
     '', '[]', '[]', '[]')`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-value-environment-migration", "link-1", 1, "VAL-1", now, now)
	execSeed(t, db, "plan and review placements", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES
    ('placement-value-plan', 'task-value-environment-migration', 'node-agent', 'completed', ?, ?),
    ('placement-value-review', 'task-value-environment-migration', 'node-review', 'active', ?, ?)`,
		now,
		now+1,
		now+2,
		now+3,
	)
	execSeed(t, db, "plan completion transition", `
INSERT INTO task_transitions (
    id, task_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (
    'transition-value-plan-review',
    'task-value-environment-migration',
    'placement-value-plan',
    'plan',
    'Plan',
    'review',
    'Review',
    1,
    'agent',
    'approved',
    'ready for review',
    '{"summary":"approved plan"}',
    ?,
    ?
)`, now+2, now+2)
	execSeed(t, db, "plan completion edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    target_placement_id, state, context_mode, requires_approval,
    input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-value-plan-review',
    'transition-value-plan-review',
    'edge-plan-review',
    'review',
    'node-review',
    'review',
    'Review',
    'agent',
    'placement-value-review',
    'applied',
    'new_session',
    0,
    '[{"name":"summary","source":"transition_output","field":"summary"}]',
    '[]',
    '{"node_output_values":{}}'
)`)
	execSeed(t, db, "review run", `
INSERT INTO task_runs (
    id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms,
    metadata_json
) VALUES (
    'run-value-review',
    'placement-value-review',
    1,
    ?,
    ?,
    '{"prior_parameter_values":{"review":{"summary":"approved plan"}}}'
)`, now+2, now+4)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var currentInputs, priorNodeValues, enteredByEdgeID string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT current_input_values_json, prior_node_values_json, entered_by_edge_id
FROM task_current_nodes
WHERE task_id = 'task-value-environment-migration'`).Scan(
		&currentInputs,
		&priorNodeValues,
		&enteredByEdgeID,
	); err != nil {
		t.Fatalf("query migrated value environment: %v", err)
	}
	if currentInputs != `{"summary":"approved plan"}` ||
		priorNodeValues != `{"plan":{"summary":"approved plan"},"review":{"summary":"approved plan"}}` ||
		enteredByEdgeID != "edge-plan-review" {
		t.Fatalf(
			"migrated value environment = inputs=%q prior=%q entered_by=%q",
			currentInputs,
			priorNodeValues,
			enteredByEdgeID,
		)
	}
}

func TestOpenProjectsMigratesPendingApprovalFrozenTargetPriorValues(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-frozen-prior-parameter-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-frozen-prior-parameter-migration", now)
	execSeed(t, db, "frozen prior parameter graph", `
UPDATE workflow_nodes
SET node_key = 'plan',
    display_name = 'Plan',
    prompt_template = 'Plan.',
    output_fields_json = '[{"name":"summary","description":"Plan summary."}]'
WHERE id = 'node-agent';

INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role,
    prompt_template, input_fields_json, output_fields_json
) VALUES
    ('node-review', 'workflow-1', 'review', 'agent', 'Review', 'coder',
     'Review.', '[]', '[]'),
    ('node-audit', 'workflow-1', 'audit', 'agent', 'Audit', 'coder',
     'Audit.', '[]', '[]');

UPDATE workflow_transition_groups
SET transition_id = 'review',
    display_name = 'Review'
WHERE id = 'group-done';

UPDATE workflow_edges
SET edge_key = 'review',
    target_node_id = 'node-review',
    prompt_template = 'Review {{.Params.summary}}.',
    parameters_json = '[{"key":"summary","description":"Plan summary."}]'
WHERE id = 'edge-done-1';

INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES
    ('group-review-audit', 'node-review', 'audit', 'Audit'),
    ('group-audit-done', 'node-audit', 'done', 'Done');

INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, requires_approval,
    context_mode, prompt_template, parameters_json, input_bindings_json, output_requirements_json
) VALUES
    ('edge-review-audit', 'group-review-audit', 'audit', 'node-audit', 1,
     'new_session', 'Mutable prompt without historical references.', '[]', '[]', '[]'),
    ('edge-audit-done', 'group-audit-done', 'done', 'node-done', 0,
     'new_session', 'Ship {{.Params.audit.result}}.', '[]', '[]', '[]')`)
	execSeed(t, db, "task", workflowSeedTaskSQL,
		"task-frozen-prior-parameter-migration",
		"link-1",
		1,
		"APR-VALUES-1",
		now,
		now,
	)
	execSeed(t, db, "plan and review placements", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES
    ('placement-frozen-plan', 'task-frozen-prior-parameter-migration', 'node-agent', 'completed', ?, ?),
    ('placement-frozen-review', 'task-frozen-prior-parameter-migration', 'node-review', 'waiting_approval', ?, ?)`,
		now,
		now+1,
		now+3,
		now+4,
	)
	execSeed(t, db, "applied review transition", `
INSERT INTO task_transitions (
    id, task_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (
    'transition-frozen-plan-review',
    'task-frozen-prior-parameter-migration',
    'placement-frozen-plan',
    'historical_plan',
    'Historical Plan',
    'review',
    'Review',
    1,
    'agent',
    'applied',
    '',
    '{"summary":"approved plan"}',
    ?,
    ?
)`, now+2, now+2)
	execSeed(t, db, "applied review transition edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    target_placement_id, state, context_mode, requires_approval,
    input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-frozen-plan-review',
    'transition-frozen-plan-review',
    'edge-done-1',
    'review',
    'node-review',
    'review',
    'Review',
    'agent',
    'placement-frozen-review',
    'applied',
    'new_session',
    0,
    '[]',
    '[]',
    '{"node_output_values":{}}'
)`)
	execSeed(t, db, "pending audit transition", `
INSERT INTO task_transitions (
    id, task_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms
) VALUES (
    'transition-frozen-review-audit',
    'task-frozen-prior-parameter-migration',
    'placement-frozen-review',
    'review',
    'Review',
    'audit',
    'Audit',
    1,
    'agent',
    'pending_approval',
    '',
    '{"result":"approved review"}',
    ?
)`, now+4)
	execSeed(t, db, "pending audit edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-frozen-review-audit',
    'transition-frozen-review-audit',
    'edge-review-audit',
    'audit',
    'node-audit',
    'audit',
    'Audit',
    'agent',
    'pending',
    'new_session',
    1,
    '[]',
    '[]',
    '{"prompt_template":"Finalize {{.Params.review.summary}}.","parameters":[{"key":"result","description":"Approved review result."}],"prior_parameter_values":{"review":{"summary":"approved plan"}}}'
)`)
	execSeed(t, db, "delete mutable approval graph", `
DELETE FROM workflow_transition_groups
WHERE id = 'group-review-audit'`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var targetPriorValues string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT json_extract(branch.target_snapshot_json, '$.prior_node_values')
FROM task_pending_approval_branches branch
JOIN task_pending_approvals approval ON approval.id = branch.approval_id
WHERE approval.source_task_id = 'task-frozen-prior-parameter-migration'`).Scan(&targetPriorValues); err != nil {
		t.Fatalf("query migrated pending approval target values: %v", err)
	}
	if targetPriorValues != `{"audit":{"result":"approved review"},"review":{"summary":"approved plan"}}` {
		t.Fatalf("migrated pending approval target prior values = %q, want frozen review and pending audit transition values", targetPriorValues)
	}
}

func TestOpenRejectsInvalidCurrentNodeValueEnvironmentsWithContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, int64)
		want   []string
	}{
		{
			name: "missing current input",
			mutate: func(t *testing.T, db *sql.DB, _ int64) {
				execSeed(t, db, "missing current input", `
UPDATE task_transition_edges
SET input_bindings_json = '[{"name":"required_input","source":"transition_output","field":"missing"}]'
WHERE id = 'entry-edge-placement-invalid-values'`)
			},
			want: []string{"task-invalid-values", "node-agent", "transition_branch_key=serial", "value_key=required_input"},
		},
		{
			name: "missing prior node value",
			mutate: func(t *testing.T, db *sql.DB, _ int64) {
				execSeed(t, db, "missing prior node value", `
UPDATE task_transitions
SET output_values_json = '{}'
WHERE id = 'entry-transition-placement-invalid-values'`)
			},
			want: []string{"task-invalid-values", "node-agent", "transition_branch_key=serial", "value_key=plan.summary", "required value is missing"},
		},
		{
			name: "malformed frozen prior node values",
			mutate: func(t *testing.T, db *sql.DB, _ int64) {
				execSeed(t, db, "malformed frozen prior node values", `
UPDATE task_runs
SET metadata_json = '{"node_output_values":[]}'
WHERE id = 'run-invalid-values'`)
			},
			want: []string{"task-invalid-values", "node-agent", "transition_branch_key=serial", "decode frozen values"},
		},
		{
			name: "conflicting frozen prior node value",
			mutate: func(t *testing.T, db *sql.DB, _ int64) {
				execSeed(t, db, "conflicting frozen prior node value", `
UPDATE task_runs
SET metadata_json = '{"node_output_values":{"plan":{"summary":"frozen conflict"}}}'
WHERE id = 'run-invalid-values'`)
			},
			want: []string{"task-invalid-values", "node-agent", "transition_branch_key=serial", "value_key=plan.summary", "frozen value conflicts"},
		},
		{
			name: "prior node ordering tie",
			mutate: func(t *testing.T, db *sql.DB, now int64) {
				execSeed(t, db, "tied prior node transition", `
INSERT INTO task_transitions (
    id, task_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (
    'transition-invalid-values-tie',
    'task-invalid-values',
    'plan',
    'Plan',
    'review',
    'Review',
    1,
    'agent',
    'applied',
    '',
    '{"summary":"same timestamp"}',
    ?,
    ?
)`, now, now)
			},
			want: []string{"task-invalid-values", "node-agent", "transition_branch_key=serial", "value_key=plan.summary", "ordering tie"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "db", "main.sqlite3")
			db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
			if err != nil {
				t.Fatalf("open version 58 db: %v", err)
			}
			now := time.Now().UTC().UnixMilli()
			seedLegacyInvalidValueEnvironmentFixture(t, db, now)
			test.mutate(t, db, now)
			if err := db.Close(); err != nil {
				t.Fatalf("close version 58 db: %v", err)
			}

			_, err = Open(root)
			if err == nil {
				t.Fatal("Open unexpectedly accepted an invalid Current Node value environment")
			}
			for _, expected := range test.want {
				if !strings.Contains(err.Error(), expected) {
					t.Fatalf("Open error = %v, want context %q", err, expected)
				}
			}
		})
	}
}

func seedLegacyInvalidValueEnvironmentFixture(t *testing.T, db *sql.DB, now int64) {
	t.Helper()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-invalid-values', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-invalid-values", now)
	execSeed(t, db, "prior-node requirement graph", `
UPDATE workflow_nodes
SET node_key = 'review',
    display_name = 'Review',
    prompt_template = 'Review.'
WHERE id = 'node-agent';

INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role,
    prompt_template, input_fields_json, output_fields_json
) VALUES (
    'node-plan-invalid-values',
    'workflow-1',
    'plan',
    'agent',
    'Plan',
    'coder',
    'Plan.',
    '[]',
    '[{"name":"summary","description":"Plan summary."}]'
);

UPDATE workflow_edges
SET target_node_id = 'node-plan-invalid-values',
    prompt_template = 'Plan.'
WHERE id = 'edge-start-1';

UPDATE workflow_transition_groups
SET source_node_id = 'node-plan-invalid-values',
    transition_id = 'review',
    display_name = 'Review'
WHERE id = 'group-done';

UPDATE workflow_edges
SET edge_key = 'review',
    target_node_id = 'node-agent',
    prompt_template = 'Review {{.Params.summary}}.',
    parameters_json = '[{"key":"summary","description":"Plan summary."}]'
WHERE id = 'edge-done-1';

INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES ('group-review-done-invalid-values', 'node-agent', 'done', 'Done');

INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode,
    prompt_template, parameters_json, input_bindings_json, output_requirements_json
) VALUES (
    'edge-review-done-invalid-values',
    'group-review-done-invalid-values',
    'done',
    'node-done',
    'new_session',
    'Continue with {{.Nodes.plan.summary}}.',
    '[]',
    '[]',
    '[]'
)`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-invalid-values", "link-1", 1, "BAD-1", now, now)
	execSeed(t, db, "current placement", workflowSeedPlacementSQL, "placement-invalid-values", "task-invalid-values", "node-agent", now, now)
	execSeed(t, db, "current run", `
INSERT INTO task_runs (
    id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms,
    metadata_json
) VALUES (
    'run-invalid-values',
    'placement-invalid-values',
    1,
    ?,
    ?,
    '{"node_output_values":{}}'
)`, now, now+1)
	execSeed(t, db, "legacy plan placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'entry-source-placement-invalid-values',
    'task-invalid-values',
    'node-plan-invalid-values',
    'completed',
    ?,
    ?
)`, now, now)
	execSeed(t, db, "legacy review transition", `
INSERT INTO task_transitions (
    id, task_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (
    'entry-transition-placement-invalid-values',
    'task-invalid-values',
    'entry-source-placement-invalid-values',
    'plan',
    'Plan',
    'review',
    'Review',
    1,
    'agent',
    'applied',
    '',
    '{"summary":"approved plan"}',
    ?,
    ?
)`, now, now)
	execSeed(t, db, "legacy review transition edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    target_placement_id, state, context_mode, requires_approval,
    input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'entry-edge-placement-invalid-values',
    'entry-transition-placement-invalid-values',
    'edge-done-1',
    'review',
    'node-agent',
    'review',
    'Review',
    'agent',
    'placement-invalid-values',
    'applied',
    'new_session',
    0,
    '[]',
    '[]',
    '{}'
)`)
}

func TestOpenProjectsLegacyRunnableAgentPlacementToInterruptedCurrentNode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	placementUpdatedAt := now + 7
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-runnable-agent-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-runnable-agent-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-runnable-agent-migration", "link-1", 1, "RUN-1", now, now)
	execSeed(
		t,
		db,
		"runnable agent placement",
		workflowSeedPlacementSQL,
		"placement-runnable-agent-migration",
		"task-runnable-agent-migration",
		"node-agent",
		now,
		placementUpdatedAt,
	)
	seedLegacyExecutableCurrentNodeEnteringEdge(t, db, "task-runnable-agent-migration", "placement-runnable-agent-migration", now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID, schedulingState, interruptionReason, interruptionDetail string
	var interruptedAt int64
	var sessionID sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    node_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms,
    session_id
FROM task_current_nodes
WHERE task_id = 'task-runnable-agent-migration'`).Scan(
		&nodeID,
		&schedulingState,
		&interruptionReason,
		&interruptionDetail,
		&interruptedAt,
		&sessionID,
	); err != nil {
		t.Fatalf("query projected runnable agent current node: %v", err)
	}
	if nodeID != "node-agent" ||
		schedulingState != "interrupted" ||
		interruptionReason != "server_restart" ||
		interruptionDetail != `{"code":"workflow.execution.restarted","fields":{"operation":"recovery"}}` ||
		interruptedAt != placementUpdatedAt ||
		sessionID.Valid {
		t.Fatalf(
			"projected runnable agent current node = node=%q scheduling=%q reason=%q detail=%q interrupted_at=%d session=%+v",
			nodeID,
			schedulingState,
			interruptionReason,
			interruptionDetail,
			interruptedAt,
			sessionID,
		)
	}
}
