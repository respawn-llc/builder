package metadata

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenCreatesTaskCommentCreatedIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("open metadata store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if !indexExists(t, store.db, "task_comments_task_created_idx") {
		t.Fatal("task_comments_task_created_idx should back the comment cursor ordering")
	}
}

func TestOpenMigratesLegacyEmptyParentSessionIDToNullPreviousSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 52)
	if err != nil {
		t.Fatalf("open test database at version 52: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-parent-null', 'Project', ?, ?, '{}')`, now, now)
	execSeed(t, db, "workspace", `
INSERT INTO workspaces (
    id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES ('workspace-parent-null', 'project-parent-null', '/workspace-parent-null', '{}', ?, ?)`, now, now)
	execSeed(t, db, "legacy session", `
INSERT INTO sessions (
    id, project_id, workspace_id, artifact_relpath, parent_session_id,
    created_at_unix_ms, updated_at_unix_ms
) VALUES ('session-parent-null', 'project-parent-null', 'workspace-parent-null', 'sessions/session-parent-null', '', ?, ?)`, now, now)
	execSeed(t, db, "legacy prompt history", `
INSERT INTO session_prompt_history_entries (session_id, source_id, text, created_at_unix_ms)
VALUES ('session-parent-null', 'prompt-1', 'legacy prompt', ?)`, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 52 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()

	var previousSessionID sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT previous_session_id
FROM sessions
WHERE id = 'session-parent-null'
`).Scan(&previousSessionID); err != nil {
		t.Fatalf("query migrated previous session id: %v", err)
	}
	if previousSessionID.Valid {
		t.Fatalf("migrated previous session id = %+v, want NULL absence", previousSessionID)
	}
	var promptText string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT text
FROM session_prompt_history_entries
WHERE session_id = 'session-parent-null'
`).Scan(&promptText); err != nil {
		t.Fatalf("query migrated prompt history: %v", err)
	}
	if promptText != "legacy prompt" {
		t.Fatalf("migrated prompt history = %q, want legacy prompt", promptText)
	}
}

func TestOpenMigratesLegacySessionParentToTypedProvenance(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 55)
	if err != nil {
		t.Fatalf("open test database at version 55: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-provenance', 'Project', ?, ?, '{}')`, now, now)
	execSeed(t, db, "workspace", `
INSERT INTO workspaces (
    id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES ('workspace-provenance', 'project-provenance', '/workspace-provenance', '{}', ?, ?)`, now, now)
	execSeed(t, db, "legacy sessions", `
INSERT INTO sessions (
    id, project_id, workspace_id, artifact_relpath, parent_session_id,
    created_at_unix_ms, updated_at_unix_ms
) VALUES
    ('session-with-parent', 'project-provenance', 'workspace-provenance', 'sessions/session-with-parent', 'legacy-parent', ?, ?),
    ('session-without-parent', 'project-provenance', 'workspace-provenance', 'sessions/session-without-parent', NULL, ?, ?),
    ('session-padded-parent', 'project-provenance', 'workspace-provenance', 'sessions/session-padded-parent', ' padded-parent ', ?, ?),
    ('session-traversal-parent', 'project-provenance', 'workspace-provenance', 'sessions/session-traversal-parent', '../legacy-parent', ?, ?),
    ('session-absolute-parent', 'project-provenance', 'workspace-provenance', 'sessions/session-absolute-parent', '/legacy-parent', ?, ?),
    ('session-uuidv4-parent', 'project-provenance', 'workspace-provenance', 'sessions/session-uuidv4-parent', '550e8400-e29b-41d4-a716-446655440000', ?, ?),
    ('session-uuidv1-parent', 'project-provenance', 'workspace-provenance', 'sessions/session-uuidv1-parent', '550e8400-e29b-11d4-a716-446655440000', ?, ?)`,
		now, now, now, now, now, now, now, now, now, now, now, now, now, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 55 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()

	rows, err := store.db.QueryContext(t.Context(), `
SELECT id, previous_session_id, parent_agent_session_id
FROM sessions
ORDER BY id`)
	if err != nil {
		t.Fatalf("query migrated provenance: %v", err)
	}
	defer func() { _ = rows.Close() }()
	type migratedRow struct {
		id          string
		previous    sql.NullString
		parentAgent sql.NullString
	}
	var migrated []migratedRow
	for rows.Next() {
		var row migratedRow
		if err := rows.Scan(&row.id, &row.previous, &row.parentAgent); err != nil {
			t.Fatalf("scan migrated provenance: %v", err)
		}
		migrated = append(migrated, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated provenance: %v", err)
	}
	if len(migrated) != 7 {
		t.Fatalf("migrated rows = %+v, want seven", migrated)
	}
	migratedByID := make(map[string]migratedRow, len(migrated))
	for _, row := range migrated {
		migratedByID[row.id] = row
	}
	withParent := migratedByID["session-with-parent"]
	if !withParent.previous.Valid || withParent.previous.String != "legacy-parent" ||
		withParent.parentAgent.Valid {
		t.Fatalf("session-with-parent provenance = %+v", withParent)
	}
	withoutParent := migratedByID["session-without-parent"]
	if withoutParent.previous.Valid || withoutParent.parentAgent.Valid {
		t.Fatalf("session-without-parent provenance = %+v", withoutParent)
	}
	for id, expected := range map[string]string{
		"session-padded-parent":    " padded-parent ",
		"session-traversal-parent": "../legacy-parent",
		"session-absolute-parent":  "/legacy-parent",
		"session-uuidv1-parent":    "550e8400-e29b-11d4-a716-446655440000",
	} {
		row := migratedByID[id]
		if !row.previous.Valid || row.previous.String != expected || row.parentAgent.Valid {
			t.Fatalf("legacy provenance for %s = %+v, want exact previous-session value %q", id, row, expected)
		}
		if _, err := store.ResolvePersistedSession(context.Background(), row.id); err == nil {
			t.Fatalf("ResolvePersistedSession(%s) accepted malformed preserved provenance", row.id)
		}
	}
	uuidV4 := migratedByID["session-uuidv4-parent"]
	if !uuidV4.previous.Valid || uuidV4.previous.String != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("session-uuidv4-parent provenance = %+v, want canonical UUIDv4", uuidV4)
	}

	var legacyColumnCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM pragma_table_info('sessions')
WHERE name = 'parent_session_id'`).Scan(&legacyColumnCount); err != nil {
		t.Fatalf("inspect sessions columns: %v", err)
	}
	if legacyColumnCount != 0 {
		t.Fatalf("parent_session_id column count = %d, want removed", legacyColumnCount)
	}
}

func TestOpenMigratesWorkflowScriptNodesWithRuntimeReferences(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 42)
	if err != nil {
		t.Fatalf("open test database at version 42: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json) VALUES ('project-script-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-script-migration", now)
	execSeed(t, db, "workflow task", workflowSeedTaskSQL, "task-script-migration", "link-1", 1, "SCR-1", now, now)
	execSeed(t, db, "workflow placement", workflowSeedPlacementSQL, "placement-script-migration", "task-script-migration", "node-start", now, now)
	execSeed(t, db, "workflow run", `INSERT INTO task_runs (id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms)
VALUES ('run-script-migration', 'placement-script-migration', 1, ?, ?)`, now, now)
	execSeed(t, db, "workflow transition", `INSERT INTO task_transitions (id, task_id, source_run_id, source_placement_id, transition_id, workflow_revision_seen, actor, state, output_values_json, created_at_unix_ms)
VALUES ('transition-script-migration', 'task-script-migration', 'run-script-migration', 'placement-script-migration', 'start', 1, 'system', 'applied', '{}', ?)`, now)
	execSeed(t, db, "workflow transition edge", `INSERT INTO task_transition_edges (id, task_transition_id, workflow_edge_id, edge_key, target_node_id, state, input_bindings_json, output_requirements_json)
VALUES ('transition-edge-script-migration', 'transition-script-migration', 'edge-start-1', 'start', 'node-agent', 'applied', '[]', '[]')`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 42 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if !columnExists(t, store.db, "workflow_nodes", "script_path") {
		t.Fatal("workflow_nodes.script_path should exist after migration")
	}
	var violations int
	if err := store.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatalf("query foreign key check: %v", err)
	}
	if violations != 0 {
		t.Fatalf("foreign key violations after workflow script node migration = %d, want 0", violations)
	}
}
