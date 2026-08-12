package metadata

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenProjectsNormalizesCanceledTaskToCanonicalDone(t *testing.T) {
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
VALUES ('project-canceled-done-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-canceled-done-migration",
		"workspace-canceled-done-migration",
		"550e8400-e29b-41d4-a716-446655440008",
		now,
	)
	seedWorkflowGraph(t, db, "project-canceled-done-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-canceled-done-migration", "link-1", 1, "CAN-1", now, now)
	execSeed(t, db, "canceled task", `
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-done-migration'`, now+1)
	execSeed(t, db, "active agent placement", workflowSeedPlacementSQL, "placement-canceled-done-migration", "task-canceled-done-migration", "node-agent", now, now)
	execSeed(t, db, "active agent run", `
INSERT INTO task_runs (
    id, placement_id, session_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'run-canceled-done-migration',
    'placement-canceled-done-migration',
    '550e8400-e29b-41d4-a716-446655440008',
    1,
    ?,
    ?
)`, now, now+2)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	var schedulingState sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT `+graphEntityIDTextFunction+`(node_id), scheduling_state
FROM task_current_nodes
WHERE task_id = 'task-canceled-done-migration'`).Scan(&nodeID, &schedulingState); err != nil {
		t.Fatalf("query normalized canceled current node: %v", err)
	}
	if nodeID != workflowGraphSeedIDText(t, store.db, "node-done") || schedulingState.Valid {
		t.Fatalf("normalized canceled current node = node=%q scheduling=%+v", nodeID, schedulingState)
	}

	var currentNodeCount, approvalCount, fanoutCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    (SELECT COUNT(*) FROM task_current_nodes WHERE task_id = 'task-canceled-done-migration'),
    (SELECT COUNT(*) FROM task_pending_approvals WHERE source_task_id = 'task-canceled-done-migration'),
    (SELECT COUNT(*) FROM task_active_fanouts WHERE task_id = 'task-canceled-done-migration')`).Scan(
		&currentNodeCount,
		&approvalCount,
		&fanoutCount,
	); err != nil {
		t.Fatalf("query normalized canceled aggregate: %v", err)
	}
	if currentNodeCount != 1 || approvalCount != 0 || fanoutCount != 0 {
		t.Fatalf(
			"normalized canceled aggregate = current_nodes=%d approvals=%d fanouts=%d",
			currentNodeCount,
			approvalCount,
			fanoutCount,
		)
	}

	var taskID, associationNodeID string
	var associatedAt int64
	if err := store.db.QueryRowContext(t.Context(), `
SELECT session.task_id, `+graphEntityIDTextFunction+`(association.node_id), association.associated_at_unix_ms
FROM sessions session
JOIN session_workflow_node_associations association ON association.session_id = session.id
WHERE session.id = '550e8400-e29b-41d4-a716-446655440008'`).Scan(&taskID, &associationNodeID, &associatedAt); err != nil {
		t.Fatalf("query normalized canceled Session association: %v", err)
	}
	if taskID != "task-canceled-done-migration" ||
		associationNodeID != workflowGraphSeedIDText(t, store.db, "node-agent") ||
		associatedAt != now+2 {
		t.Fatalf(
			"normalized canceled Session association = task=%q node=%q associated_at=%d",
			taskID,
			associationNodeID,
			associatedAt,
		)
	}
}

