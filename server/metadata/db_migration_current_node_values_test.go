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
    prompt_template, input_bindings_json, output_requirements_json
) VALUES
    ('edge-plan-review', 'group-done', 'review', 'node-review', 'new_session',
     'Review {{.Inputs.summary}}.',
     '[{"name":"summary","source":"transition_output","field":"summary"}]',
     '[]'),
    ('edge-review-audit', 'group-review-audit', 'audit', 'node-audit', 'new_session',
     'Audit {{.Nodes.plan.summary}}.', '[]', '[]'),
    ('edge-audit-done', 'group-audit-done', 'done', 'node-done', 'new_session',
     '', '[]', '[]')`)
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
    'applied',
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
    '{"node_output_values":{}}'
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
		priorNodeValues != `{"plan":{"summary":"approved plan"}}` ||
		enteredByEdgeID != "edge-plan-review" {
		t.Fatalf(
			"migrated value environment = inputs=%q prior=%q entered_by=%q",
			currentInputs,
			priorNodeValues,
			enteredByEdgeID,
		)
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
	execSeed(t, db, "prior-node requirement", `
UPDATE workflow_edges
SET prompt_template = 'Continue with {{.Nodes.plan.summary}}.'
WHERE id = 'edge-done-1'`)
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
	seedLegacyExecutableCurrentNodeEnteringEdge(t, db, "task-invalid-values", "placement-invalid-values", now)
	execSeed(t, db, "prior transition value", `
UPDATE task_transitions
SET source_node_key = 'plan',
    source_node_display_name = 'Plan',
    output_values_json = '{"summary":"approved plan"}'
WHERE id = 'entry-transition-placement-invalid-values'`)
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
