package metadata

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed testdata/*.sql
var metadataDBTestFixtures embed.FS

func metadataDBTestSQL(t *testing.T, name string) string {
	t.Helper()
	contents, err := metadataDBTestFixtures.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read metadata db test fixture %s: %v", name, err)
	}
	return string(contents)
}

func TestOpenSuppressesGooseStatusLogging(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	previousDebug := metadataMigrationDebugLogs
	previousWriter := metadataMigrationLogWriter
	metadataMigrationDebugLogs = false
	metadataMigrationLogWriter = &buf
	t.Cleanup(func() {
		metadataMigrationDebugLogs = previousDebug
		metadataMigrationLogWriter = previousWriter
	})

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open metadata store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close metadata store: %v", err)
	}
	if strings.Contains(buf.String(), "goose:") {
		t.Fatalf("did not expect goose status log output, got %q", buf.String())
	}
}

func TestOpenIgnoresGlobalGoMigrationRegistry(t *testing.T) {
	goose.ResetGlobalMigrations()
	t.Cleanup(goose.ResetGlobalMigrations)

	var ran atomic.Bool
	if err := goose.SetGlobalMigrations(goose.NewGoMigration(
		43,
		&goose.GoFunc{RunTx: func(context.Context, *sql.Tx) error {
			ran.Store(true)
			return nil
		}},
		nil,
	)); err != nil {
		t.Fatalf("register conflicting global migration: %v", err)
	}

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open metadata store with conflicting global migration: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if ran.Load() {
		t.Fatal("metadata migration provider executed an unrelated global migration")
	}
}

func TestIsolatedMetadataMigrationProviderRunsOnlyExplicitMigration(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "candidate.sqlite3")
	db, err := openDatabaseAtPathWithoutMigrationsForTest(root, dbPath)
	if err != nil {
		t.Fatalf("open candidate database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	candidate := goose.NewGoMigration(48, &goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE explicit_migration_candidate_marker (value TEXT NOT NULL)`)
		return err
	}}, nil)
	provider, err := newMetadataMigrationProvider(db, candidate)
	if err != nil {
		t.Fatalf("create isolated candidate provider: %v", err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply isolated candidate provider: %v", err)
	}
	if !tableExists(t, db, "explicit_migration_candidate_marker") {
		t.Fatal("explicit candidate migration did not run")
	}

	productionProvider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create production provider: %v", err)
	}
	status, err := productionProvider.Status(t.Context())
	if err != nil {
		t.Fatalf("read production provider status: %v", err)
	}
	for _, migration := range status {
		if migration.Source.Version == 48 {
			t.Fatal("production metadata provider must not expose an unregistered explicit migration")
		}
	}
}

func TestOpenPreservesLegacyMutableIdentityGraph(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 43)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	legacyProjectID := "project-legacy"
	parentSessionID := "b27f28bb-4a74-4e0a-81bf-257ba14d58fa"
	childSessionID := "9f34347d-e61b-49c4-91a5-8f8091bb5a2b"
	now := time.Now().UTC().UnixMilli()
	seedLegacyMutableIdentityGraph(t, db, root, legacyProjectID, parentSessionID, childSessionID, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open legacy-identity database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, identity := range []struct {
		table  string
		column string
	}{
		{table: "projects", column: "id"},
		{table: "workspaces", column: "id"},
		{table: "worktrees", column: "id"},
		{table: "workflows", column: "id"},
		{table: "project_workflow_links", column: "id"},
		{table: "workflow_node_groups", column: "id"},
		{table: "workflow_nodes", column: "id"},
		{table: "workflow_transition_groups", column: "id"},
		{table: "workflow_edges", column: "id"},
		{table: "tasks", column: "id"},
		{table: "task_node_placements", column: "id"},
		{table: "task_runs", column: "id"},
		{table: "task_transitions", column: "id"},
		{table: "task_transition_edges", column: "id"},
		{table: "task_comments", column: "id"},
	} {
		assertTableLegacyIdentifierColumn(t, store.db, identity.table, identity.column)
	}
	for _, reference := range []struct {
		table  string
		column string
	}{
		{table: "projects", column: "primary_workspace_id"},
		{table: "workspaces", column: "project_id"},
		{table: "worktrees", column: "workspace_id"},
		{table: "workflow_node_groups", column: "workflow_id"},
		{table: "workflow_nodes", column: "workflow_id"},
		{table: "workflow_nodes", column: "group_id"},
		{table: "workflow_transition_groups", column: "source_node_id"},
		{table: "workflow_edges", column: "transition_group_id"},
		{table: "workflow_edges", column: "target_node_id"},
		{table: "project_workflow_links", column: "project_id"},
		{table: "project_workflow_links", column: "workflow_id"},
		{table: "tasks", column: "project_workflow_link_id"},
		{table: "tasks", column: "source_workspace_id"},
		{table: "tasks", column: "managed_worktree_id"},
		{table: "task_node_placements", column: "task_id"},
		{table: "task_node_placements", column: "node_id"},
		{table: "task_runs", column: "placement_id"},
		{table: "task_transitions", column: "task_id"},
		{table: "task_transitions", column: "source_run_id"},
		{table: "task_transitions", column: "source_placement_id"},
		{table: "task_transition_edges", column: "task_transition_id"},
		{table: "task_transition_edges", column: "workflow_edge_id"},
		{table: "task_transition_edges", column: "target_node_id"},
		{table: "task_comments", column: "task_id"},
	} {
		assertTableLegacyIdentifierColumn(t, store.db, reference.table, reference.column)
	}
	if !columnExists(t, store.db, "task_runs", "metadata_json") {
		t.Fatal("task_runs.metadata_json must remain available while the ID migration is out of scope")
	}
	assertJSONColumnContainsAll(t, store.db, "workflow_nodes", "join_input_providers_json", "edge-start-1")
	assertJSONColumnContainsAll(t, store.db, "task_runs", "run_start_snapshot_json",
		"workflow-1",
		"node-start",
		"node-agent",
		"node-done",
		"group-start",
		"group-done",
		"edge-start-1",
		"edge-done-1",
	)

	var sessionID string
	if err := store.db.QueryRowContext(t.Context(), `SELECT id FROM sessions WHERE id = ?`, childSessionID).Scan(&sessionID); err != nil {
		t.Fatalf("load stable child session: %v", err)
	}
	if sessionID != childSessionID {
		t.Fatalf("child session ID = %q, want stable %q", sessionID, childSessionID)
	}
	var artifactRelpath string
	if err := store.db.QueryRowContext(t.Context(), `SELECT artifact_relpath FROM sessions WHERE id = ?`, childSessionID).Scan(&artifactRelpath); err != nil {
		t.Fatalf("load remapped artifact relpath: %v", err)
	}
	if !strings.HasPrefix(artifactRelpath, "projects/") || !strings.Contains(artifactRelpath, legacyProjectID) {
		t.Fatalf("artifact relpath = %q, want preserved legacy project root", artifactRelpath)
	}
	sessionJSON, err := os.ReadFile(filepath.Join(root, artifactRelpath, "session.json"))
	if err != nil {
		t.Fatalf("read preserved session.json: %v", err)
	}
	if string(sessionJSON) != `{"legacy":"opaque"}` {
		t.Fatalf("session.json bytes = %q, want historical bytes preserved", sessionJSON)
	}
}

func TestOpenMigratesLegacyWorkflowExecutionPolicyToAsk(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 43)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO workflows (id, name, description, version, created_at_unix_ms, updated_at_unix_ms)
		VALUES ('workflow-legacy-policy', 'Legacy policy workflow', '', 1, ?, ?)
	`, now, now); err != nil {
		t.Fatalf("seed legacy workflow: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var mode string
	var customRef sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
		SELECT execution_policy, execution_custom_ref
		FROM workflows
		WHERE id = 'workflow-legacy-policy'
	`).Scan(&mode, &customRef); err != nil {
		t.Fatalf("load migrated workflow policy: %v", err)
	}
	if mode != "ask" || customRef.Valid {
		t.Fatalf("migrated workflow policy = %q/%+v, want ask/null", mode, customRef)
	}
}

func seedLegacyMutableIdentityGraph(t *testing.T, db *sql.DB, root string, projectID string, parentSessionID string, childSessionID string, now int64) {
	t.Helper()
	workspaceID := "workspace-legacy"
	worktreeID := "worktree-legacy"
	workspaceRoot := filepath.Join(root, "source-workspace")
	worktreeRoot := filepath.Join(root, "task-worktree")
	childRelpath := filepath.ToSlash(filepath.Join("projects", projectID, "sessions", childSessionID))
	parentRelpath := filepath.ToSlash(filepath.Join("projects", projectID, "sessions", parentSessionID))
	if err := os.MkdirAll(filepath.Join(root, childRelpath), 0o755); err != nil {
		t.Fatalf("create legacy child session directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, childRelpath, "session.json"), []byte(`{"legacy":"opaque"}`), 0o600); err != nil {
		t.Fatalf("write legacy session.json: %v", err)
	}
	execSeed(t, db, "legacy project", `INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json) VALUES (?, 'Legacy project', ?, ?, '{}')`, projectID, now, now)
	execSeed(t, db, "legacy workspace", `INSERT INTO workspaces (id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms) VALUES (?, ?, ?, '{}', ?, ?)`, workspaceID, projectID, workspaceRoot, now, now)
	execSeed(t, db, "legacy project primary workspace", `UPDATE projects SET primary_workspace_id = ? WHERE id = ?`, workspaceID, projectID)
	execSeed(t, db, "legacy worktree", `INSERT INTO worktrees (id, workspace_id, canonical_root_path, managed, created_branch, origin_session_id, git_metadata_json, created_at_unix_ms, updated_at_unix_ms) VALUES (?, ?, ?, 1, 1, ?, '{}', ?, ?)`, worktreeID, workspaceID, worktreeRoot, parentSessionID, now, now)
	execSeed(t, db, "legacy parent session", `INSERT INTO sessions (id, project_id, workspace_id, worktree_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms, metadata_json) VALUES (?, ?, ?, ?, ?, ?, ?, '{}')`, parentSessionID, projectID, workspaceID, worktreeID, parentRelpath, now, now)
	execSeed(t, db, "legacy child session", `INSERT INTO sessions (id, project_id, workspace_id, worktree_id, artifact_relpath, parent_session_id, created_at_unix_ms, updated_at_unix_ms, metadata_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '{}')`, childSessionID, projectID, workspaceID, worktreeID, childRelpath, parentSessionID, now, now)
	execSeed(t, db, "legacy prompt history", `INSERT INTO session_prompt_history_entries (session_id, source_id, text, created_at_unix_ms) VALUES (?, 'legacy-source', 'legacy prompt', ?)`, childSessionID, now)

	seedWorkflowGraph(t, db, projectID, now)
	execSeed(t, db, "legacy node group", `INSERT INTO workflow_node_groups (id, workflow_id, group_key, display_name, sort_order) VALUES ('node-group-legacy', 'workflow-1', 'legacy_group', 'Legacy group', 0)`)
	execSeed(t, db, "legacy node group membership", `UPDATE workflow_nodes SET group_id = 'node-group-legacy' WHERE id = 'node-agent'`)
	execSeed(t, db, "legacy join-provider edge reference", `UPDATE workflow_nodes SET join_input_providers_json = '[{"input_name":"summary","provider_edge_id":"edge-start-1"}]' WHERE id = 'node-agent'`)
	execSeed(t, db, "legacy task", workflowSeedTaskSQL, "task-legacy", "link-1", 1, "LEG-1", now, now)
	execSeed(t, db, "legacy task source and worktree", `UPDATE tasks SET source_workspace_id = ?, managed_worktree_id = ? WHERE id = 'task-legacy'`, workspaceID, worktreeID)
	execSeed(t, db, "legacy placement", workflowSeedPlacementSQL, "placement-legacy", "task-legacy", "node-agent", now, now)
	execSeed(t, db, "legacy run", `INSERT INTO task_runs (id, placement_id, session_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms, run_start_snapshot_json, metadata_json) VALUES ('run-legacy', 'placement-legacy', ?, 1, ?, ?, '{"workflow_id":"workflow-1","workflow_revision_seen":1,"node":{"id":"node-agent","key":"agent","display_name":"Agent","kind":"agent","join_input_providers":[{"input_name":"summary","provider_edge_id":"edge-start-1"}],"output_fields":[{"name":"summary","description":"Summary."}]},"nodes":[{"id":"node-start","key":"backlog","display_name":"Backlog","kind":"start"},{"id":"node-agent","key":"agent","display_name":"Agent","kind":"agent","join_input_providers":[{"input_name":"summary","provider_edge_id":"edge-start-1"}],"output_fields":[{"name":"summary","description":"Summary."}]},{"id":"node-done","key":"done","display_name":"Done","kind":"terminal"}],"transition_groups":[{"id":"group-start","source_node_id":"node-start","transition_id":"start","display_name":"Start","edges":[{"id":"edge-start-1","key":"start","target_node":{"id":"node-agent","key":"agent","display_name":"Agent","kind":"agent","join_input_providers":[{"input_name":"summary","provider_edge_id":"edge-start-1"}],"output_fields":[{"name":"summary","description":"Summary."}]},"context_mode":"new_session","context_source":{"kind":"immediate_source"},"requires_approval":false}]},{"id":"group-done","source_node_id":"node-agent","transition_id":"done","display_name":"Done","edges":[{"id":"edge-done-1","key":"done","target_node":{"id":"node-done","key":"done","display_name":"Done","kind":"terminal"},"context_mode":"new_session","context_source":{"kind":"immediate_source"},"requires_approval":false}]}]}', '{"context_mode":"new_session"}')`, childSessionID, now, now)
	execSeed(t, db, "legacy transition", `INSERT INTO task_transitions (id, task_id, source_run_id, source_placement_id, source_node_key, source_node_display_name, transition_id, transition_display_name, workflow_revision_seen, actor, state, output_values_json, created_at_unix_ms, applied_at_unix_ms) VALUES ('transition-legacy', 'task-legacy', 'run-legacy', 'placement-legacy', 'agent', 'Agent', 'done', 'Done', 1, 'agent', 'applied', '{}', ?, ?)`, now, now)
	execSeed(t, db, "legacy transition edge", `INSERT INTO task_transition_edges (id, task_transition_id, workflow_edge_id, edge_key, target_node_id, target_node_key, target_node_display_name, target_node_kind, target_placement_id, state, context_mode, input_bindings_json, output_requirements_json, metadata_json) VALUES ('transition-edge-legacy', 'transition-legacy', 'edge-done-1', 'done', 'node-done', 'done', 'Done', 'terminal', NULL, 'applied', 'new_session', '[]', '[]', '{}')`)
	execSeed(t, db, "legacy comment", `INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms) VALUES ('comment-legacy', 'task-legacy', 'Legacy comment', 'user', 'user-legacy', ?, ?)`, now, now)
}

func assertTableLegacyIdentifierColumn(t *testing.T, db *sql.DB, table string, column string) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT `+column+` FROM `+table+` WHERE `+column+` IS NOT NULL`)
	if err != nil {
		t.Fatalf("query %s.%s: %v", table, column, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan %s.%s: %v", table, column, err)
		}
		if parsed, err := uuid.Parse(value); err == nil && parsed.Version() == 4 {
			t.Fatalf("%s.%s = %q, want preserved legacy identifier", table, column, value)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s.%s: %v", table, column, err)
	}
}

func assertJSONColumnContainsAll(t *testing.T, db *sql.DB, table string, column string, expected ...string) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT `+column+` FROM `+table)
	if err != nil {
		t.Fatalf("query %s.%s: %v", table, column, err)
	}
	defer func() { _ = rows.Close() }()
	found := make(map[string]bool, len(expected))
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan %s.%s: %v", table, column, err)
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatalf("decode %s.%s: %v", table, column, err)
		}
		collectJSONStrings(value, found)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s.%s: %v", table, column, err)
	}
	for _, expectedValue := range expected {
		if !found[expectedValue] {
			t.Fatalf("typed JSON does not preserve legacy identifier %q", expectedValue)
		}
	}
}

func collectJSONStrings(value any, found map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for _, nested := range typed {
			collectJSONStrings(nested, found)
		}
	case []any:
		for _, nested := range typed {
			collectJSONStrings(nested, found)
		}
	case string:
		found[typed] = true
	}
}

func TestOpenConfiguresSQLitePragmasThroughPathSafeDSN(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db with spaces", "main ? #.sqlite3")
	store, err := OpenAtPath(root, dbPath)
	if err != nil {
		t.Fatalf("OpenAtPath with escaped path characters: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var foreignKeys int64
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var synchronous int64
	if err := store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if synchronous != 1 {
		t.Fatalf("synchronous = %d, want NORMAL(1)", synchronous)
	}
	var busyTimeout int64
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database path was not created at expected path: %v", err)
	}
}

func TestMetadataSQLiteDSNNormalizesWindowsPaths(t *testing.T) {
	dsn := metadataSQLiteDSN(`C:\Users\Nek\kent db\main ? #.sqlite3`)
	if !strings.HasPrefix(dsn, "file:///C:/Users/Nek/kent%20db/main%20%3F%20%23.sqlite3?") {
		t.Fatalf("dsn = %q, want file URL with normalized Windows drive path", dsn)
	}
	if !strings.Contains(dsn, "_pragma=foreign_keys%281%29") {
		t.Fatalf("dsn = %q, want pragma query values preserved", dsn)
	}
}

func TestMetadataMigrationSQLiteDSNUsesExclusiveTransactionLock(t *testing.T) {
	dsn := metadataMigrationSQLiteDSN(filepath.Join(t.TempDir(), "main.sqlite3"))
	if !strings.Contains(dsn, "_txlock=exclusive") {
		t.Fatalf("migration DSN = %q, want exclusive transaction lock", dsn)
	}
}

func TestOpenAllowsDatabaseAtRemovedMigrationVersion(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("initial open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(metadataDBTestSQL(t, "legacy_mutation_dedupe.sql")); err != nil {
		t.Fatalf("create legacy mutation_dedupe table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (3, 1)`); err != nil {
		t.Fatalf("insert removed migration version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite db: %v", err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatalf("reopen metadata store with removed migration version: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
}

func TestOpenMigratesCommentsToMinimalStorage(t *testing.T) {
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

func TestOpenDropsPersistedWorkflowEvents(t *testing.T) {
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
	assertSQLiteConstraint(t, store.db, `INSERT INTO task_comments (id, task_id, body, author_kind, created_at_unix_ms, updated_at_unix_ms) VALUES ('comment-system-rejected', 'task-system-comment', 'bad', 'system', 1, 1)`)
}

func TestOpenRemovesRedundantIndexesAndArchiveMetadata(t *testing.T) {
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
	tests := []struct {
		name string
		seed string
	}{
		{
			name: "transition group workflow disagrees with source node",
			seed: `INSERT INTO workflow_transition_groups (id, workflow_id, source_node_id, transition_id, display_name)
VALUES ('group-bad', 'workflow-b', 'node-a', 'bad', 'Bad');`,
		},
		{
			name: "edge workflow disagrees with transition group source node",
			seed: `
INSERT INTO workflow_transition_groups (id, workflow_id, source_node_id, transition_id, display_name)
VALUES ('group-a', 'workflow-a', 'node-a', 'next', 'Next');
INSERT INTO workflow_edges (id, workflow_id, transition_group_id, edge_key, target_node_id, context_mode, input_bindings_json, output_requirements_json)
VALUES ('edge-bad', 'workflow-b', 'group-a', 'next', 'node-a', 'new_session', '{}', '{}');`,
		},
		{
			name: "edge target node belongs to different workflow",
			seed: `
INSERT INTO workflow_transition_groups (id, workflow_id, source_node_id, transition_id, display_name)
VALUES ('group-a', 'workflow-a', 'node-a', 'next', 'Next');
INSERT INTO workflow_edges (id, workflow_id, transition_group_id, edge_key, target_node_id, context_mode, input_bindings_json, output_requirements_json)
VALUES ('edge-bad', 'workflow-a', 'group-a', 'next', 'node-b', 'new_session', '{}', '{}');`,
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
VALUES ('workflow-a', 'A', '', 1, 1, 1),
       ('workflow-b', 'B', '', 1, 1, 1);
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, output_fields_json)
VALUES ('node-a', 'workflow-a', 'start', 'start', 'Start A', '[]'),
       ('node-b', 'workflow-b', 'done', 'terminal', 'Done B', '[]');
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

func TestOpenMigratesHistoricalTerminalPlacementsToSuperseded(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 39)
	if err != nil {
		t.Fatalf("open test database at version 39: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json) VALUES ('project-terminal-history', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-terminal-history", now)
	execSeed(t, db, "workflow task", workflowSeedTaskSQL, "task-terminal-history", "link-1", 1, "TRM-1", now, now)
	execSeed(t, db, "legacy terminal placement", `INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms) VALUES ('placement-terminal-history', 'task-terminal-history', 'node-done', 'completed', ?, ?)`, now+1, now+1)
	execSeed(t, db, "same-timestamp active placement", `INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms) VALUES ('placement-active-after-terminal', 'task-terminal-history', 'node-start', 'active', ?, ?)`, now+1, now+1)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 39 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()
	var terminalState string
	if err := store.db.QueryRowContext(t.Context(), `SELECT state FROM task_node_placements WHERE id = 'placement-terminal-history'`).Scan(&terminalState); err != nil {
		t.Fatalf("query migrated terminal placement: %v", err)
	}
	if terminalState != "superseded" {
		t.Fatalf("terminal placement state = %q, want superseded", terminalState)
	}
}

func TestOpenMigratesWorkflowScriptNodesWithRuntimeReferences(t *testing.T) {
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
VALUES ('workflow-a', 'Workflow', '', 1, 1, 1, '{}');
INSERT INTO project_workflow_links (id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('link-a', 'project-a', 'workflow-a', 1, 1);
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

func openDatabaseAtVersionForTest(t *testing.T, root string, dbPath string, version int64) (*sql.DB, error) {
	t.Helper()
	db, err := openDatabaseAtPathWithoutMigrationsForTest(root, dbPath)
	if err != nil {
		return nil, err
	}
	migrations, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations, goose.WithLogger(goose.NopLogger()), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := provider.UpTo(context.Background(), version); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func openDatabaseAtPathWithoutMigrationsForTest(root string, dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", metadataSQLiteDSN(dbPath))
	if err != nil {
		return nil, err
	}
	return db, nil
}

func primaryWorkspaceIDsByProject(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT id, primary_workspace_id FROM projects`)
	if err != nil {
		t.Fatalf("query project primary workspace ids: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var projectID string
		var workspaceID sql.NullString
		if err := rows.Scan(&projectID, &workspaceID); err != nil {
			t.Fatalf("scan project primary workspace id: %v", err)
		}
		out[projectID] = workspaceID.String
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate project primary workspace ids: %v", err)
	}
	return out
}
