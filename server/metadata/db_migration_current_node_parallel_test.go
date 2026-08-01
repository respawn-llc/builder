package metadata

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenProjectsMigratesActiveParallelFanout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-parallel-migration", "task-parallel-migration", now)
	execSeed(t, db, "runnable parallel branch", `
DELETE FROM task_runs
WHERE id = 'run-parallel-b'`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var fanoutCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM task_active_fanouts
WHERE task_id = 'task-parallel-migration'`).Scan(&fanoutCount); err != nil {
		t.Fatalf("count migrated active fanout: %v", err)
	}
	if fanoutCount != 1 {
		t.Fatalf("migrated active fanout count = %d, want 1", fanoutCount)
	}

	rows, err := store.db.QueryContext(t.Context(), `
SELECT
    branch.transition_branch_key,
    branch.arrival_state,
    current_node.node_id,
    current_node.scheduling_state,
    current_node.current_input_values_json,
    current_node.prior_node_values_json,
    current_node.entered_by_edge_id
FROM task_active_fanout_branches branch
JOIN task_current_nodes current_node
    ON current_node.task_id = branch.task_id
   AND current_node.transition_branch_key = branch.transition_branch_key
WHERE branch.task_id = 'task-parallel-migration'
ORDER BY branch.transition_branch_key`)
	if err != nil {
		t.Fatalf("query migrated parallel branches: %v", err)
	}
	defer func() { _ = rows.Close() }()
	type branchState struct {
		arrivalState    string
		currentNodeID   string
		schedulingState string
		currentInputs   string
		priorNodeValues string
		enteredByEdgeID string
	}
	branches := map[string]branchState{}
	for rows.Next() {
		var key string
		var state branchState
		if err := rows.Scan(
			&key,
			&state.arrivalState,
			&state.currentNodeID,
			&state.schedulingState,
			&state.currentInputs,
			&state.priorNodeValues,
			&state.enteredByEdgeID,
		); err != nil {
			t.Fatalf("scan migrated parallel branch: %v", err)
		}
		branches[key] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated parallel branches: %v", err)
	}
	want := map[string]branchState{
		"split_a": {
			arrivalState:    "pending",
			currentNodeID:   "node-branch-a",
			schedulingState: "interrupted",
			currentInputs:   `{"summary":"parallel source"}`,
			priorNodeValues: `{"transition_parameters":{}}`,
			enteredByEdgeID: "edge-split-a",
		},
		"split_b": {
			arrivalState:    "pending",
			currentNodeID:   "node-branch-b",
			schedulingState: "interrupted",
			currentInputs:   `{"summary":"parallel source"}`,
			priorNodeValues: `{"transition_parameters":{}}`,
			enteredByEdgeID: "edge-split-b",
		},
	}
	if !reflect.DeepEqual(branches, want) {
		t.Fatalf("migrated parallel branches = %+v, want %+v", branches, want)
	}
}

func TestOpenRejectsAmbiguousParallelBranchWithTaskNodeAndBranchContext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(
		t,
		db,
		"project-ambiguous-parallel-branch",
		"task-ambiguous-parallel-branch",
		now,
	)
	execSeed(t, db, "duplicate parallel branch placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, parallel_batch_transition_id, parallel_branch_edge_id,
    created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-parallel-a-duplicate',
    'task-ambiguous-parallel-branch',
    'node-branch-a',
    'active',
    'transition-parallel-split',
    'edge-split-a',
    ?,
    ?
);
INSERT INTO task_runs (
    id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'run-parallel-a-duplicate',
    'placement-parallel-a-duplicate',
    1,
    ?,
    ?
);
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    target_placement_id, state, context_mode, requires_approval,
    input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-split_a-duplicate',
    'transition-parallel-split',
    'edge-split-a',
    'split_a',
    'node-branch-a',
    'split_a',
    'Branch a',
    'agent',
    'placement-parallel-a-duplicate',
    'applied',
    'new_session',
    0,
    '[{"name":"summary","source":"transition_output","field":"summary"}]',
    '[]',
    '{}'
)`, now+3, now+3, now+3, now+4)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	_, err = Open(root)
	if err == nil {
		t.Fatal("Open unexpectedly accepted two Current Nodes for one parallel branch")
	}
	for _, expected := range []string{
		"task-ambiguous-parallel-branch",
		"node_id=node-branch-a",
		"transition_branch_key=split_a",
		"error_kind=duplicate_branch",
		"placement_count=2",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("Open error = %v, want context %q", err, expected)
		}
	}
}

func TestOpenProjectsRetainsParallelSessionBranchAssociation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	const sessionID = "550e8400-e29b-41d4-a716-446655440007"
	seedLegacyParallelMigrationFixture(
		t,
		db,
		"project-parallel-session-association-migration",
		"task-parallel-session-association-migration",
		now,
	)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-parallel-session-association-migration",
		"workspace-parallel-session-association-migration",
		sessionID,
		now,
	)
	execSeed(t, db, "parallel branch session", `
UPDATE task_runs
SET session_id = ?
WHERE id = 'run-parallel-a'`, sessionID)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var taskID, nodeID, branchKey string
	var associatedAt int64
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    session.task_id,
    association.node_id,
    association.transition_branch_key,
    association.associated_at_unix_ms
