package metadata

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenProjectsMigratesPartialParallelJoinArrival(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyPartialParallelJoinArrivalFixture(
		t,
		db,
		"project-parallel-arrival-migration",
		"task-parallel-arrival-migration",
		now,
	)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rows, err := store.db.QueryContext(t.Context(), `
SELECT transition_branch_key, arrival_state, arrival_values_json
FROM task_active_fanout_branches
WHERE task_id = 'task-parallel-arrival-migration'
ORDER BY transition_branch_key`)
	if err != nil {
		t.Fatalf("query migrated parallel join arrivals: %v", err)
	}
	defer func() { _ = rows.Close() }()
	type arrival struct {
		state  string
		values sql.NullString
	}
	arrivals := map[string]arrival{}
	for rows.Next() {
		var key string
		var item arrival
		if err := rows.Scan(&key, &item.state, &item.values); err != nil {
			t.Fatalf("scan migrated parallel join arrival: %v", err)
		}
		arrivals[key] = item
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated parallel join arrivals: %v", err)
	}
	want := map[string]arrival{
		"split_a": {state: "arrived", values: sql.NullString{String: `{"joined":"a"}`, Valid: true}},
		"split_b": {state: "pending"},
	}
	if !reflect.DeepEqual(arrivals, want) {
		t.Fatalf("migrated parallel join arrivals = %+v, want %+v", arrivals, want)
	}

	var currentNodeID, currentBranchKey string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT `+graphEntityIDTextFunction+`(node_id), transition_branch_key
FROM task_current_nodes
WHERE task_id = 'task-parallel-arrival-migration'`).Scan(&currentNodeID, &currentBranchKey); err != nil {
		t.Fatalf("query remaining current branch: %v", err)
	}
	if currentNodeID != graphEntityIDTextByKey(t, store.db, "workflow_nodes", "node_key", "branch_b") ||
		currentBranchKey != "split_b" {
		t.Fatalf("remaining current branch = node=%q branch=%q", currentNodeID, currentBranchKey)
	}
}

func TestOpenRejectsAmbiguousParallelJoinArrivalWithBranchContext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyPartialParallelJoinArrivalFixture(
		t,
		db,
		"project-ambiguous-parallel-arrival",
		"task-ambiguous-parallel-arrival",
		now,
	)
	execSeed(t, db, "competing Join arrival", `
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (
    'transition-parallel-arrival-a-competing',
    'task-ambiguous-parallel-arrival',
    'run-parallel-a',
    'placement-parallel-a',
    'branch_a',
    'Branch A',
    'join',
    'Join',
    1,
    'agent',
    'applied',
    '',
    '{"joined":"competing"}',
    ?,
    ?
);
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-parallel-arrival-a-competing',
    'transition-parallel-arrival-a-competing',
    'edge-branch-a-join',
    'join_a',
    'node-join',
    'join',
    'Join',
    'join',
    'applied',
    'new_session',
    0,
    '[]',
    '[]',
    '{}'
)`, now+5, now+5)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	_, err = Open(root)
	if err == nil {
		t.Fatal("Open unexpectedly selected one of several Join arrivals")
	}
	for _, expected := range []string{
		"task-ambiguous-parallel-arrival",
		"node-branch-a",
		"transition_branch_key=split_a",
		"error_kind=join_arrival_ambiguity",
		"arrival_count=2",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("Open error = %v, want context %q", err, expected)
		}
	}
}

func seedLegacyPartialParallelJoinArrivalFixture(t *testing.T, db *sql.DB, projectID, taskID string, now int64) {
	t.Helper()
	seedLegacyParallelMigrationFixture(t, db, projectID, taskID, now)
	execSeed(t, db, "join node", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, join_input_providers_json, output_fields_json
) VALUES (
    'node-join',
    'workflow-550e8400-e29b-41d4-a716-446655440001',
    'join',
    'join',
    'Join',
    '[]',
    '[]'
)`)
	execSeed(t, db, "join edges", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES
    ('group-branch-a-join', 'node-branch-a', 'join', 'Join'),
    ('group-branch-b-join', 'node-branch-b', 'join', 'Join');
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode, input_bindings_json, output_requirements_json
) VALUES
    ('edge-branch-a-join', 'group-branch-a-join', 'join_a', 'node-join', 'new_session', '[]', '[]'),
    ('edge-branch-b-join', 'group-branch-b-join', 'join_b', 'node-join', 'new_session', '[]', '[]')`)
	execSeed(t, db, "completed branch", `
