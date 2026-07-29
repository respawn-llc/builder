package metadata

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesCommentsToMinimalStorage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 19)
	if err != nil {
		t.Fatalf("open test database at version 19: %v", err)
	}
	if _, err := db.Exec(metadataDBTestSQL(t, "version19_minimal_storage.sql")); err != nil {
		t.Fatalf("seed version 19 minimal storage data: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 19 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()
	for _, column := range []string{"source_run_id", "deleted_at_unix_ms", "metadata_json"} {
		if columnExists(t, store.db, "task_comments", column) {
			t.Fatalf("task_comments.%s should have been removed", column)
		}
	}
	comments, err := store.DB().QueryContext(t.Context(), `SELECT id, body FROM task_comments ORDER BY updated_at_unix_ms DESC`)
	if err != nil {
		t.Fatalf("query migrated comments: %v", err)
	}
	defer func() { _ = comments.Close() }()
	if !comments.Next() {
		t.Fatal("expected one visible comment after migration")
	}
	var commentID, body string
	if err := comments.Scan(&commentID, &body); err != nil {
		t.Fatalf("scan migrated comment: %v", err)
	}
	if commentID != "comment-visible" || body != "visible" {
		t.Fatalf("migrated comment = %q/%q, want visible comment", commentID, body)
	}
	if comments.Next() {
		t.Fatal("deleted comment should not survive hard-delete migration")
	}
	if err := comments.Err(); err != nil {
		t.Fatalf("iterate migrated comments: %v", err)
	}
}

func TestOpenProjectsLegacyBacklogPlacementToCurrentNode(t *testing.T) {
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
VALUES ('project-current-node-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-current-node-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-current-node-migration", "link-1", 1, "CUR-1", now, now)
	execSeed(t, db, "backlog placement", workflowSeedPlacementSQL, "placement-current-node-migration", "task-current-node-migration", "node-start", now, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	var branchKey, schedulingState, sessionID sql.NullString
	var currentInputs, priorNodeValues string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    node_id,
    transition_branch_key,
    scheduling_state,
    session_id,
    current_input_values_json,
    prior_node_values_json
FROM task_current_nodes
WHERE task_id = 'task-current-node-migration'`).Scan(
		&nodeID,
		&branchKey,
		&schedulingState,
		&sessionID,
		&currentInputs,
		&priorNodeValues,
	); err != nil {
		t.Fatalf("query projected backlog current node: %v", err)
	}
	if nodeID != "node-start" ||
		branchKey.Valid ||
		schedulingState.Valid ||
		sessionID.Valid ||
		currentInputs != "{}" ||
		priorNodeValues != "{}" {
		t.Fatalf(
			"projected backlog current node = node=%q branch=%+v scheduling=%+v session=%+v inputs=%q prior=%q, want serial unbound backlog state",
			nodeID,
			branchKey,
			schedulingState,
			sessionID,
			currentInputs,
			priorNodeValues,
		)
	}
	assertLegacyWorkflowHistoryDropped(t, store.db)
}

func assertLegacyWorkflowHistoryDropped(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, relation := range []string{
		"task_node_placements",
		"task_runs",
		"task_transitions",
		"task_transition_edges",
		"task_node_placement_records",
		"task_run_records",
		"task_transition_records",
		"task_transition_edge_records",
		"workflow_task_current_run_records",
		"workflow_attention_candidates",
	} {
		if tableExists(t, db, relation) || viewExists(t, db, relation) {
			t.Fatalf("hard cutover retained legacy workflow relation %q", relation)
		}
	}
	if _, err := db.Exec(`SELECT * FROM task_runs`); err == nil {
		t.Fatal("hard cutover left legacy task_runs queryable")
	}
}

func TestOpenProjectsLegacyTerminalPlacementToCurrentNode(t *testing.T) {
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
VALUES ('project-terminal-current-node-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-terminal-current-node-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-terminal-current-node-migration", "link-1", 1, "TER-1", now, now)
	execSeed(t, db, "terminal placement", workflowSeedPlacementSQL, "placement-terminal-current-node-migration", "task-terminal-current-node-migration", "node-done", now, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	var schedulingState, sessionID sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT node_id, scheduling_state, session_id
FROM task_current_nodes
WHERE task_id = 'task-terminal-current-node-migration'`).Scan(&nodeID, &schedulingState, &sessionID); err != nil {
		t.Fatalf("query projected terminal current node: %v", err)
	}
	if nodeID != "node-done" || schedulingState.Valid || sessionID.Valid {
		t.Fatalf("projected terminal current node = node=%q scheduling=%+v session=%+v, want terminal without executable state", nodeID, schedulingState, sessionID)
	}
}

func TestOpenProjectsLatestActiveSerialTerminalPlacement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 59)
	if err != nil {
		t.Fatalf("open version 59 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-duplicate-serial-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-duplicate-serial-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-duplicate-serial-migration", "link-1", 1, "SER-1", now, now)
	execSeed(t, db, "older terminal placement", workflowSeedPlacementSQL, "placement-duplicate-serial-older", "task-duplicate-serial-migration", "node-done", now, now+1)
	execSeed(t, db, "newer terminal placement", workflowSeedPlacementSQL, "placement-duplicate-serial-newer", "task-duplicate-serial-migration", "node-done", now+1, now+1)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 59 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var currentNodeCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM task_current_nodes
WHERE task_id = 'task-duplicate-serial-migration'`).Scan(&currentNodeCount); err != nil {
		t.Fatalf("query normalized serial current node: %v", err)
	}
	if currentNodeCount != 1 {
		t.Fatalf("normalized serial current node count = %d, want singleton", currentNodeCount)
	}
}

func TestOpenProjectsLegacyActiveAgentRunToInterruptedCurrentNode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	runUpdatedAt := now + 7
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-active-agent-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-active-agent-migration",
		"workspace-active-agent-migration",
		"550e8400-e29b-41d4-a716-446655440000",
		now,
	)
	seedWorkflowGraph(t, db, "project-active-agent-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-active-agent-migration", "link-1", 1, "AGT-1", now, now)
	execSeed(t, db, "agent placement", workflowSeedPlacementSQL, "placement-active-agent-migration", "task-active-agent-migration", "node-agent", now, now)
	execSeed(t, db, "active agent run", `
INSERT INTO task_runs (
    id,
    placement_id,
    session_id,
    workflow_revision_seen,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms
) VALUES (
    'run-active-agent-migration',
    'placement-active-agent-migration',
    '550e8400-e29b-41d4-a716-446655440000',
    1,
    ?,
    ?,
    ?
)`, now, runUpdatedAt, now+1)
	execSeed(t, db, "legacy workflow session metadata", `
UPDATE sessions
SET metadata_json = json_object(
    'workflow_session',
    json_object(
        'run_id', 'run-stale-agent-migration',
        'task_id', 'task-stale-agent-migration',
        'workflow_id', 'workflow-stale-agent-migration'
    )
)
WHERE id = '550e8400-e29b-41d4-a716-446655440000'`)
	seedLegacyExecutableCurrentNodeEnteringEdge(t, db, "task-active-agent-migration", "placement-active-agent-migration", now)
	execSeed(t, db, "materialized active agent inputs", `
UPDATE task_transitions
SET commentary = 'entry note',
    output_values_json = '{"summary":"from transition"}'
WHERE id = 'entry-transition-placement-active-agent-migration';
UPDATE task_transition_edges
SET input_bindings_json = '[
    {"name":"summary","source":"transition_output","field":"summary"},
    {"name":"task_title","source":"task","field":"title"},
    {"name":"note","source":"transition_output","field":"commentary"}
]'
WHERE id = 'entry-edge-placement-active-agent-migration'`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var (
		nodeID, currentInputs, priorNodeValues, schedulingState, interruptionReason, interruptionDetail, sessionID string
		interruptedAt                                                                                              int64
	)
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    node_id,
    current_input_values_json,
    prior_node_values_json,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms,
    session_id
FROM task_current_nodes
WHERE task_id = 'task-active-agent-migration'`).Scan(
		&nodeID,
		&currentInputs,
		&priorNodeValues,
		&schedulingState,
		&interruptionReason,
		&interruptionDetail,
		&interruptedAt,
		&sessionID,
	); err != nil {
		t.Fatalf("query projected active agent current node: %v", err)
	}
	if nodeID != "node-agent" ||
		currentInputs != `{"note":"entry note","summary":"from transition","task_title":"Task"}` ||
		priorNodeValues != "{}" ||
		schedulingState != "interrupted" ||
		interruptionReason != "server_restart" ||
		interruptionDetail != `{"code":"workflow.execution.restarted","fields":{"operation":"recovery"}}` ||
		interruptedAt != runUpdatedAt ||
		sessionID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf(
			"projected active agent current node = node=%q inputs=%q prior=%q scheduling=%q reason=%q detail=%q interrupted_at=%d session=%q",
			nodeID,
			currentInputs,
			priorNodeValues,
			schedulingState,
			interruptionReason,
			interruptionDetail,
			interruptedAt,
			sessionID,
		)
	}

	var taskID, associationNodeID string
	var associatedAt int64
	if err := store.db.QueryRowContext(t.Context(), `
SELECT session.task_id, association.node_id, association.associated_at_unix_ms
FROM sessions session
JOIN session_workflow_node_associations association ON association.session_id = session.id
WHERE session.id = '550e8400-e29b-41d4-a716-446655440000'`).Scan(&taskID, &associationNodeID, &associatedAt); err != nil {
		t.Fatalf("query projected active agent session ownership: %v", err)
	}
	if taskID != "task-active-agent-migration" || associationNodeID != "node-agent" || associatedAt != runUpdatedAt {
		t.Fatalf("projected active agent session = task=%q node=%q associated_at=%d", taskID, associationNodeID, associatedAt)
	}
	var legacyWorkflowMetadataType sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT json_type(metadata_json, '$.workflow_session')
FROM sessions
WHERE id = '550e8400-e29b-41d4-a716-446655440000'`).Scan(&legacyWorkflowMetadataType); err != nil {
		t.Fatalf("query migrated workflow session metadata: %v", err)
	}
	if legacyWorkflowMetadataType.Valid {
		t.Fatalf("migrated workflow session metadata type = %q, want absent", legacyWorkflowMetadataType.String)
	}
}

func TestOpenRejectsMalformedSessionMetadataWithSessionContext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	const sessionID = "550e8400-e29b-41d4-a716-446655440099"
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-malformed-session-metadata', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-malformed-session-metadata",
		"workspace-malformed-session-metadata",
		sessionID,
		now,
	)
	execSeed(t, db, "malformed Session metadata", `
UPDATE sessions
SET metadata_json = '{"retained":'
WHERE id = ?`, sessionID)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	_, err = Open(root)
	if err == nil {
		t.Fatal("Open unexpectedly erased malformed Session metadata")
	}
	if !strings.Contains(err.Error(), sessionID) ||
		!strings.Contains(err.Error(), "metadata_json is malformed") {
		t.Fatalf("Open error = %v, want Session metadata diagnostic", err)
	}
}
