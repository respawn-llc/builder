package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestOpenDropsPersistedWorkflowEvents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 20)
	if err != nil {
		t.Fatalf("open test database at version 20: %v", err)
	}
	if _, err := db.Exec(metadataDBTestSQL(t, "version20_workflow_events.sql")); err != nil {
		t.Fatalf("seed workflow events: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 20 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if tableExists(t, store.db, "workflow_events") {
		t.Fatal("workflow_events should have been dropped")
	}
}

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

func TestOpenRemovesSystemTaskCommentAuthorKind(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 32)
	if err != nil {
		t.Fatalf("open test database at version 32: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms) VALUES ('project-system-comment', 'Project', ?, ?)`, now, now)
	seedWorkflowGraph(t, db, "project-system-comment", now)
	execSeed(t, db, "workflow task", workflowSeedTaskSQL, "task-system-comment", "link-1", 1, "SYS-1", now, now)
	execSeed(t, db, "workflow placement", workflowSeedPlacementSQL, "placement-system-comment", "task-system-comment", "node-start", now, now)
	if _, err := db.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatalf("enable check constraint bypass: %v", err)
	}
	execSeed(t, db, "legacy system comment", `INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms) VALUES ('comment-system', 'task-system-comment', 'legacy system note', 'system', '', ?, ?)`, now, now)
	if _, err := db.Exec(`PRAGMA ignore_check_constraints = OFF`); err != nil {
		t.Fatalf("disable check constraint bypass: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 32 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()
	var authorKind, authorID string
	if err := store.db.QueryRowContext(t.Context(), `SELECT author_kind, author_id FROM task_comments WHERE id = 'comment-system'`).Scan(&authorKind, &authorID); err != nil {
		t.Fatalf("query migrated system comment: %v", err)
	}
	if authorKind != "agent" || authorID != "system" {
		t.Fatalf("migrated system comment author = %q/%q, want agent/system", authorKind, authorID)
	}
	if !indexExists(t, store.db, "task_comments_task_updated_idx") {
		t.Fatal("task_comments_task_updated_idx should be recreated after rebuilding task_comments")
	}
	assertSQLiteConstraint(t, store.db, sqlite3.SQLITE_CONSTRAINT_CHECK, `INSERT INTO task_comments (id, task_id, body, author_kind, created_at_unix_ms, updated_at_unix_ms) VALUES ('comment-system-rejected', 'task-system-comment', 'bad', 'system', 1, 1)`)
}

func TestOpenRemovesRedundantIndexesAndArchiveMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 21)
	if err != nil {
		t.Fatalf("open test database at version 21: %v", err)
	}
	if _, err := db.Exec(metadataDBTestSQL(t, "version21_archive_metadata.sql")); err != nil {
		t.Fatalf("seed archive metadata: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 21 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()
	for _, index := range []string{
		"runtime_leases_session_idx",
		"workspaces_project_idx",
		"workflow_transition_groups_source_transition_idx",
		"tasks_project_short_id_idx",
	} {
		if indexExists(t, store.db, index) {
			t.Fatalf("index %s should have been dropped", index)
		}
	}
	if columnExists(t, store.db, "workflow_nodes", "metadata_json") {
		t.Fatal("workflow_nodes.metadata_json should have been removed by workflow definition metadata migration")
	}
}

func TestOpenRejectsInconsistentWorkflowGraphDenormalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seed string
	}{
		{
			name: "transition group workflow disagrees with source node",
			seed: `INSERT INTO workflow_transition_groups (id, workflow_id, source_node_id, transition_id, display_name)
VALUES ('group-bad', 'workflow-550e8400-e29b-41d4-a716-44665544000b', 'node-a', 'bad', 'Bad');`,
		},
		{
			name: "edge workflow disagrees with transition group source node",
			seed: `
INSERT INTO workflow_transition_groups (id, workflow_id, source_node_id, transition_id, display_name)
VALUES ('group-a', 'workflow-550e8400-e29b-41d4-a716-44665544000a', 'node-a', 'next', 'Next');
INSERT INTO workflow_edges (id, workflow_id, transition_group_id, edge_key, target_node_id, context_mode, input_bindings_json, output_requirements_json)
VALUES ('edge-bad', 'workflow-550e8400-e29b-41d4-a716-44665544000b', 'group-a', 'next', 'node-a', 'new_session', '{}', '{}');`,
		},
		{
			name: "edge target node belongs to different workflow",
			seed: `
INSERT INTO workflow_transition_groups (id, workflow_id, source_node_id, transition_id, display_name)
VALUES ('group-a', 'workflow-550e8400-e29b-41d4-a716-44665544000a', 'node-a', 'next', 'Next');
INSERT INTO workflow_edges (id, workflow_id, transition_group_id, edge_key, target_node_id, context_mode, input_bindings_json, output_requirements_json)
VALUES ('edge-bad', 'workflow-550e8400-e29b-41d4-a716-44665544000a', 'group-a', 'next', 'node-b', 'new_session', '{}', '{}');`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "db", "main.sqlite3")
			db, err := openDatabaseAtVersionForTest(t, root, dbPath, 23)
			if err != nil {
				t.Fatalf("open test database at version 23: %v", err)
			}
			if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
				t.Fatalf("disable foreign keys: %v", err)
			}
			if _, err := db.Exec(`
INSERT INTO workflows (id, name, description, graph_revision, created_at_unix_ms, updated_at_unix_ms)
VALUES ('workflow-550e8400-e29b-41d4-a716-44665544000a', 'A', '', 1, 1, 1),
       ('workflow-550e8400-e29b-41d4-a716-44665544000b', 'B', '', 1, 1, 1);
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, output_fields_json)
VALUES ('node-a', 'workflow-550e8400-e29b-41d4-a716-44665544000a', 'start', 'start', 'Start A', '[]'),
       ('node-b', 'workflow-550e8400-e29b-41d4-a716-44665544000b', 'done', 'terminal', 'Done B', '[]');
`); err != nil {
				t.Fatalf("seed version 23 graph base: %v", err)
			}
			if _, err := db.Exec(tt.seed); err != nil {
				t.Fatalf("seed version 23 contradiction: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close version 23 db: %v", err)
			}

			if store, err := Open(root); err == nil {
				_ = store.Close()
				t.Fatal("expected migration to reject inconsistent workflow graph denormalization")
			}
		})
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

