package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkflowContextFreshnessMigrationBackfillsHistoricalAssociationOwnershipWithoutChoosingCurrent(t *testing.T) {
	t.Parallel()
	const (
		projectID = "project-context-freshness"
		taskID    = "task-context-freshness"
	)
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 80)
	if err != nil {
		t.Fatalf("open version 80 metadata database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Context freshness', ?, ?, '{}')`, projectID, now, now)
	seedWorkflowGraph(t, db, projectID, now)
	execSeed(t, db, "Task", workflowSeedTaskSQL, taskID, "link-1", 1, "CTX-1", now, now)
	sessions := []struct {
		id           string
		workspaceID  string
		associatedAt int64
	}{
		{id: "session-z-older", workspaceID: "workspace-z-older", associatedAt: now},
		{id: "session-a-newer", workspaceID: "workspace-a-newer", associatedAt: now + 1},
	}
	for _, session := range sessions {
		seedLegacyWorkflowSession(t, db, projectID, session.workspaceID, session.id, now)
		execSeed(t, db, "Session Task owner", `
UPDATE sessions SET task_id = ? WHERE id = ?`, taskID, session.id)
		execSeed(t, db, "Session Workflow Node association", `
INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, 'node-agent', NULL, ?)`, session.id, session.associatedAt)
	}
	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 83); err != nil {
		t.Fatalf("apply Workflow context freshness migration: %v", err)
	}
	rows, err := db.Query(`
SELECT session_id, task_id, association_status, source_session_id
FROM session_workflow_node_associations
ORDER BY session_id`)
	if err != nil {
		t.Fatalf("read migrated associations: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var count int
	for rows.Next() {
		var sessionID, gotTaskID, status string
		var sourceSessionID sql.NullString
		if err := rows.Scan(&sessionID, &gotTaskID, &status, &sourceSessionID); err != nil {
			t.Fatalf("scan migrated association: %v", err)
		}
		if gotTaskID != taskID || status != "historical" || sourceSessionID.Valid {
			t.Fatalf(
				"migrated association %q = Task %q status %q source %v; want Task %q historical without proof",
				sessionID,
				gotTaskID,
				status,
				sourceSessionID,
				taskID,
			)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated associations: %v", err)
	}
	if count != len(sessions) {
		t.Fatalf("migrated associations = %d, want %d", count, len(sessions))
	}
	var currentCount int
	if err := db.QueryRow(`
SELECT COUNT(*)
FROM session_workflow_node_associations
WHERE association_status = 'current'`).Scan(&currentCount); err != nil {
		t.Fatalf("count migrated current associations: %v", err)
	}
	if currentCount != 0 {
		t.Fatalf("migration designated %d current associations, want zero", currentCount)
	}
}
func TestWorkflowContextFreshnessMigrationAppliesCurrentNodeKindMatrix(t *testing.T) {
	t.Parallel()
	const projectID = "project-context-source-matrix"
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 80)
	if err != nil {
		t.Fatalf("open version 80 metadata database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Context source matrix', ?, ?, '{}')`, projectID, now, now)
	seedWorkflowGraph(t, db, projectID, now)
	workflowID := workflowSeedID(t, db, "1")
	execSeed(t, db, "additional Workflow Nodes", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, script_path
) VALUES
    ('node-script-matrix', ?, 'script_matrix', 'script', 'Script', 'scripts/matrix'),
    ('node-join-matrix', ?, 'join_matrix', 'join', 'Join', NULL)`,
		workflowID,
		workflowID,
	)
	type matrixCase struct {
		name       string
		taskID     string
		nodeID     string
		sessionID  string
		wantKind   sql.NullString
		wantLegacy int64
	}
	cases := []matrixCase{
		{name: "start", taskID: "task-matrix-start", nodeID: "node-start", wantKind: sql.NullString{String: "absent", Valid: true}},
		{name: "agent with Session", taskID: "task-matrix-agent-session", nodeID: "node-agent", sessionID: "session-matrix-agent", wantLegacy: 1},
		{name: "agent without Session", taskID: "task-matrix-agent-fresh", nodeID: "node-agent", wantKind: sql.NullString{String: "deferred_self", Valid: true}},
		{name: "unbound retained Agent", taskID: "task-matrix-agent-retained", nodeID: "node-agent", wantLegacy: 1},
		{name: "Script", taskID: "task-matrix-script", nodeID: "node-script-matrix", wantLegacy: 1},
		{name: "Join", taskID: "task-matrix-join", nodeID: "node-join-matrix", wantLegacy: 1},
		{name: "Terminal", taskID: "task-matrix-terminal", nodeID: "node-done", wantKind: sql.NullString{String: "absent", Valid: true}},
	}
	for index, test := range cases {
		execSeed(t, db, "Task", workflowSeedTaskSQL, test.taskID, "link-1", int64(index+1), "MATRIX-"+test.name, now, now)
		if test.sessionID != "" {
			workspaceID := "workspace-matrix-agent"
			seedLegacyWorkflowSession(t, db, projectID, workspaceID, test.sessionID, now)
			execSeed(t, db, "Session Task owner", `
UPDATE sessions SET task_id = ? WHERE id = ?`, test.taskID, test.sessionID)
		}
		execSeed(t, db, "Current Node", `
INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id
) VALUES (?, ?, NULL, '{}', '{"transition_parameters":{}}', ?)`,
			test.taskID,
			test.nodeID,
			sql.NullString{String: test.sessionID, Valid: test.sessionID != ""},
		)
		if test.name == "unbound retained Agent" {
			execSeed(t, db, "retained Agent entering edge mode", `
UPDATE workflow_edges
SET context_mode = 'continue_session'
WHERE id = 'edge-start-1'`)
			execSeed(t, db, "retained Agent entering edge", `
UPDATE task_current_nodes
SET entered_by_edge_id = 'edge-start-1'
WHERE task_id = ?`, test.taskID)
		}
	}
	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 83); err != nil {
		t.Fatalf("apply Workflow context freshness migration: %v", err)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var sourceKind, sourceSessionID sql.NullString
			var legacyMaterialized int64
			if err := db.QueryRow(`
SELECT continuation_source_kind, continuation_source_session_id, legacy_materialized
FROM task_current_nodes
WHERE task_id = ?`, test.taskID).Scan(
				&sourceKind,
				&sourceSessionID,
				&legacyMaterialized,
			); err != nil {
				t.Fatalf("read migrated Current Node: %v", err)
			}
			if sourceKind != test.wantKind || sourceSessionID.Valid || legacyMaterialized != test.wantLegacy {
				t.Fatalf(
					"migrated source = kind %v Session %v legacy %d; want kind %v no Session legacy %d",
					sourceKind,
					sourceSessionID,
					legacyMaterialized,
					test.wantKind,
					test.wantLegacy,
				)
			}
		})
	}
}
func TestWorkflowContextFreshnessMigrationAppliesPendingApprovalTargetKindMatrix(t *testing.T) {
	t.Parallel()
	const projectID = "project-approval-source-matrix"
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 80)
	if err != nil {
		t.Fatalf("open version 80 metadata database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Approval source matrix', ?, ?, '{}')`, projectID, now, now)
	seedWorkflowGraph(t, db, projectID, now)
	workflowID := workflowSeedID(t, db, "1")
	execSeed(t, db, "additional Workflow Nodes", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, script_path
) VALUES
    ('node-script-approval', ?, 'script_approval', 'script', 'Script', 'scripts/approval'),
    ('node-join-approval', ?, 'join_approval', 'join', 'Join', NULL)`,
		workflowID,
		workflowID,
	)
	type matrixCase struct {
		name             string
		targetNodeID     string
		targetSessionID  string
		wantTargetIntent string
		wantSourceKind   string
	}
	cases := []matrixCase{
		{
			name:             "agent with Session",
			targetNodeID:     "node-agent",
			targetSessionID:  "session-approval-agent",
			wantTargetIntent: "reuse",
			wantSourceKind:   "legacy",
		},
		{
			name:             "agent without Session",
			targetNodeID:     "node-agent",
			wantTargetIntent: "create",
			wantSourceKind:   "deferred_self",
		},
		{
			name:             "unbound retained Agent",
			targetNodeID:     "node-agent",
			wantTargetIntent: "create",
			wantSourceKind:   "legacy",
		},
		{name: "Script", targetNodeID: "node-script-approval", wantTargetIntent: "no_agent", wantSourceKind: "legacy"},
		{name: "Join", targetNodeID: "node-join-approval", wantTargetIntent: "no_agent", wantSourceKind: "legacy"},
		{name: "Terminal", targetNodeID: "node-done", wantTargetIntent: "no_agent", wantSourceKind: "absent"},
	}
	for index, test := range cases {
		taskID := "task-approval-matrix-" + string(rune('a'+index))
		approvalID := "approval-matrix-" + string(rune('a'+index))
		execSeed(t, db, "Task", workflowSeedTaskSQL, taskID, "link-1", int64(index+1), "APPROVAL-"+string(rune('A'+index)), now, now)
		execSeed(t, db, "source Current Node", insertTaskCurrentNodeSQL, taskID, "node-start", nil)
		if test.targetSessionID != "" {
			workspaceID := "workspace-approval-agent"
			seedLegacyWorkflowSession(t, db, projectID, workspaceID, test.targetSessionID, now)
			execSeed(t, db, "target Session Task owner", `
UPDATE sessions SET task_id = ? WHERE id = ?`, taskID, test.targetSessionID)
		}
		execSeed(t, db, "pending Approval", insertTaskPendingApprovalSQL, approvalID, taskID, "node-start", nil, now)
		execSeed(t, db, "pending Approval branch", `
INSERT INTO task_pending_approval_branches (
    approval_id,
    transition_branch_key,
    target_snapshot_json,
    effective_edge_configuration_json,
    context_source_resolution_json
) VALUES (
    ?,
    'target',
    json_object(
        'node_id', ?,
        'display_name', 'Target',
        'current_input_values', json('{}'),
        'prior_values', json('{"transition_parameters":{}}'),
        'session_id', ?
    ),
    '{}',
    json_object('session_id', ?)
)`,
			approvalID,
			test.targetNodeID,
			sql.NullString{String: test.targetSessionID, Valid: test.targetSessionID != ""},
			sql.NullString{String: test.targetSessionID, Valid: test.targetSessionID != ""},
		)
		if test.name == "agent without Session" {
			execSeed(t, db, "fresh pending Approval context mode", `
UPDATE task_pending_approval_branches
SET effective_edge_configuration_json = json_object('context_mode', 'new_session')
WHERE approval_id = ?`, approvalID)
		} else if test.name == "unbound retained Agent" {
			execSeed(t, db, "retained pending Approval context mode", `
UPDATE task_pending_approval_branches
SET effective_edge_configuration_json = json_object('context_mode', 'continue_session')
WHERE approval_id = ?`, approvalID)
		}
	}
	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 83); err != nil {
		t.Fatalf("apply Workflow context freshness migration: %v", err)
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			approvalID := "approval-matrix-" + string(rune('a'+index))
			var targetIntent, sourceKind string
			var targetSessionID sql.NullString
			if err := db.QueryRow(`
SELECT
    json_extract(context_source_resolution_json, '$.target_session.kind'),
    json_extract(context_source_resolution_json, '$.target_session.session_id'),
    json_extract(context_source_resolution_json, '$.active_source.kind')
FROM task_pending_approval_branches
WHERE approval_id = ?`, approvalID).Scan(
				&targetIntent,
				&targetSessionID,
				&sourceKind,
			); err != nil {
				t.Fatalf("read migrated pending Approval source: %v", err)
			}
			if targetIntent != test.wantTargetIntent ||
				sourceKind != test.wantSourceKind ||
				targetSessionID.String != test.targetSessionID ||
				targetSessionID.Valid != (test.targetSessionID != "") {
				t.Fatalf(
					"migrated resolution = intent %q Session %v source %q; want intent %q Session %q source %q",
					targetIntent,
					targetSessionID,
					sourceKind,
					test.wantTargetIntent,
					test.targetSessionID,
					test.wantSourceKind,
				)
			}
		})
	}
}
func TestWorkflowContextFreshnessMigrationMarksPendingAndArrivedFanoutBranchesLegacy(t *testing.T) {
	t.Parallel()
	const (
		projectID = "project-fanout-source-migration"
		taskID    = "task-fanout-source-migration"
	)
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 80)
	if err != nil {
		t.Fatalf("open version 80 metadata database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Fanout source migration', ?, ?, '{}')`, projectID, now, now)
	seedWorkflowGraph(t, db, projectID, now)
	execSeed(t, db, "Task", workflowSeedTaskSQL, taskID, "link-1", 1, "FANOUT-1", now, now)
	execSeed(t, db, "active Fan-Out", `
INSERT INTO task_active_fanouts (task_id) VALUES (?)`, taskID)
	execSeed(t, db, "active Fan-Out branches", `
INSERT INTO task_active_fanout_branches (
    task_id, transition_branch_key, arrival_state, arrival_values_json
) VALUES
    (?, 'pending', 'pending', NULL),
    (?, 'arrived', 'arrived', '{}')`, taskID, taskID)
	execSeed(t, db, "pending branch Current Node", insertTaskCurrentNodeSQL, taskID, "node-agent", "pending")
	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 83); err != nil {
		t.Fatalf("apply Workflow context freshness migration: %v", err)
	}
	rows, err := db.Query(`
SELECT
    transition_branch_key,
    continuation_source_kind,
    continuation_source_session_id,
    legacy_materialized
FROM task_active_fanout_branches
WHERE task_id = ?
ORDER BY transition_branch_key`, taskID)
	if err != nil {
		t.Fatalf("read migrated Fan-Out branches: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var count int
	for rows.Next() {
		var branchKey string
		var sourceKind, sourceSessionID sql.NullString
		var legacyMaterialized int64
		if err := rows.Scan(&branchKey, &sourceKind, &sourceSessionID, &legacyMaterialized); err != nil {
			t.Fatalf("scan migrated Fan-Out branch: %v", err)
		}
		if sourceKind.Valid || sourceSessionID.Valid || legacyMaterialized != 1 {
			t.Fatalf(
				"migrated Fan-Out branch %q = kind %v Session %v legacy %d; want proofless legacy",
				branchKey,
				sourceKind,
				sourceSessionID,
				legacyMaterialized,
			)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated Fan-Out branches: %v", err)
	}
	if count != 2 {
		t.Fatalf("migrated Fan-Out branches = %d, want 2", count)
	}
}
