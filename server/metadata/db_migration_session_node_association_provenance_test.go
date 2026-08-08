package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionNodeAssociationProvenanceMigrationPreservesLiveUpgradeRows(t *testing.T) {
	t.Parallel()

	const (
		projectID      = "project-session-node-provenance"
		taskID         = "task-session-node-provenance"
		sessionID      = "session-session-node-provenance"
		workspaceID    = "workspace-session-node-provenance"
		historicalNode = "f21331de-fb79-48fe-8b14-c2c8efbdd98e"
	)

	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 70)
	if err != nil {
		t.Fatalf("open version 70 metadata database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Session node provenance', ?, ?, '{}')`, projectID, now, now)
	seedWorkflowGraph(t, db, projectID, now)
	workflowID := workflowSeedID(t, db, "1")
	execSeed(t, db, "historical Workflow Node", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role
) VALUES (?, ?, 'historical_agent', 'agent', 'Historical Agent', 'coder')`,
		historicalNode,
		workflowID,
	)
	execSeed(t, db, "Task", workflowSeedTaskSQL, taskID, "link-1", 1, "SNP-1", now, now)
	execSeed(t, db, "terminal Current Node", insertTaskCurrentNodeSQL, taskID, "node-done", nil)
	seedLegacyWorkflowSession(t, db, projectID, workspaceID, sessionID, now)
	execSeed(t, db, "Session Task owner", `
UPDATE sessions
SET task_id = ?
WHERE id = ?`, taskID, sessionID)
	execSeed(t, db, "historical Session Workflow Node association", `
INSERT INTO session_workflow_node_associations (
    session_id, node_id, transition_branch_key, associated_at_unix_ms
) VALUES (?, ?, NULL, ?)`, sessionID, historicalNode, now)

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 71); err != nil {
		t.Fatalf("apply Session node provenance migration: %v", err)
	}

	assertSessionNodeAssociationProvenance(t, db, sessionID, historicalNode, taskID)

	deleted, err := db.Exec(`DELETE FROM workflow_nodes WHERE id = ?`, historicalNode)
	if err != nil {
		t.Fatalf("delete non-current historical Workflow Node: %v", err)
	}
	deletedRows, err := deleted.RowsAffected()
	if err != nil {
		t.Fatalf("read deleted Workflow Node count: %v", err)
	}
	if deletedRows != 1 {
		t.Fatalf("deleted historical Workflow Nodes = %d, want 1", deletedRows)
	}

	assertSessionNodeAssociationProvenance(t, db, sessionID, historicalNode, taskID)

	var currentNodeRestrictiveForeignKeys int
	if err := db.QueryRow(`
SELECT COUNT(*)
FROM pragma_foreign_key_list('task_current_nodes')
WHERE "from" = 'node_id'
  AND "table" = 'workflow_nodes'
  AND on_delete = 'RESTRICT'`).Scan(&currentNodeRestrictiveForeignKeys); err != nil {
		t.Fatalf("inspect task_current_nodes.node_id foreign key: %v", err)
	}
	if currentNodeRestrictiveForeignKeys != 1 {
		t.Fatalf("restrictive task_current_nodes.node_id foreign keys = %d, want 1", currentNodeRestrictiveForeignKeys)
	}

	var foreignKeyViolations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	if foreignKeyViolations != 0 {
		t.Fatalf("foreign key violations = %d, want 0", foreignKeyViolations)
	}
}

func assertSessionNodeAssociationProvenance(
	t *testing.T,
	db *sql.DB,
	sessionID string,
	nodeID string,
	taskID string,
) {
	t.Helper()

	var gotNodeID, gotTaskID string
	if err := db.QueryRow(`
SELECT association.node_id, session.task_id
FROM session_workflow_node_associations association
JOIN sessions session ON session.id = association.session_id
WHERE association.session_id = ?`, sessionID).Scan(&gotNodeID, &gotTaskID); err != nil {
		t.Fatalf("read Session node association provenance: %v", err)
	}
	if gotNodeID != nodeID || gotTaskID != taskID {
		t.Fatalf(
			"Session node association provenance = node %q Task %q, want node %q Task %q",
			gotNodeID,
			gotTaskID,
			nodeID,
			taskID,
		)
	}
}