UPDATE task_node_placements
SET state = 'completed', updated_at_unix_ms = ?
WHERE id = 'placement-parallel-a';
UPDATE task_runs
SET completed_at_unix_ms = ?, updated_at_unix_ms = ?
WHERE id = 'run-parallel-a'`, now+5, now+5, now+5)
	execSeed(t, db, "join arrival transition", `
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (
    'transition-parallel-arrival-a',
    ?,
    'run-parallel-a',
    'placement-parallel-a',
    'branch_a',
    'Branch A',
    'join',
    'Join',
    1,
    'agent',
    'applied',
    '',
    '{"joined":"a"}',
    ?,
    ?
);
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-parallel-arrival-a',
    'transition-parallel-arrival-a',
    'edge-branch-a-join',
    'join_a',
    'node-join',
    'join',
    'Join',
    'join',
    'applied',
    'new_session',
    0,
    '[]',
    '[]',
    '{}'
)`, taskID, now+5, now+5)
}

func TestOpenProjectsMigratesPendingApprovalOnParallelBranch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-parallel-approval-migration", "task-parallel-approval-migration", now)
	execSeed(t, db, "approval edge", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES ('group-branch-a-done', 'node-branch-a', 'done', 'Done');
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, requires_approval,
    context_mode, input_bindings_json, output_requirements_json
) VALUES (
    'edge-branch-a-done',
    'group-branch-a-done',
    'done',
    'node-done',
    1,
    'new_session',
    '[{"name":"summary","source":"transition_output","field":"summary"}]',
    '[]'
)`)
	execSeed(t, db, "waiting branch approval", `
UPDATE task_node_placements
SET state = 'waiting_approval', updated_at_unix_ms = ?
WHERE id = 'placement-parallel-a';
UPDATE task_runs
SET completed_at_unix_ms = ?, updated_at_unix_ms = ?
WHERE id = 'run-parallel-a';
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms
) VALUES (
    'transition-parallel-approval-a',
    'task-parallel-approval-migration',
    'run-parallel-a',
    'placement-parallel-a',
    'branch_a',
    'Branch A',
    'done',
    'Done',
    1,
    'agent',
    'pending_approval',
    '',
    '{"summary":"approved branch"}',
    ?
);
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-parallel-approval-a',
    'transition-parallel-approval-a',
    'edge-branch-a-done',
    'done',
    'node-done',
    'done',
    'Done',
    'terminal',
    'pending',
    'new_session',
    1,
    '[{"name":"summary","source":"transition_output","field":"summary"}]',
    '[]',
    '{"node_output_values":{"agent":{"summary":"parallel source"}}}'
)`, now+5, now+5, now+5)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var sourceNodeID, sourceBranchKey string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT `+graphEntityIDTextFunction+`(source_node_id), source_transition_branch_key
FROM task_pending_approvals
WHERE source_task_id = 'task-parallel-approval-migration'`).Scan(&sourceNodeID, &sourceBranchKey); err != nil {
		t.Fatalf("query migrated parallel pending approval: %v", err)
	}
	if sourceNodeID != graphEntityIDTextByKey(t, store.db, "workflow_nodes", "node_key", "branch_a") ||
		sourceBranchKey != "split_a" {
		t.Fatalf("migrated parallel pending approval source = node=%q branch=%q", sourceNodeID, sourceBranchKey)
	}

	var targetBranchKey, targetEnteredByEdgeID, targetInputs, targetPriorValues string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    json_extract(branch.target_snapshot_json, '$.transition_branch_key'),
    json_extract(branch.target_snapshot_json, '$.entered_by_edge_id'),
    json_extract(branch.target_snapshot_json, '$.current_input_values'),
    json_extract(branch.target_snapshot_json, '$.prior_values')
FROM task_pending_approval_branches branch
JOIN task_pending_approvals approval ON approval.id = branch.approval_id
WHERE approval.source_task_id = 'task-parallel-approval-migration'`).Scan(
		&targetBranchKey,
		&targetEnteredByEdgeID,
		&targetInputs,
		&targetPriorValues,
	); err != nil {
		t.Fatalf("query migrated parallel approval target: %v", err)
	}
	if targetBranchKey != "split_a" ||
		targetEnteredByEdgeID != graphEntityIDTextByKey(t, store.db, "workflow_edges", "edge_key", "done") ||
		targetInputs != `{"summary":"approved branch"}` ||
		targetPriorValues != `{"transition_parameters":{}}` {
		t.Fatalf(
			"migrated parallel approval target = branch=%q entered_by=%q inputs=%q prior=%q",
			targetBranchKey,
			targetEnteredByEdgeID,
			targetInputs,
			targetPriorValues,
		)
	}

	rows, err := store.db.QueryContext(t.Context(), `
SELECT `+graphEntityIDTextFunction+`(node_id), transition_branch_key, scheduling_state
FROM task_current_nodes
WHERE task_id = 'task-parallel-approval-migration'
ORDER BY transition_branch_key`)
	if err != nil {
		t.Fatalf("query migrated parallel approval current nodes: %v", err)
	}
	defer func() { _ = rows.Close() }()
	type currentNodeState struct {
		nodeID     string
		scheduling sql.NullString
	}
	currentNodes := map[string]currentNodeState{}
	for rows.Next() {
		var branch string
		var state currentNodeState
		if err := rows.Scan(&state.nodeID, &branch, &state.scheduling); err != nil {
			t.Fatalf("scan migrated parallel approval current node: %v", err)
		}
		currentNodes[branch] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated parallel approval current nodes: %v", err)
	}
	if branch := currentNodes["split_a"]; branch.nodeID != graphEntityIDTextByKey(t, store.db, "workflow_nodes", "node_key", "branch_a") || branch.scheduling.Valid {
		t.Fatalf("pending approval branch current node = %+v", branch)
	}
	if branch := currentNodes["split_b"]; branch.nodeID != graphEntityIDTextByKey(t, store.db, "workflow_nodes", "node_key", "branch_b") || !branch.scheduling.Valid || branch.scheduling.String != "interrupted" {
		t.Fatalf("sibling branch current node = %+v", branch)
	}
}

