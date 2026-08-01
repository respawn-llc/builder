package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestWorkflowTransitionKeyMigrationRepairsPersistedDuplicateKeys(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 62)
	if err != nil {
		t.Fatalf("open version 62 database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const workflowID = "workflow-duplicate-transition-keys"
	const now = int64(1_700_000_000_000)
	execMigrationTransitionKeySeed(t, db, "workflow", `
INSERT INTO workflows (id, name, description, version, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, 'Duplicate Transition Workflow', '', 1, ?, ?)`, workflowID, now, now)
	execMigrationTransitionKeySeed(t, db, "nodes", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role, prompt_template,
    output_fields_json, sort_order, input_fields_json, join_input_providers_json, completion_mode
)
VALUES
    ('node-start', ?, 'backlog', 'start', 'Backlog', '', '', '[]', 0, '[]', '[]', ''),
    ('node-a', ?, 'branch_a', 'agent', 'Branch A', 'coder', 'A.', '[]', 1, '[]', '[]', ''),
    ('node-b', ?, 'branch_b', 'agent', 'Branch B', 'coder', 'B.', '[]', 2, '[]', '[]', '')`,
		workflowID, workflowID, workflowID)
	execMigrationTransitionKeySeed(t, db, "transition groups", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name, sort_order, description)
VALUES
    ('group-start', 'node-start', 'start', 'Start', 0, ''),
    ('group-a', 'node-a', 'join', 'Join', 1, ''),
    ('group-b', 'node-b', 'join', 'Join', 2, '')`)

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 63); err != nil {
		t.Fatalf("apply transition-key migration: %v", err)
	}

	rows, err := db.Query(`
SELECT transition_group.id, transition_group.transition_id
FROM workflow_transition_groups transition_group
JOIN workflow_nodes source_node
    ON source_node.id = transition_group.source_node_id
WHERE source_node.workflow_id = ?
ORDER BY transition_group.id`, workflowID)
	if err != nil {
		t.Fatalf("query repaired transition groups: %v", err)
	}
	defer func() { _ = rows.Close() }()

	got := map[string]string{}
	for rows.Next() {
		var id string
		var transitionID string
		if err := rows.Scan(&id, &transitionID); err != nil {
			t.Fatalf("scan repaired transition group: %v", err)
		}
		got[id] = transitionID
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate repaired transition groups: %v", err)
	}
	if got["group-a"] != "join" || got["group-b"] != "join_branch_2" {
		t.Fatalf("repaired transition groups = %+v, want group-a=join and group-b=join_branch_2", got)
	}

	var duplicateCount int
	if err := db.QueryRow(`
SELECT COUNT(*)
FROM (
    SELECT transition_group.transition_id
    FROM workflow_transition_groups transition_group
    JOIN workflow_nodes source_node
        ON source_node.id = transition_group.source_node_id
    WHERE source_node.workflow_id = ?
    GROUP BY transition_group.transition_id
    HAVING COUNT(*) > 1
)`, workflowID).Scan(&duplicateCount); err != nil {
		t.Fatalf("duplicate transition query: %v", err)
	}
	if duplicateCount != 0 {
		t.Fatalf("duplicate transition groups = %d, want none", duplicateCount)
	}
}

func execMigrationTransitionKeySeed(t *testing.T, db *sql.DB, label string, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("seed transition-key migration fixture %s: %v", label, err)
	}
}