FROM sessions session
JOIN session_workflow_node_associations association ON association.session_id = session.id
WHERE session.id = ?`, sessionID).Scan(&taskID, &nodeID, &branchKey, &associatedAt); err != nil {
		t.Fatalf("query migrated parallel Session association: %v", err)
	}
	if taskID != "task-parallel-session-association-migration" ||
		nodeID != "node-branch-a" ||
		branchKey != "split_a" ||
		associatedAt != now+4 {
		t.Fatalf(
			"migrated parallel Session association = task=%q node=%q branch=%q associated_at=%d",
			taskID,
			nodeID,
			branchKey,
			associatedAt,
		)
	}
}

func TestOpenProjectsDiscardsCompletedParallelHistory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-completed-parallel-history", "task-completed-parallel-history", now)
	execSeed(t, db, "complete parallel history", `
UPDATE task_node_placements
SET state = 'completed', updated_at_unix_ms = ?
WHERE id IN ('placement-parallel-a', 'placement-parallel-b');
UPDATE task_runs
SET completed_at_unix_ms = ?, updated_at_unix_ms = ?
WHERE id IN ('run-parallel-a', 'run-parallel-b');
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-completed-parallel-terminal',
    'task-completed-parallel-history',
    'node-done',
    'active',
    ?,
    ?
)`, now+5, now+5, now+5, now+5)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	var fanoutCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    (SELECT node_id FROM task_current_nodes WHERE task_id = 'task-completed-parallel-history'),
    (SELECT COUNT(*) FROM task_active_fanouts WHERE task_id = 'task-completed-parallel-history')`).Scan(&nodeID, &fanoutCount); err != nil {
		t.Fatalf("query discarded completed parallel history: %v", err)
	}
	if nodeID != "node-done" || fanoutCount != 0 {
		t.Fatalf("discarded completed parallel history = node=%q fanouts=%d", nodeID, fanoutCount)
	}
}

func TestOpenProjectsDiscardsCanceledParallelFanout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-canceled-parallel-migration", "task-canceled-parallel-migration", now)
	execSeed(t, db, "canceled task", `
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-parallel-migration'`, now+5)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	var fanoutCount, approvalCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    (SELECT node_id FROM task_current_nodes WHERE task_id = 'task-canceled-parallel-migration'),
    (SELECT COUNT(*) FROM task_active_fanouts WHERE task_id = 'task-canceled-parallel-migration'),
    (SELECT COUNT(*) FROM task_pending_approvals WHERE source_task_id = 'task-canceled-parallel-migration')`).Scan(
		&nodeID,
		&fanoutCount,
		&approvalCount,
	); err != nil {
		t.Fatalf("query normalized canceled parallel aggregate: %v", err)
	}
	if nodeID != "node-done" || fanoutCount != 0 || approvalCount != 0 {
		t.Fatalf("normalized canceled parallel aggregate = node=%q fanouts=%d approvals=%d", nodeID, fanoutCount, approvalCount)
	}
}

func TestOpenProjectsDiscardsCanceledParallelPendingApproval(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-canceled-parallel-approval-migration", "task-canceled-parallel-approval-migration", now)
	execSeed(t, db, "approval edge", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES ('group-canceled-branch-a-done', 'node-branch-a', 'done', 'Done');
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, requires_approval,
    context_mode, input_bindings_json, output_requirements_json
) VALUES (
    'edge-canceled-branch-a-done',
    'group-canceled-branch-a-done',
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
    'transition-canceled-parallel-approval-a',
    'task-canceled-parallel-approval-migration',
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
    'transition-edge-canceled-parallel-approval-a',
    'transition-canceled-parallel-approval-a',
    'edge-canceled-branch-a-done',
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
);
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-parallel-approval-migration'`, now+5, now+5, now+5, now+6)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	var currentNodeCount, approvalCount, fanoutCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    (SELECT node_id FROM task_current_nodes WHERE task_id = 'task-canceled-parallel-approval-migration'),
    (SELECT COUNT(*) FROM task_current_nodes WHERE task_id = 'task-canceled-parallel-approval-migration'),
    (SELECT COUNT(*) FROM task_pending_approvals WHERE source_task_id = 'task-canceled-parallel-approval-migration'),
    (SELECT COUNT(*) FROM task_active_fanouts WHERE task_id = 'task-canceled-parallel-approval-migration')`).Scan(
		&nodeID,
		&currentNodeCount,
		&approvalCount,
		&fanoutCount,
	); err != nil {
		t.Fatalf("query normalized canceled parallel approval aggregate: %v", err)
	}
	if nodeID != "node-done" ||
		currentNodeCount != 1 ||
		approvalCount != 0 ||
		fanoutCount != 0 {
		t.Fatalf(
			"normalized canceled parallel approval aggregate = node=%q current_nodes=%d approvals=%d fanouts=%d",
			nodeID,
			currentNodeCount,
			approvalCount,
			fanoutCount,
		)
	}
}