func TestOpenProjectsDiscardsCanceledSerialPendingApproval(t *testing.T) {
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
VALUES ('project-canceled-approval-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-canceled-approval-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-canceled-approval-migration", "link-1", 1, "CAN-8", now, now)
	execSeed(t, db, "canceled task", `
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-approval-migration'`, now+1)
	execSeed(t, db, "waiting approval placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-canceled-approval-migration',
    'task-canceled-approval-migration',
    'node-agent',
    'waiting_approval',
    ?,
    ?
)`, now, now+1)
	execSeed(t, db, "completed source run", `
INSERT INTO task_runs (
    id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms, completed_at_unix_ms
) VALUES (
    'run-canceled-approval-migration',
    'placement-canceled-approval-migration',
    1,
    ?, ?, ?
)`, now, now+2, now+2)
	execSeed(t, db, "pending approval transition", `
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms
) VALUES (
    'transition-canceled-approval-migration',
    'task-canceled-approval-migration',
    'run-canceled-approval-migration',
    'placement-canceled-approval-migration',
    'agent',
    'Agent',
    'done',
    'Done',
    1,
    'agent',
    'pending_approval',
    '',
    '{}',
    ?
);
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-canceled-approval-migration',
    'transition-canceled-approval-migration',
    'edge-done-1',
    'done',
    'node-done',
    'done',
    'Done',
    'terminal',
    'pending',
    'new_session',
    1,
    '[]',
    '[]',
    '{}'
)`, now+3)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	var approvalCount, fanoutCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    (SELECT `+graphEntityIDTextFunction+`(node_id) FROM task_current_nodes WHERE task_id = 'task-canceled-approval-migration'),
    (SELECT COUNT(*) FROM task_pending_approvals WHERE source_task_id = 'task-canceled-approval-migration'),
    (SELECT COUNT(*) FROM task_active_fanouts WHERE task_id = 'task-canceled-approval-migration')`).Scan(
		&nodeID,
		&approvalCount,
		&fanoutCount,
	); err != nil {
		t.Fatalf("query normalized canceled approval aggregate: %v", err)
	}
	if nodeID != workflowGraphSeedIDText(t, store.db, "node-done") || approvalCount != 0 || fanoutCount != 0 {
		t.Fatalf("normalized canceled approval aggregate = node=%q approvals=%d fanouts=%d", nodeID, approvalCount, fanoutCount)
	}
}