func TestOpenProjectsMigratesSeveralPendingApprovalsOnParallelBranches(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-parallel-approvals-migration", "task-parallel-approvals-migration", now)
	for _, branch := range []struct {
		key       string
		nodeID    string
		placement string
		runID     string
	}{
		{"a", "node-branch-a", "placement-parallel-a", "run-parallel-a"},
		{"b", "node-branch-b", "placement-parallel-b", "run-parallel-b"},
	} {
		execSeed(t, db, "approval transition group", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES (?, ?, 'done', 'Done')`,
			"group-parallel-"+branch.key+"-done",
			branch.nodeID,
		)
		execSeed(t, db, "approval edge", `
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, requires_approval,
    context_mode, input_bindings_json, output_requirements_json
) VALUES (?, ?, 'done', ?, 1, 'new_session', '[]', '[]')`,
			"edge-parallel-"+branch.key+"-done",
			"group-parallel-"+branch.key+"-done",
			branch.nodeID,
		)
		execSeed(t, db, "waiting branch placement", `
UPDATE task_node_placements SET state = 'waiting_approval' WHERE id = ?`, branch.placement)
		execSeed(t, db, "completed branch run", `
UPDATE task_runs SET completed_at_unix_ms = ?, updated_at_unix_ms = ? WHERE id = ?`,
			now+5,
			now+5,
			branch.runID,
		)
		execSeed(t, db, "pending branch transition", `
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms
) VALUES (?, 'task-parallel-approvals-migration', ?, ?, ?, ?, 'done', 'Done', 1, 'agent', 'pending_approval', '', '{}', ?)`,
			"transition-parallel-"+branch.key+"-approval",
			branch.runID,
			branch.placement,
			"branch_"+branch.key,
			"Branch "+strings.ToUpper(branch.key),
			now+5,
		)
		execSeed(t, db, "pending branch transition edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (?, ?, ?, 'done', 'node-done', 'done', 'Done', 'terminal', 'pending', 'new_session', 1, '[]', '[]', '{}')`,
			"transition-edge-parallel-"+branch.key+"-approval",
			"transition-parallel-"+branch.key+"-approval",
			"edge-parallel-"+branch.key+"-done",
		)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var approvalCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM task_pending_approvals
