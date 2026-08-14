package metadata

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSessionCommandAuthorityCutoverUpgradesReleasedSchema(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 80)
	if err != nil {
		t.Fatalf("open version 80 database: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-session-command-cutover', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-session-command-cutover",
		"workspace-session-command-cutover",
		"session-session-command-cutover",
		now,
	)
	seedWorkflowGraph(t, db, "project-session-command-cutover", now)
	execSeed(t, db, "task", workflowSeedTaskSQL,
		"task-session-command-cutover",
		"link-1",
		1,
		"CUT-1",
		now,
		now,
	)
	execSeed(t, db, "session owner", `
UPDATE sessions
SET task_id = 'task-session-command-cutover'
WHERE id = 'session-session-command-cutover'`)
	execSeed(t, db, "current node", `
INSERT INTO task_current_nodes (
    task_id,
    node_id,
    current_input_values_json,
    prior_node_values_json,
    scheduling_state
) VALUES (
    'task-session-command-cutover',
    'node-start',
    '{}',
    '{"transition_parameters":{}}',
    'ready'
)`)
	execSeed(t, db, "session association", `
INSERT INTO session_workflow_node_associations (
    session_id,
    node_id,
    associated_at_unix_ms
) VALUES (
    'session-session-command-cutover',
    'node-start',
    ?
)`, now)
	execSeed(t, db, "prompt history", `
INSERT INTO session_prompt_history_entries (
    session_id,
    source_id,
    text,
    created_at_unix_ms
) VALUES (
    'session-session-command-cutover',
    'request-session-command-cutover',
    'preserved prompt',
    ?
)`, now)

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 86); err != nil {
		t.Fatalf("advance released database to version 86: %v", err)
	}
	var releasedNodeID string
	if err := db.QueryRow(`
SELECT kent_graph_entity_id_text_v1(id)
FROM workflow_nodes
WHERE node_key = 'backlog'`).Scan(&releasedNodeID); err != nil {
		t.Fatalf("read released graph identity: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 86 database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("upgrade released database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, assertion := range []struct {
		table  string
		column string
	}{
		{table: "session_prompt_history_entries", column: "source_id"},
		{table: "session_workflow_node_associations", column: "task_id"},
		{table: "session_workflow_node_associations", column: "association_status"},
		{table: "session_workflow_node_associations", column: "source_session_id"},
		{table: "task_current_nodes", column: "continuation_source_kind"},
		{table: "task_current_nodes", column: "continuation_source_session_id"},
		{table: "task_current_nodes", column: "legacy_materialized"},
		{table: "task_active_fanout_branches", column: "continuation_source_kind"},
		{table: "task_active_fanout_branches", column: "continuation_source_session_id"},
		{table: "task_active_fanout_branches", column: "legacy_materialized"},
	} {
		if columnExists(t, store.db, assertion.table, assertion.column) {
			t.Fatalf("%s.%s survived the authority cutover", assertion.table, assertion.column)
		}
	}

	var nodeStorage, nodeID, currentNodeID, associationNodeID string
	if err := store.db.QueryRow(`
SELECT typeof(id), id
FROM workflow_nodes
WHERE node_key = 'backlog'`).Scan(&nodeStorage, &nodeID); err != nil {
		t.Fatalf("read upgraded Workflow Node: %v", err)
	}
	if err := store.db.QueryRow(`
SELECT node_id
FROM task_current_nodes
WHERE task_id = 'task-session-command-cutover'`).Scan(&currentNodeID); err != nil {
		t.Fatalf("read upgraded Current Node: %v", err)
	}
	if err := store.db.QueryRow(`
SELECT node_id
FROM session_workflow_node_associations
WHERE session_id = 'session-session-command-cutover'`).Scan(&associationNodeID); err != nil {
		t.Fatalf("read upgraded Session association: %v", err)
	}
	if nodeStorage != "text" ||
		nodeID != releasedNodeID ||
		currentNodeID != releasedNodeID ||
		associationNodeID != releasedNodeID {
		t.Fatalf(
			"upgraded graph identity = storage=%q node=%q current=%q association=%q, want text/%q",
			nodeStorage,
			nodeID,
			currentNodeID,
			associationNodeID,
			releasedNodeID,
		)
	}

	var promptText string
	if err := store.db.QueryRow(`
SELECT text
FROM session_prompt_history_entries
WHERE session_id = 'session-session-command-cutover'`).Scan(&promptText); err != nil {
		t.Fatalf("read preserved prompt history: %v", err)
	}
	if promptText != "preserved prompt" {
		t.Fatalf("preserved prompt = %q, want %q", promptText, "preserved prompt")
	}
	if _, err := store.RecordPromptHistoryEntry(t.Context(), PromptHistoryEntry{
		SessionID: "session-session-command-cutover",
		Text:      "new prompt",
		CreatedAt: time.UnixMilli(now + 1).UTC(),
	}); err != nil {
		t.Fatalf("record prompt after upgrade: %v", err)
	}

	var foreignKeyViolations int
	if err := store.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		t.Fatalf("foreign-key check: %v", err)
	}
	if foreignKeyViolations != 0 {
		t.Fatalf("foreign-key violations after authority cutover = %d", foreignKeyViolations)
	}
}