func TestOpenMigratesWorkspaceHistorySnapshots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 8)
	if err != nil {
		t.Fatalf("open test database at version 8: %v", err)
	}
	if _, err := db.Exec(metadataDBTestSQL(t, "version8_workspace_history.sql")); err != nil {
		t.Fatalf("seed version 8 workspace history: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 8 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()
	record, err := store.ResolvePersistedSession(t.Context(), "session-1")
	if err != nil {
		t.Fatalf("ResolvePersistedSession after migration: %v", err)
	}
	if record.Meta.WorkspaceRoot != "/tmp/workspace-1" || record.Meta.WorkspaceContainer != "Workspace One" {
		t.Fatalf("session workspace snapshot = %q/%q", record.Meta.WorkspaceRoot, record.Meta.WorkspaceContainer)
	}
	var taskMetadata string
	if err := store.db.QueryRow(`SELECT metadata_json FROM tasks WHERE id = 'task-1'`).Scan(&taskMetadata); err != nil {
		t.Fatalf("scan task metadata: %v", err)
	}
	var taskMetadataJSON struct {
		SourceWorkspaceSnapshot struct {
			RootPath    string `json:"root_path"`
			DisplayName string `json:"display_name"`
		} `json:"source_workspace_snapshot"`
	}
	if err := json.Unmarshal([]byte(taskMetadata), &taskMetadataJSON); err != nil {
		t.Fatalf("unmarshal task metadata: %v", err)
	}
	if taskMetadataJSON.SourceWorkspaceSnapshot.RootPath != "/tmp/workspace-1" || taskMetadataJSON.SourceWorkspaceSnapshot.DisplayName != "Workspace One" {
		t.Fatalf("task workspace snapshot = %+v", taskMetadataJSON.SourceWorkspaceSnapshot)
	}
}

func TestOpenMigratesPrimaryWorkspacePointerDeterministically(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 17)
	if err != nil {
		t.Fatalf("open test database at version 17: %v", err)
	}
	if _, err := db.Exec(metadataDBTestSQL(t, "version17_primary_workspace.sql")); err != nil {
		t.Fatalf("seed version 17 primary workspace data: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 17 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()
	got := primaryWorkspaceIDsByProject(t, store.db)
	if got["project-primary"] != "workspace-oldest-primary" {
		t.Fatalf("project-primary primary workspace = %q, want workspace-oldest-primary", got["project-primary"])
	}
	if got["project-fallback"] != "workspace-fallback-oldest" {
		t.Fatalf("project-fallback primary workspace = %q, want workspace-fallback-oldest", got["project-fallback"])
	}
	if got["project-empty"] != "" {
		t.Fatalf("project-empty primary workspace = %q, want empty", got["project-empty"])
	}
}

func TestOpenRejectsWorkspaceSessionRelationshipContradictions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seed string
	}{
		{
			name: "session workspace outside project",
			seed: `
INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-cross-workspace', 'project-a', 'workspace-b', 'projects/project-a/sessions/session-cross-workspace', 1, 1);
`,
		},
		{
			name: "session worktree outside workspace",
			seed: `
INSERT INTO worktrees (id, workspace_id, canonical_root_path, display_name, availability, is_main, git_metadata_json, created_at_unix_ms, updated_at_unix_ms)
VALUES ('worktree-b', 'workspace-b', '/tmp/worktree-b', 'worktree-b', 'available', 0, '{}', 1, 1);
INSERT INTO sessions (id, project_id, workspace_id, worktree_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-cross-worktree', 'project-a', 'workspace-a', 'worktree-b', 'projects/project-a/sessions/session-cross-worktree', 1, 1);
`,
		},
		{
			name: "managed task worktree outside source workspace",
			seed: `
INSERT INTO worktrees (id, workspace_id, canonical_root_path, display_name, availability, is_main, git_metadata_json, created_at_unix_ms, updated_at_unix_ms)
VALUES ('worktree-b', 'workspace-b', '/tmp/worktree-b', 'worktree-b', 'available', 0, '{}', 1, 1);
INSERT INTO workflows (id, name, description, graph_revision, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('workflow-550e8400-e29b-41d4-a716-44665544000a', 'Workflow', '', 1, 1, 1, '{}');
INSERT INTO project_workflow_links (id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('link-a', 'project-a', 'workflow-550e8400-e29b-41d4-a716-44665544000a', 1, 1);
INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, managed_worktree_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-cross-worktree', 'link-a', 1, 1, 'A-1', 'Task', '', 'workspace-a', 'worktree-b', 1, 1, '{}');
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "db", "main.sqlite3")
			db, err := openDatabaseAtVersionForTest(t, root, dbPath, 17)
			if err != nil {
				t.Fatalf("open test database at version 17: %v", err)
			}
			if _, err := db.Exec(metadataDBTestSQL(t, "version17_workspace_session_base.sql")); err != nil {
				t.Fatalf("seed version 17 base data: %v", err)
			}
			if _, err := db.Exec(tt.seed); err != nil {
				t.Fatalf("seed version 17 contradiction: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close version 17 db: %v", err)
			}

			if store, err := Open(root); err == nil {
				_ = store.Close()
				t.Fatal("expected migration to reject contradictory workspace/session data")
			}
		})
	}
}

func TestOpenBackfillsSessionWorkspaceFromSameProjectWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 17)
	if err != nil {
		t.Fatalf("open test database at version 17: %v", err)
	}
	if _, err := db.Exec(metadataDBTestSQL(t, "version17_session_worktree.sql")); err != nil {
		t.Fatalf("seed version 17 session worktree data: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 17 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()
	var workspaceID sql.NullString
	if err := store.db.QueryRow(`SELECT workspace_id FROM sessions WHERE id = 'session-a'`).Scan(&workspaceID); err != nil {
		t.Fatalf("scan migrated session workspace: %v", err)
	}
	if !workspaceID.Valid || workspaceID.String != "workspace-a" {
		t.Fatalf("session workspace = %+v, want workspace-a", workspaceID)
	}
}

func TestOpenMigratesWorkspaceWorktreeDerivedStorageAway(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	workspaceRoot := filepath.Join(t.TempDir(), "derived-workspace")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll workspace root: %v", err)
	}
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 18)
	if err != nil {
		t.Fatalf("open test database at version 18: %v", err)
	}
	if _, err := db.Exec(metadataDBTestSQL(t, "version18_derived_workspace_worktree.sql"), workspaceRoot, workspaceRoot); err != nil {
		t.Fatalf("seed version 18 derived workspace data: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 18 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()
	for _, column := range []string{"display_name", "availability", "is_primary"} {
		if columnExists(t, store.db, "workspaces", column) {
			t.Fatalf("workspaces.%s should have been removed", column)
		}
	}
	for _, column := range []string{"display_name", "availability", "is_main"} {
		if columnExists(t, store.db, "worktrees", column) {
			t.Fatalf("worktrees.%s should have been removed", column)
		}
	}
	workspaces, err := store.ListProjectWorkspaces(t.Context(), "project-derived")
	if err != nil {
		t.Fatalf("ListProjectWorkspaces: %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("workspace count = %d, want 1", len(workspaces))
	}
	if workspaces[0].DisplayName != filepath.Base(workspaceRoot) || string(workspaces[0].Availability) != "available" || !workspaces[0].IsPrimary {
		t.Fatalf("derived workspace summary = %+v", workspaces[0])
	}
	home, err := store.ListProjectHomeSummaries(t.Context(), "project-derived", 1, 0)
	if err != nil {
		t.Fatalf("ListProjectHomeSummaries: %v", err)
	}
	if len(home) != 1 || home[0].PrimaryWorkspace.DisplayName != filepath.Base(workspaceRoot) || home[0].PrimaryWorkspace.Availability != "available" {
		t.Fatalf("derived home summary = %+v", home)
	}
	worktree, err := store.GetWorktreeRecordByID(t.Context(), "worktree-derived")
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	if worktree.DisplayName != filepath.Base(workspaceRoot) || worktree.Availability != "available" || !worktree.IsMain {
		t.Fatalf("derived worktree record = %+v", worktree)
	}
	if !strings.Contains(worktree.GitMetadataJSON, "branch_name") {
		t.Fatalf("worktree git metadata not preserved: %q", worktree.GitMetadataJSON)
	}
}