WHERE source_task_id = 'task-parallel-approvals-migration'`).Scan(&approvalCount); err != nil {
		t.Fatalf("count migrated parallel approvals: %v", err)
	}
	if approvalCount != 2 {
		t.Fatalf("migrated parallel approval count = %d, want 2", approvalCount)
	}
}

func seedLegacyParallelMigrationFixture(t *testing.T, db *sql.DB, projectID, taskID string, now int64) {
	t.Helper()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Project', ?, ?, '{}')`, projectID, now, now)
	seedWorkflowGraph(t, db, projectID, now)
	execSeed(t, db, "branch nodes", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role, prompt_template, output_fields_json
) VALUES (
    'node-branch-a',
    'workflow-550e8400-e29b-41d4-a716-446655440001',
    'branch_a',
    'agent',
    'Branch A',
    'coder',
    'A.',
    '[]'
), (
    'node-branch-b',
    'workflow-550e8400-e29b-41d4-a716-446655440001',
    'branch_b',
    'agent',
    'Branch B',
    'coder',
    'B.',
    '[]'
)`)
	execSeed(t, db, "fanout edges", `
DELETE FROM workflow_edges
WHERE id = 'edge-done-1';
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode,
    prompt_template, input_bindings_json, output_requirements_json
) VALUES (
    'edge-split-a',
    'group-done',
    'split_a',
    'node-branch-a',
    'new_session',
    'Use {{.Params.summary}}.',
    '[{"name":"summary","source":"transition_output","field":"summary"}]',
    '[]'
), (
    'edge-split-b',
    'group-done',
    'split_b',
    'node-branch-b',
    'new_session',
    'Use {{.Params.summary}}.',
    '[{"name":"summary","source":"transition_output","field":"summary"}]',
    '[]'
)`)
	execSeed(t, db, "task", workflowSeedTaskSQL, taskID, "link-1", 1, "PAR-1", now, now)
	execSeed(t, db, "fanout source placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-parallel-source',
    ?,
    'node-agent',
    'completed',
    ?,
    ?
)`, taskID, now, now+1)
	execSeed(t, db, "fanout transition", `
INSERT INTO task_transitions (
    id, task_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (
    'transition-parallel-split',
    ?,
    'placement-parallel-source',
    'agent',
    'Agent',
    'done',
    'Done',
    1,
    'agent',
    'applied',
    '',
    '{"summary":"parallel source"}',
    ?,
    ?
)`, taskID, now+2, now+2)
	for _, branch := range []struct {
		placementID string
		nodeID      string
		edgeID      string
		edgeKey     string
		runID       string
	}{
		{"placement-parallel-a", "node-branch-a", "edge-split-a", "split_a", "run-parallel-a"},
		{"placement-parallel-b", "node-branch-b", "edge-split-b", "split_b", "run-parallel-b"},
	} {
		execSeed(t, db, "parallel placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, parallel_batch_transition_id, parallel_branch_edge_id,
    created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, ?, 'active', 'transition-parallel-split', ?, ?, ?)`,
			branch.placementID,
			taskID,
			branch.nodeID,
			branch.edgeID,
			now+3,
			now+3,
		)
		execSeed(t, db, "parallel run", `
INSERT INTO task_runs (
    id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, 1, ?, ?)`, branch.runID, branch.placementID, now+3, now+4)
		execSeed(t, db, "fanout transition edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    target_placement_id, state, context_mode, requires_approval,
    input_bindings_json, output_requirements_json, metadata_json
) VALUES (?, 'transition-parallel-split', ?, ?, ?, ?, ?, 'agent', ?, 'applied', 'new_session', 0, '[{"name":"summary","source":"transition_output","field":"summary"}]', '[]', '{}')`,
			"transition-edge-"+branch.edgeKey,
			branch.edgeID,
			branch.edgeKey,
			branch.nodeID,
			branch.edgeKey,
			"Branch "+string(branch.edgeKey[len(branch.edgeKey)-1]),
			branch.placementID,
		)
	}
}