func TestOpenProjectsCanceledTaskWithoutCanonicalDonePreservesUniqueActiveTerminal(t *testing.T) {
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
VALUES ('project-canceled-terminal-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-canceled-terminal-migration", now)
	execSeed(t, db, "rename canonical terminal", `
UPDATE workflow_nodes
SET node_key = 'finished'
WHERE id = 'node-done'`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-canceled-terminal-migration", "link-1", 1, "CAN-2", now, now)
	execSeed(t, db, "canceled task", `
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-terminal-migration'`, now+1)
	execSeed(t, db, "selected terminal placement", workflowSeedPlacementSQL, "placement-canceled-terminal-migration", "task-canceled-terminal-migration", "node-done", now, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT `+graphEntityIDTextFunction+`(node_id)
FROM task_current_nodes
WHERE task_id = 'task-canceled-terminal-migration'`).Scan(&nodeID); err != nil {
		t.Fatalf("query normalized selected terminal current node: %v", err)
	}
	wantNodeID := graphEntityIDTextByKey(t, store.db, "workflow_nodes", "node_key", "finished")
	if nodeID != wantNodeID {
		t.Fatalf("normalized selected terminal current node = %q, want %q", nodeID, wantNodeID)
	}
}

func TestOpenProjectsCanceledTaskWithoutCanonicalDoneWithNonterminalLegacyCandidate(t *testing.T) {
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
VALUES ('project-canceled-invalid-terminal-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-canceled-invalid-terminal-migration", now)
	execSeed(t, db, "rename canonical terminal", `
UPDATE workflow_nodes
SET node_key = 'finished'
WHERE id = 'node-done'`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-canceled-invalid-terminal-migration", "link-1", 1, "CAN-3", now, now)
	execSeed(t, db, "canceled task", `
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-invalid-terminal-migration'`, now+1)
	execSeed(t, db, "non-terminal active candidate", workflowSeedPlacementSQL, "placement-canceled-invalid-terminal-migration", "task-canceled-invalid-terminal-migration", "node-agent", now, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	assertMigratedCurrentNodeID(t, store, "task-canceled-invalid-terminal-migration", "finished")
}

func TestOpenProjectsCanceledTaskWithoutCanonicalDoneWithoutLegacyCandidate(t *testing.T) {
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
VALUES ('project-canceled-missing-terminal-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-canceled-missing-terminal-migration", now)
	execSeed(t, db, "rename canonical terminal", `
UPDATE workflow_nodes
SET node_key = 'finished'
WHERE id = 'node-done'`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-canceled-missing-terminal-migration", "link-1", 1, "CAN-5", now, now)
	execSeed(t, db, "canceled task", `
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-missing-terminal-migration'`, now+1)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	assertMigratedCurrentNodeID(t, store, "task-canceled-missing-terminal-migration", "finished")
}

func TestOpenProjectsCanceledTaskWithoutCanonicalDoneWithAmbiguousLegacyCandidates(t *testing.T) {
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
VALUES ('project-canceled-ambiguous-terminal-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-canceled-ambiguous-terminal-migration", now)
	execSeed(t, db, "rename canonical terminal", `
UPDATE workflow_nodes
SET node_key = 'finished'
WHERE id = 'node-done';
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, output_fields_json)
VALUES ('node-terminal-alternate', 'workflow-550e8400-e29b-41d4-a716-446655440001', 'alternate', 'terminal', 'Alternate', '[]')`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-canceled-ambiguous-terminal-migration", "link-1", 1, "CAN-6", now, now)
	execSeed(t, db, "canceled task", `
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-ambiguous-terminal-migration'`, now+1)
	execSeed(t, db, "terminal placements", `
INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms)
VALUES
    ('placement-canceled-ambiguous-one', 'task-canceled-ambiguous-terminal-migration', 'node-done', 'active', ?, ?),
    ('placement-canceled-ambiguous-two', 'task-canceled-ambiguous-terminal-migration', 'node-terminal-alternate', 'active', ?, ?)`,
		now,
		now,
		now,
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

	assertMigratedCurrentNodeID(t, store, "task-canceled-ambiguous-terminal-migration", "finished")
}

func TestOpenProjectsCanceledTaskWithoutCanonicalDoneWithForeignLegacyCandidate(t *testing.T) {
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
VALUES ('project-canceled-foreign-terminal-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-canceled-foreign-terminal-migration", now)
	seedWorkflowGraphForProject(t, db, "project-canceled-foreign-terminal-migration", now, "2")
	execSeed(t, db, "rename canonical terminal", `
UPDATE workflow_nodes
SET node_key = 'finished'
WHERE id = 'node-done'`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-canceled-foreign-terminal-migration", "link-1", 1, "CAN-7", now, now)
	execSeed(t, db, "canceled task", `
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-foreign-terminal-migration'`, now+1)
	execSeed(t, db, "relax legacy placement trigger", `
DROP TRIGGER task_node_placements_runtime_insert;
DROP TRIGGER task_node_placements_runtime_update`)
	execSeed(t, db, "foreign terminal placement", `
INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms)
VALUES (
    'placement-canceled-foreign-terminal',
    'task-canceled-foreign-terminal-migration',
    'node-done-2',
    'active',
    ?,
    ?
)`, now, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	assertMigratedCurrentNodeID(t, store, "task-canceled-foreign-terminal-migration", "finished")
}

func assertMigratedCurrentNodeID(t *testing.T, store *Store, taskID, wantNodeKey string) {
	t.Helper()

	var gotNodeID string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT `+graphEntityIDTextFunction+`(node_id)
FROM task_current_nodes
WHERE task_id = ?`, taskID).Scan(&gotNodeID); err != nil {
		t.Fatalf("query migrated current node for task %q: %v", taskID, err)
	}
	wantNodeID := graphEntityIDTextByKey(t, store.db, "workflow_nodes", "node_key", wantNodeKey)
	if gotNodeID != wantNodeID {
		t.Fatalf("migrated current node for task %q = %q, want %q", taskID, gotNodeID, wantNodeID)
	}
}

func TestOpenOmitsInvalidCanceledTaskWithoutCanonicalDoneAndRetainsNeutralSession(t *testing.T) {
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
VALUES ('project-invalid-canceled-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-invalid-canceled-migration",
		"workspace-invalid-canceled-migration",
		"550e8400-e29b-41d4-a716-446655440004",
		now,
	)
	seedWorkflowGraph(t, db, "project-invalid-canceled-migration", now)
	execSeed(t, db, "remove terminal graph facts", `
DELETE FROM workflow_edges
WHERE id = 'edge-done-1';
DELETE FROM workflow_transition_groups
WHERE id = 'group-done';
DELETE FROM workflow_nodes
WHERE id = 'node-done'`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-invalid-canceled-migration", "link-1", 1, "CAN-4", now, now)
	execSeed(t, db, "canceled task", `
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-invalid-canceled-migration'`, now+1)
	execSeed(t, db, "agent placement", workflowSeedPlacementSQL, "placement-invalid-canceled-migration", "task-invalid-canceled-migration", "node-agent", now, now)
	execSeed(t, db, "agent run", `
INSERT INTO task_runs (
    id, placement_id, session_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'run-invalid-canceled-migration',
    'placement-invalid-canceled-migration',
    '550e8400-e29b-41d4-a716-446655440004',
    1,
    ?,
    ?
)`, now, now+2)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var taskCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM tasks
WHERE id = 'task-invalid-canceled-migration'`).Scan(&taskCount); err != nil {
		t.Fatalf("count omitted invalid canceled task: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("invalid canceled task count = %d, want 0", taskCount)
	}

	var taskID sql.NullString
	var associationCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    session.task_id,
    (SELECT COUNT(*) FROM session_workflow_node_associations WHERE session_id = session.id)
FROM sessions session
WHERE session.id = '550e8400-e29b-41d4-a716-446655440004'`).Scan(&taskID, &associationCount); err != nil {
		t.Fatalf("query neutral retained session: %v", err)
	}
	if taskID.Valid || associationCount != 0 {
		t.Fatalf("neutral retained session = task_id=%+v associations=%d", taskID, associationCount)
	}
}

func TestOpenRejectsSessionAssociatedWithSeveralRetainedTasks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	const sessionID = "550e8400-e29b-41d4-a716-446655440005"
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-shared-session-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-shared-session-migration",
		"workspace-shared-session-migration",
		sessionID,
		now,
	)
	seedWorkflowGraph(t, db, "project-shared-session-migration", now)
	for _, task := range []struct {
		id          string
		seq         int
		shortID     string
		placementID string
		runID       string
	}{
		{"task-shared-session-one", 1, "SES-2", "placement-shared-session-one", "run-shared-session-one"},
		{"task-shared-session-two", 2, "SES-3", "placement-shared-session-two", "run-shared-session-two"},
	} {
		execSeed(t, db, "task", workflowSeedTaskSQL, task.id, "link-1", task.seq, task.shortID, now, now)
		execSeed(t, db, "agent placement", workflowSeedPlacementSQL, task.placementID, task.id, "node-agent", now, now)
		execSeed(t, db, "agent run", `
INSERT INTO task_runs (
    id, placement_id, session_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, ?, 1, ?, ?)`, task.runID, task.placementID, sessionID, now, now+int64(task.seq))
		seedLegacyExecutableCurrentNodeEnteringEdge(t, db, task.id, task.placementID, now)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	_, err = Open(root)
	if err == nil {
		t.Fatal("Open unexpectedly accepted a Session linked to several retained Tasks")
	}
	if !strings.Contains(err.Error(), sessionID) || !strings.Contains(err.Error(), "retained_task_count") {
		t.Fatalf("Open error = %v, want Session ownership diagnostic", err)
	}
}

func TestOpenRejectsSeveralUnfinishedRunsForOneCurrentNode(t *testing.T) {
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
VALUES ('project-duplicate-run-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-duplicate-run-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-duplicate-run-migration", "link-1", 1, "RUN-1", now, now)
	execSeed(t, db, "agent placement", workflowSeedPlacementSQL, "placement-duplicate-run-migration", "task-duplicate-run-migration", "node-agent", now, now)
	execSeed(t, db, "unfinished runs", `
INSERT INTO task_runs (id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms)
VALUES
    ('run-duplicate-one', 'placement-duplicate-run-migration', 1, ?, ?),
    ('run-duplicate-two', 'placement-duplicate-run-migration', 1, ?, ?)`,
		now,
		now,
		now,
		now+1,
	)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	_, err = Open(root)
	if err == nil {
		t.Fatal("Open unexpectedly accepted several unfinished runs for one current node")
	}
	if !strings.Contains(err.Error(), "task-duplicate-run-migration") ||
		!strings.Contains(err.Error(), "unfinished_run_count=2") {
		t.Fatalf("Open error = %v, want current-node execution diagnostic", err)
	}
}
