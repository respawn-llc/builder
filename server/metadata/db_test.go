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
	"testing"
	"time"

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

func TestSessionCategoryMigrationAddsNullableConstrainedIndexedStorage(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if !columnExists(t, store.db, "sessions", "category") {
		t.Fatal("sessions.category is missing")
	}
	if !indexExists(t, store.db, "sessions_visible_category_recency_idx") {
		t.Fatal("sessions_visible_category_recency_idx is missing")
	}

	var partial int
	rows, err := store.db.Query(`PRAGMA index_list('sessions')`)
	if err != nil {
		t.Fatalf("list session indexes: %v", err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var sequence int
		var name, origin string
		var unique int
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan session index: %v", err)
		}
		if name == "sessions_visible_category_recency_idx" {
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate session indexes: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close session indexes: %v", err)
	}
	if !found || partial != 1 {
		t.Fatalf("visible category recency index found=%v partial=%d, want true/1", found, partial)
	}

	indexRows, err := store.db.Query(`PRAGMA index_xinfo('sessions_visible_category_recency_idx')`)
	if err != nil {
		t.Fatalf("inspect session category index: %v", err)
	}
	defer func() { _ = indexRows.Close() }()
	type indexedColumn struct {
		name string
		desc int
	}
	var columns []indexedColumn
	for indexRows.Next() {
		var sequence, columnID, desc, key int
		var name sql.NullString
		var collation string
		if err := indexRows.Scan(&sequence, &columnID, &name, &desc, &collation, &key); err != nil {
			t.Fatalf("scan session category index column: %v", err)
		}
		if key == 1 && name.Valid {
			columns = append(columns, indexedColumn{name: name.String, desc: desc})
		}
	}
	if err := indexRows.Err(); err != nil {
		t.Fatalf("iterate session category index columns: %v", err)
	}
	wantColumns := []indexedColumn{
		{name: "project_id"},
		{name: "category"},
		{name: "updated_at_unix_ms", desc: 1},
		{name: "id", desc: 1},
	}
	if len(columns) != len(wantColumns) {
		t.Fatalf("index columns = %+v, want %+v", columns, wantColumns)
	}
	for index := range wantColumns {
		if columns[index] != wantColumns[index] {
			t.Fatalf("index column %d = %+v, want %+v", index, columns[index], wantColumns[index])
		}
	}
}

func TestSessionCategoryStorageRejectsInvalidValues(t *testing.T) {
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)
	if _, err := store.db.Exec(`UPDATE sessions SET category = 'worker' WHERE id = ?`, sess.Meta().SessionID); err == nil {
		t.Fatal("sessions.category accepted an invalid value")
	}
}

