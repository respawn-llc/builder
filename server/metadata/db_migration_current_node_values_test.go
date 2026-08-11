package metadata

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/toolspec"

	_ "modernc.org/sqlite"
)

const (
	historicalVersion58TransitionPrompt = `Review {{.Inputs.summary}}.`
	version71TransitionPrompt           = `Execute {{.TaskTitle}}.`
	version71TransitionParameters       = `[{"key":"summary","description":"Transition summary."}]`
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
    ('node-review', 'workflow-550e8400-e29b-41d4-a716-446655440001', 'review', 'agent', 'Review', 'coder',
     'Review.', '[{"name":"summary","description":"Plan summary."}]', '[]'),
    ('node-audit', 'workflow-550e8400-e29b-41d4-a716-446655440001', 'audit', 'agent', 'Audit', 'coder',
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
     ?,
     '[{"key":"summary","description":"Plan summary."}]',
     '[{"name":"summary","source":"transition_output","field":"summary"}]',
     '[]'),
    ('edge-review-audit', 'group-review-audit', 'audit', 'node-audit', 'new_session',
     'Audit {{.Params.review.summary}}.', '[]', '[]', '[]'),
    ('edge-audit-done', 'group-audit-done', 'done', 'node-done', 'new_session',
     '', '[]', '[]', '[]')`, historicalVersion58TransitionPrompt)
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
)`, now+10, now+11)
	execSeed(t, db, "newer unrelated same-key transition", `
INSERT INTO task_transitions (
    id, task_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (
    'transition-value-unrelated-review',
    'task-value-environment-migration',
    'review',
    'Review',
    'review',
    'Review',
    1,
    'agent',
    'applied',
    '',
    '{"summary":"unrelated review"}',
    ?,
    ?
)`, now+5, now+5)
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
SELECT current_input_values_json, prior_node_values_json, `+graphEntityIDTextFunction+`(entered_by_edge_id)
FROM task_current_nodes
WHERE task_id = 'task-value-environment-migration'`).Scan(
		&currentInputs,
		&priorNodeValues,
		&enteredByEdgeID,
	); err != nil {
		t.Fatalf("query migrated value environment: %v", err)
	}
	if currentInputs != `{"summary":"approved plan"}` ||
		priorNodeValues != `{"transition_parameters":{"review":{"summary":"approved plan"}}}` ||
		enteredByEdgeID != graphEntityIDTextByKey(t, store.db, "workflow_edges", "edge_key", "review") {
		t.Fatalf(
			"migrated value environment = inputs=%q prior=%q entered_by=%q",
			currentInputs,
			priorNodeValues,
			enteredByEdgeID,
		)
	}
	for _, column := range []string{"prompt_template", "input_fields_json", "output_fields_json"} {
		if columnExists(t, store.db, "workflow_nodes", column) {
			t.Fatalf("workflow_nodes.%s remains after migration 72", column)
		}
	}
	var preservedPrompt string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT prompt_template
FROM workflow_edges
WHERE edge_key = 'review'`).Scan(&preservedPrompt); err != nil {
		t.Fatalf("query preserved historical prompt: %v", err)
	}
	if preservedPrompt != historicalVersion58TransitionPrompt {
		t.Fatalf("historical Edge prompt = %q, want byte-for-byte preserved prompt", preservedPrompt)
	}
	workflowID, err := runtimeids.ParseWorkflowID("550e8400-e29b-41d4-a716-446655440001")
	if err != nil {
		t.Fatalf("ParseWorkflowID: %v", err)
	}
	validation := workflow.ValidateDefinition(workflow.Definition{
		ID:          workflowID,
		DisplayName: "Migrated Workflow",
		Nodes: []workflow.Node{
			workflow.StartNode{NodeIdentity: workflow.NodeIdentity{WorkflowID: workflowID, ID: "node-start", Key: "start", DisplayName: "Start"}},
			workflow.AgentNode{
				NodeIdentity: workflow.NodeIdentity{WorkflowID: workflowID, ID: "node-agent", Key: "agent", DisplayName: "Agent"},
				SubagentRole: "coder",
			},
			workflow.TerminalNode{NodeIdentity: workflow.NodeIdentity{WorkflowID: workflowID, ID: "node-done", Key: "done", DisplayName: "Done"}},
		},
		TransitionGroups: []workflow.TransitionGroup{
			{WorkflowID: workflowID, ID: "group-start", SourceNodeID: "node-start", TransitionID: "start", DisplayName: "Start"},
			{WorkflowID: workflowID, ID: "group-done", SourceNodeID: "node-agent", TransitionID: "done", DisplayName: "Done"},
		},
		Edges: []workflow.Edge{
			{WorkflowID: workflowID, ID: "edge-start", Key: "start", TransitionGroupID: "group-start", TargetNodeID: "node-agent", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Use {{.Inputs.summary}}."},
			{WorkflowID: workflowID, ID: "edge-done", Key: "done", TransitionGroupID: "group-done", TargetNodeID: "node-done", ContextMode: workflow.ContextModeNewSession},
		},
	}, workflow.ValidationOptions{
		Context:      workflow.ValidationContextExecution,
		RoleResolver: metadataTestRoleResolver{},
	})
	foundInvalidPrompt := false
	for _, diagnostic := range validation.Errors {
		if diagnostic.Code == workflow.CodeInvalidTemplatePlaceholder {
			foundInvalidPrompt = true
			break
		}
	}
	if !foundInvalidPrompt {
		t.Fatalf("post-upgrade validation = %+v, want preserved .Inputs prompt rejection", validation.Errors)
	}
}