func TestSessionCategoryMigrationPreservesLegacyRowsAsNull(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 53)
	if err != nil {
		t.Fatalf("open version 53 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	workspaceRoot := t.TempDir()
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys for legacy seed: %v", err)
	}
	execSeed(t, db, "legacy workspace", `INSERT INTO workspaces (id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms)
VALUES ('workspace-category-legacy', 'project-category-legacy', ?, '{}', ?, ?)`, workspaceRoot, now, now)
	execSeed(t, db, "legacy project", `INSERT INTO projects (id, display_name, project_key, next_task_seq, primary_workspace_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('project-category-legacy', 'Legacy category', 'LEG', 1, 'workspace-category-legacy', ?, ?)`, now, now)
	execSeed(t, db, "legacy session", `INSERT INTO sessions (
    id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'session-category-legacy',
    'project-category-legacy',
    'workspace-category-legacy',
    'projects/project-category-legacy/sessions/session-category-legacy',
    ?,
    ?
)`, now, now)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys after legacy seed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 53 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var category sql.NullString
	if err := store.db.QueryRow(`SELECT category FROM sessions WHERE id = 'session-category-legacy'`).Scan(&category); err != nil {
		t.Fatalf("scan migrated category: %v", err)
	}
	if category.Valid {
		t.Fatalf("legacy category = %q, want SQL NULL", category.String)
	}
	record, err := store.ResolvePersistedSession(t.Context(), "session-category-legacy")
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.Category != nil {
		t.Fatalf("legacy resolved category = %v, want absent", record.Meta.Category)
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

func TestOpenBackfillsExecutionTargetsForEveryLegacyTaskWithUsableRecordedWorktreeHead(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 52)
	if err != nil {
		t.Fatalf("open version 52 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "legacy project", `INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms)
VALUES ('project-legacy-target', 'Legacy target project', ?, ?)`, now, now)
	execSeed(t, db, "legacy workspace", `INSERT INTO workspaces (id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms)
VALUES ('workspace-legacy-target', 'project-legacy-target', ?, '{}', ?, ?)`, t.TempDir(), now, now)
	seedWorkflowGraph(t, db, "project-legacy-target", now)
	execSeed(t, db, "legacy managed worktrees", `INSERT INTO worktrees (
    id, workspace_id, canonical_root_path, managed, created_branch, origin_session_id, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES
    ('worktree-legacy-valid', 'workspace-legacy-target', ?, 1, 1, '', '{"head_oid":"observed-commit","branch_ref":"refs/heads/BLD-1"}', ?, ?),
    ('worktree-legacy-invalid', 'workspace-legacy-target', ?, 1, 1, '', '{"branch_ref":"refs/heads/BLD-3"}', ?, ?)`,
		t.TempDir(), now, now, t.TempDir(), now, now,
	)
	for _, task := range []struct {
		id          string
		taskSeq     int
		shortID     string
		worktreeID  string
		placementID string
		nodeID      string
		runID       string
	}{
		{id: "task-legacy-executed", taskSeq: 1, shortID: "BLD-1", worktreeID: "worktree-legacy-valid", placementID: "placement-legacy-executed", nodeID: "node-agent", runID: "run-legacy-executed"},
		{id: "task-legacy-backlog", taskSeq: 2, shortID: "BLD-2", worktreeID: "worktree-legacy-valid", placementID: "placement-legacy-backlog", nodeID: "node-start"},
		{id: "task-legacy-invalid-oid", taskSeq: 3, shortID: "BLD-3", worktreeID: "worktree-legacy-invalid", placementID: "placement-legacy-invalid", nodeID: "node-agent", runID: "run-legacy-invalid"},
	} {
		execSeed(t, db, "legacy task", `INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, managed_worktree_id,
    created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (?, 'link-1', 1, ?, ?, 'Legacy task', '', 'workspace-legacy-target', ?, ?, ?, '{}')`,
			task.id, task.taskSeq, task.shortID, task.worktreeID, now, now,
		)
		execSeed(t, db, "legacy placement", `INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, ?, 'active', ?, ?)`, task.placementID, task.id, task.nodeID, now, now)
		if task.runID != "" {
			execSeed(t, db, "legacy executable run", `INSERT INTO task_runs (id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, 1, ?, ?)`, task.runID, task.placementID, now, now)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 52 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var policy string
	if err := store.db.QueryRow(`SELECT execution_target_policy FROM workflows WHERE id = 'workflow-1'`).Scan(&policy); err != nil {
		t.Fatalf("read migrated workflow policy: %v", err)
	}
	if policy != "head" {
		t.Fatalf("migrated workflow policy = %q, want head", policy)
	}

	type targetRow struct {
		mode       sql.NullString
		requested  sql.NullString
		resolved   sql.NullString
		commitOID  sql.NullString
		provenance sql.NullString
		worktreeID sql.NullString
	}
	readTarget := func(taskID string) targetRow {
		t.Helper()
		var row targetRow
		if err := store.db.QueryRow(`SELECT
    execution_target_mode,
    execution_target_requested_ref,
    execution_target_resolved_ref,
    execution_target_commit_oid,
    execution_target_provenance,
    managed_worktree_id
FROM tasks
WHERE id = ?`, taskID).Scan(&row.mode, &row.requested, &row.resolved, &row.commitOID, &row.provenance, &row.worktreeID); err != nil {
			t.Fatalf("read migrated task target %s: %v", taskID, err)
		}
		return row
	}

	executed := readTarget("task-legacy-executed")
	if !executed.mode.Valid || executed.mode.String != "head" ||
		!executed.requested.Valid || executed.requested.String != "HEAD" ||
		!executed.resolved.Valid || executed.resolved.String != "refs/heads/BLD-1" ||
		!executed.commitOID.Valid || executed.commitOID.String != "observed-commit" ||
		!executed.provenance.Valid || executed.provenance.String != "legacy_observed" ||
		!executed.worktreeID.Valid || executed.worktreeID.String != "worktree-legacy-valid" {
		t.Fatalf("executed legacy target = %+v, want observed head target", executed)
	}
	backlog := readTarget("task-legacy-backlog")
	if !backlog.mode.Valid || backlog.mode.String != "head" ||
		!backlog.requested.Valid || backlog.requested.String != "HEAD" ||
		!backlog.resolved.Valid || backlog.resolved.String != "refs/heads/BLD-1" ||
		!backlog.commitOID.Valid || backlog.commitOID.String != "observed-commit" ||
		!backlog.provenance.Valid || backlog.provenance.String != "legacy_observed" ||
		!backlog.worktreeID.Valid || backlog.worktreeID.String != "worktree-legacy-valid" {
		t.Fatalf("backlog legacy target = %+v, want observed head target", backlog)
	}
	invalid := readTarget("task-legacy-invalid-oid")
	if invalid.mode.Valid || invalid.requested.Valid || invalid.resolved.Valid || invalid.commitOID.Valid || invalid.provenance.Valid {
		t.Fatalf("invalid migrated task target = %+v, want all snapshot facts null", invalid)
	}
	if !invalid.worktreeID.Valid {
		t.Fatal("invalid migrated task lost provisional managed worktree relation")
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

func TestOpenNormalizesCurrentCompletedTerminalSinkWithoutChangingRuns(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 43)
	if err != nil {
		t.Fatalf("open test database at version 43: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json) VALUES ('project-terminal-sink', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-terminal-sink", now)
	execSeed(t, db, "workflow task", workflowSeedTaskSQL, "task-terminal-sink", "link-1", 1, "TSK-1", now, now)
	execSeed(t, db, "historical terminal placement", `INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms) VALUES ('placement-terminal-history', 'task-terminal-sink', 'node-done', 'superseded', ?, ?)`, now+1, now+1)
	execSeed(t, db, "current completed terminal placement", `INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms) VALUES ('placement-terminal-current', 'task-terminal-sink', 'node-done', 'completed', ?, ?)`, now+2, now+2)
	execSeed(t, db, "parallel active placement", `INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms) VALUES ('placement-parallel-active', 'task-terminal-sink', 'node-agent', 'active', ?, ?)`, now+2, now+2)
	execSeed(t, db, "legacy terminal run", `INSERT INTO task_runs (id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms, completed_at_unix_ms) VALUES ('run-terminal-current', 'placement-terminal-current', 1, ?, ?, ?)`, now+2, now+2, now+3)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 43 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()

	states := map[string]string{}
	rows, err := store.db.QueryContext(t.Context(), `SELECT id, state FROM task_node_placements WHERE task_id = 'task-terminal-sink'`)
	if err != nil {
		t.Fatalf("query terminal placement states: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			t.Fatalf("scan terminal placement state: %v", err)
		}
		states[id] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate terminal placement states: %v", err)
	}
	if states["placement-terminal-current"] != "active" || states["placement-terminal-history"] != "superseded" {
		t.Fatalf("terminal placement states = %+v, want active current sink and unchanged superseded history", states)
	}
	var completedAt int64
	if err := store.db.QueryRowContext(t.Context(), `SELECT completed_at_unix_ms FROM task_runs WHERE id = 'run-terminal-current'`).Scan(&completedAt); err != nil {
		t.Fatalf("query terminal run after migration: %v", err)
	}
	if completedAt != now+3 {
		t.Fatalf("terminal run completion = %d, want unchanged %d", completedAt, now+3)
	}
}

func TestOpenMigratesLifecycleAbsenceSentinelsToNull(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 45)
	if err != nil {
		t.Fatalf("open test database at version 45: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json) VALUES ('project-lifecycle-null', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-lifecycle-null", now)
	execSeed(t, db, "workflow task", workflowSeedTaskSQL, "task-lifecycle-null", "link-1", 1, "NUL-1", now, now)
	execSeed(t, db, "legacy lifecycle sentinels", `
UPDATE tasks
SET canceled_at_unix_ms = 0
WHERE id = 'task-lifecycle-null';
INSERT INTO task_node_placements (id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms)
VALUES ('placement-lifecycle-null', 'task-lifecycle-null', 'node-agent', 'active', ?, ?);
INSERT INTO task_runs (
    id,
    placement_id,
    workflow_revision_seen,
    automation_requested_at_unix_ms,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interrupted_at_unix_ms,
    waiting_ask_id
) VALUES ('run-lifecycle-null', 'placement-lifecycle-null', 1, 0, ?, ?, 0, 0, 0, '');
INSERT INTO task_transitions (
    id,
    task_id,
    source_placement_id,
    source_node_key,
    transition_id,
    workflow_revision_seen,
    actor,
    state,
    created_at_unix_ms,
    applied_at_unix_ms
) VALUES ('transition-lifecycle-null', 'task-lifecycle-null', 'placement-lifecycle-null', 'agent', 'done', 1, 'system', 'pending_approval', ?, 0);
`, now, now, now, now, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 45 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer func() { _ = store.Close() }()

	var canceledAt, automationRequestedAt, startedAt, completedAt, interruptedAt, appliedAt sql.NullInt64
	var waitingAskID sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    t.canceled_at_unix_ms,
    r.automation_requested_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.waiting_ask_id,
    tt.applied_at_unix_ms
FROM tasks t
JOIN task_node_placements p ON p.task_id = t.id
JOIN task_runs r ON r.placement_id = p.id
JOIN task_transitions tt ON tt.task_id = t.id
WHERE t.id = 'task-lifecycle-null'
`).Scan(&canceledAt, &automationRequestedAt, &startedAt, &completedAt, &interruptedAt, &waitingAskID, &appliedAt); err != nil {
		t.Fatalf("query migrated lifecycle facts: %v", err)
	}
	if canceledAt.Valid || automationRequestedAt.Valid || startedAt.Valid || completedAt.Valid || interruptedAt.Valid || waitingAskID.Valid || appliedAt.Valid {
		t.Fatalf("migrated lifecycle facts = canceled=%+v automation_requested=%+v started=%+v completed=%+v interrupted=%+v waiting_ask=%+v applied=%+v, want NULL absence", canceledAt, automationRequestedAt, startedAt, completedAt, interruptedAt, waitingAskID, appliedAt)
	}
	var status string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT kind
FROM workflow_task_status_records
WHERE task_id = 'task-lifecycle-null'
`).Scan(&status); err != nil {
		t.Fatalf("query canonical status after lifecycle migration: %v", err)
	}
	if status != "waiting_approval" {
		t.Fatalf("canonical status after lifecycle migration = %q, want waiting_approval", status)
	}
	var currentRunID string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT id
FROM workflow_task_current_run_records
WHERE task_id = 'task-lifecycle-null'
`).Scan(&currentRunID); err != nil {
		t.Fatalf("query current run projection after lifecycle migration: %v", err)
	}
	if currentRunID != "run-lifecycle-null" {
		t.Fatalf("current run projection after lifecycle migration = %q, want run-lifecycle-null", currentRunID)
	}
	if _, err := store.db.ExecContext(t.Context(), `
UPDATE task_runs
SET automation_requested_at_unix_ms = 0
WHERE id = 'run-lifecycle-null'
`); err == nil {
		t.Fatal("zero automation request timestamp should be rejected after lifecycle migration")
	}
	if _, err := store.db.ExecContext(t.Context(), `
UPDATE task_transitions
SET applied_at_unix_ms = 0
WHERE id = 'transition-lifecycle-null'
`); err == nil {
		t.Fatal("zero transition application timestamp should be rejected after lifecycle migration")
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