type metadataTestRoleResolver struct{}

func (metadataTestRoleResolver) RoleExists(string) bool {
	return true
}

func (metadataTestRoleResolver) RoleToolEnabled(string, toolspec.ID) bool {
	return true
}

func (metadataTestRoleResolver) ResolveConfiguredRole(role string) (workflow.TargetAgentRole, bool) {
	return workflow.TargetAgentRole{Identity: role, QuestionsEnabled: true, ExplicitAgentCallable: true}, true
}

func (metadataTestRoleResolver) ExplicitCallableRoles() []workflow.TargetAgentRole {
	return []workflow.TargetAgentRole{
		{Identity: workflow.DefaultAgentRole, QuestionsEnabled: true, ExplicitAgentCallable: true},
		{Identity: "coder", QuestionsEnabled: true, ExplicitAgentCallable: true},
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
    ('node-review', 'workflow-550e8400-e29b-41d4-a716-446655440001', 'review', 'agent', 'Review', 'coder',
     'Review.', '[]', '[]'),
    ('node-audit', 'workflow-550e8400-e29b-41d4-a716-446655440001', 'audit', 'agent', 'Audit', 'coder',
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
SELECT json_extract(branch.target_snapshot_json, '$.prior_values')
FROM task_pending_approval_branches branch
JOIN task_pending_approvals approval ON approval.id = branch.approval_id
WHERE approval.source_task_id = 'task-frozen-prior-parameter-migration'`).Scan(&targetPriorValues); err != nil {
		t.Fatalf("query migrated pending approval target values: %v", err)
	}
	if targetPriorValues != `{"transition_parameters":{"audit":{"result":"approved review"},"review":{"summary":"approved plan"}}}` {
		t.Fatalf("migrated pending approval target prior values = %q, want frozen review and pending audit transition values", targetPriorValues)
	}
}

func TestOpenDropsVersion60FlatLegacyNodeValues(t *testing.T) {
	t.Parallel()
	root, db := openVersion60PriorValueFixture(t)
	execSeed(t, db, "restore deployed flat Current Node values", `
UPDATE task_current_nodes
SET prior_node_values_json = '{"plan":{"summary":"approved plan"}}'
WHERE task_id = 'task-invalid-values'`)
	execSeed(t, db, "restore deployed flat pending Approval target values", `
INSERT INTO task_pending_approvals (
    id,
    source_task_id,
    source_node_id,
    workflow_version,
    transition_snapshot_json,
    materialized_values_json,
    created_at_unix_ms
) VALUES (
    '47d1d167-891d-477e-931c-a139a0c99593',
    'task-invalid-values',
    'node-agent',
    1,
    '{"workflow_id":"workflow-550e8400-e29b-41d4-a716-446655440001","id":"group-review-done-invalid-values","source_node_id":"node-agent","transition_id":"done","display_name":"Done","description":"","source_display_name":"Review"}',
    '{}',
    1
);
INSERT INTO task_pending_approval_branches (
    approval_id,
    transition_branch_key,
    target_snapshot_json,
    effective_edge_configuration_json,
    context_source_resolution_json
) VALUES (
    '47d1d167-891d-477e-931c-a139a0c99593',
    'done',
    '{"node_id":"node-done","display_name":"Done","current_input_values":{},"prior_node_values":{"plan":{"summary":"approved plan"}}}',
    '{"workflow_id":"workflow-550e8400-e29b-41d4-a716-446655440001","id":"edge-review-done-invalid-values","key":"done","transition_group_id":"group-review-done-invalid-values","target_node_id":"node-done","context_mode":"new_session","context_source":{"kind":"immediate_source"},"requires_approval":true,"prompt_template":"","parameters":[],"input_bindings":[],"output_requirements":[]}',
    '{}'
)`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 60 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open version 60 store with flat prior values: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var currentPriorValues, targetPriorValues string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT prior_node_values_json
FROM task_current_nodes
WHERE task_id = 'task-invalid-values'`).Scan(&currentPriorValues); err != nil {
		t.Fatalf("query migrated version 60 Current Node values: %v", err)
	}
	if err := store.db.QueryRowContext(t.Context(), `
SELECT json_extract(target_snapshot_json, '$.prior_values')
FROM task_pending_approval_branches
WHERE approval_id = '47d1d167-891d-477e-931c-a139a0c99593'`).Scan(&targetPriorValues); err != nil {
		t.Fatalf("query migrated version 60 pending Approval values: %v", err)
	}
	want := `{"transition_parameters":{}}`
	if currentPriorValues != want || targetPriorValues != want {
		t.Fatalf("version 60 migrated values = current=%q target=%q, want %q", currentPriorValues, targetPriorValues, want)
	}
}

func openVersion60PriorValueFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyInvalidValueEnvironmentFixture(t, db, now)
	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create metadata migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 60); err != nil {
		t.Fatalf("apply version 60 current-state cutover: %v", err)
	}
	return root, db
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
			name: "missing prior Transition parameter",
			mutate: func(t *testing.T, db *sql.DB, _ int64) {
				execSeed(t, db, "missing prior Transition parameter", `
UPDATE task_transitions
SET output_values_json = '{}'
WHERE id = 'entry-transition-placement-invalid-values'`)
			},
			want: []string{"task-invalid-values", "node-agent", "transition_branch_key=serial", "value_key=.Params.review.summary", "required value is missing"},
		},
		{
			name: "malformed frozen prior Transition parameters",
			mutate: func(t *testing.T, db *sql.DB, _ int64) {
				execSeed(t, db, "malformed frozen prior Transition parameters", `
UPDATE task_runs
SET metadata_json = '{"prior_parameter_values":[]}'
WHERE id = 'run-invalid-values'`)
			},
			want: []string{"task-invalid-values", "node-agent", "transition_branch_key=serial", "decode frozen values"},
		},
		{
			name: "conflicting frozen prior Transition parameter",
			mutate: func(t *testing.T, db *sql.DB, _ int64) {
				execSeed(t, db, "conflicting frozen prior Transition parameter", `
UPDATE task_runs
SET metadata_json = '{"prior_parameter_values":{"review":{"summary":"frozen conflict"}}}'
WHERE id = 'run-invalid-values'`)
			},
			want: []string{"task-invalid-values", "node-agent", "transition_branch_key=serial", "value_key=.Params.review.summary", "frozen value conflicts"},
		},
		{
			name: "prior Transition parameter ordering tie",
			mutate: func(t *testing.T, db *sql.DB, now int64) {
				execSeed(t, db, "tied prior Transition parameter", `
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
			want: []string{"task-invalid-values", "node-agent", "transition_branch_key=serial", "value_key=.Params.review.summary", "ordering tie"},
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
	execSeed(t, db, "prior-Transition-parameter requirement graph", `
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
    'workflow-550e8400-e29b-41d4-a716-446655440001',
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
    'Continue with {{.Params.review.summary}}.',
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
    '{"prior_parameter_values":{}}'
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
    `+graphEntityIDTextFunction+`(node_id),
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
	if nodeID != workflowGraphSeedIDText(t, store.db, "node-agent") ||
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
