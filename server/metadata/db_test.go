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
	"reflect"
	"strings"
	"testing"
	"time"

	"core/server/workflow"

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

	requireMetadataSQLitePragmas(t, store.db)
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database path was not created at expected path: %v", err)
	}
}

func TestOpenConfiguresEightConnectionSQLitePool(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if got := store.db.Stats().MaxOpenConnections; got != 8 {
		t.Fatalf("max open connections = %d, want 8", got)
	}
	connections := make([]*sql.Conn, 0, 8)
	for range 8 {
		connection, err := store.db.Conn(t.Context())
		if err != nil {
			t.Fatalf("acquire pooled connection: %v", err)
		}
		connections = append(connections, connection)
		requireMetadataSQLitePragmas(t, connection)
	}
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			t.Fatalf("return pooled connection: %v", err)
		}
	}
	if got := store.db.Stats().Idle; got != 8 {
		t.Fatalf("idle connections = %d, want 8", got)
	}
}

type sqlitePragmaQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func requireMetadataSQLitePragmas(t testing.TB, queryer sqlitePragmaQueryer) {
	t.Helper()
	var foreignKeys int64
	if err := queryer.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	var journalMode string
	if err := queryer.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var synchronous int64
	if err := queryer.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if synchronous != 1 {
		t.Fatalf("synchronous = %d, want NORMAL(1)", synchronous)
	}
	var busyTimeout int64
	if err := queryer.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
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

func TestOpenProjectsLegacyBacklogPlacementToCurrentNode(t *testing.T) {
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
}

func TestOpenProjectsLegacyTerminalPlacementToCurrentNode(t *testing.T) {
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

func TestOpenProjectsLegacyActiveAgentRunToInterruptedCurrentNode(t *testing.T) {
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
		currentInputs != "{}" ||
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
}

func TestOpenProjectsLegacyRunnableAgentPlacementToInterruptedCurrentNode(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	placementUpdatedAt := now + 7
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-runnable-agent-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-runnable-agent-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-runnable-agent-migration", "link-1", 1, "RUN-1", now, now)
	execSeed(
		t,
		db,
		"runnable agent placement",
		workflowSeedPlacementSQL,
		"placement-runnable-agent-migration",
		"task-runnable-agent-migration",
		"node-agent",
		now,
		placementUpdatedAt,
	)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID, schedulingState, interruptionReason, interruptionDetail string
	var interruptedAt int64
	var sessionID sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    node_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms,
    session_id
FROM task_current_nodes
WHERE task_id = 'task-runnable-agent-migration'`).Scan(
		&nodeID,
		&schedulingState,
		&interruptionReason,
		&interruptionDetail,
		&interruptedAt,
		&sessionID,
	); err != nil {
		t.Fatalf("query projected runnable agent current node: %v", err)
	}
	if nodeID != "node-agent" ||
		schedulingState != "interrupted" ||
		interruptionReason != "server_restart" ||
		interruptionDetail != `{"code":"workflow.execution.restarted","fields":{"operation":"recovery"}}` ||
		interruptedAt != placementUpdatedAt ||
		sessionID.Valid {
		t.Fatalf(
			"projected runnable agent current node = node=%q scheduling=%q reason=%q detail=%q interrupted_at=%d session=%+v",
			nodeID,
			schedulingState,
			interruptionReason,
			interruptionDetail,
			interruptedAt,
			sessionID,
		)
	}
}

func TestOpenProjectsLegacyWaitingQuestionInterruptsCurrentNodeWithoutClearingAsk(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-waiting-question-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-waiting-question-migration",
		"workspace-waiting-question-migration",
		"550e8400-e29b-41d4-a716-446655440003",
		now,
	)
	seedWorkflowGraph(t, db, "project-waiting-question-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-waiting-question-migration", "link-1", 1, "ASK-1", now, now)
	execSeed(t, db, "agent placement", workflowSeedPlacementSQL, "placement-waiting-question-migration", "task-waiting-question-migration", "node-agent", now, now)
	execSeed(t, db, "waiting question run", `
INSERT INTO task_runs (
    id, placement_id, session_id, workflow_revision_seen,
    created_at_unix_ms, updated_at_unix_ms, started_at_unix_ms, waiting_ask_id
) VALUES (
    'run-waiting-question-migration',
    'placement-waiting-question-migration',
    '550e8400-e29b-41d4-a716-446655440003',
    1,
    ?, ?, ?, 'ask-precutover'
)`, now, now+3, now+1)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var schedulingState, interruptionReason string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT scheduling_state, interruption_reason
FROM task_current_nodes
WHERE task_id = 'task-waiting-question-migration'`).Scan(&schedulingState, &interruptionReason); err != nil {
		t.Fatalf("query migrated waiting-question current node: %v", err)
	}
	if schedulingState != "interrupted" || interruptionReason != "server_restart" {
		t.Fatalf("migrated waiting-question current node = scheduling=%q reason=%q", schedulingState, interruptionReason)
	}

	var askID string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT waiting_ask_id
FROM task_runs
WHERE id = 'run-waiting-question-migration'`).Scan(&askID); err != nil {
		t.Fatalf("query retained legacy ask reference: %v", err)
	}
	if askID != "ask-precutover" {
		t.Fatalf("retained legacy ask reference = %q, want ask-precutover", askID)
	}
}

func seedLegacyWorkflowSession(t *testing.T, db *sql.DB, projectID, workspaceID, sessionID string, now int64) {
	t.Helper()
	execSeed(t, db, "workspace", `
INSERT INTO workspaces (
    id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, ?, '{}', ?, ?)`, workspaceID, projectID, "/"+workspaceID, now, now)
	execSeed(t, db, "session", `
INSERT INTO sessions (
    id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID,
		projectID,
		workspaceID,
		"sessions/"+sessionID,
		now,
		now,
	)
}

func TestOpenProjectsRetainsCompletedAgentSessionAssociation(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	runUpdatedAt := now + 8
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-completed-session-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-completed-session-migration",
		"workspace-completed-session-migration",
		"550e8400-e29b-41d4-a716-446655440001",
		now,
	)
	seedWorkflowGraph(t, db, "project-completed-session-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-completed-session-migration", "link-1", 1, "SES-1", now, now)
	execSeed(t, db, "terminal placement", workflowSeedPlacementSQL, "placement-completed-session-terminal", "task-completed-session-migration", "node-done", now, now)
	execSeed(t, db, "completed agent placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-completed-session-agent',
    'task-completed-session-migration',
    'node-agent',
    'completed',
    ?,
    ?
)`, now, now+1)
	execSeed(t, db, "completed agent run", `
INSERT INTO task_runs (
    id,
    placement_id,
    session_id,
    workflow_revision_seen,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms
) VALUES (
    'run-completed-session-migration',
    'placement-completed-session-agent',
    '550e8400-e29b-41d4-a716-446655440001',
    1,
    ?,
    ?,
    ?,
    ?
)`, now, runUpdatedAt, now+1, now+2)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var taskID, nodeID string
	var associatedAt int64
	if err := store.db.QueryRowContext(t.Context(), `
SELECT session.task_id, association.node_id, association.associated_at_unix_ms
FROM sessions session
JOIN session_workflow_node_associations association ON association.session_id = session.id
WHERE session.id = '550e8400-e29b-41d4-a716-446655440001'`).Scan(&taskID, &nodeID, &associatedAt); err != nil {
		t.Fatalf("query retained completed agent session association: %v", err)
	}
	if taskID != "task-completed-session-migration" || nodeID != "node-agent" || associatedAt != runUpdatedAt {
		t.Fatalf("retained completed agent session association = task=%q node=%q associated_at=%d", taskID, nodeID, associatedAt)
	}

	var historicalCurrentNodeCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM task_current_nodes
WHERE task_id = 'task-completed-session-migration'
  AND node_id = 'node-agent'`).Scan(&historicalCurrentNodeCount); err != nil {
		t.Fatalf("count projected completed agent current nodes: %v", err)
	}
	if historicalCurrentNodeCount != 0 {
		t.Fatalf("completed agent current node count = %d, want 0", historicalCurrentNodeCount)
	}
}

func TestOpenProjectsMigratesLegacySerialPendingApprovalAfterGraphDeletion(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-pending-approval-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-pending-approval-migration",
		"workspace-pending-approval-migration",
		"550e8400-e29b-41d4-a716-446655440002",
		now,
	)
	seedWorkflowGraph(t, db, "project-pending-approval-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-pending-approval-migration", "link-1", 1, "APR-1", now, now)
	execSeed(t, db, "waiting approval placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-pending-approval-migration',
    'task-pending-approval-migration',
    'node-agent',
    'waiting_approval',
    ?,
    ?
)`, now, now+1)
	execSeed(t, db, "completed approval source run", `
INSERT INTO task_runs (
    id, placement_id, session_id, workflow_revision_seen,
    created_at_unix_ms, updated_at_unix_ms, started_at_unix_ms, completed_at_unix_ms
) VALUES (
    'run-pending-approval-migration',
    'placement-pending-approval-migration',
    '550e8400-e29b-41d4-a716-446655440002',
    1,
    ?, ?, ?, ?
)`, now, now+2, now+1, now+2)
	execSeed(t, db, "pending approval transition", `
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id,
    source_node_key, source_node_display_name, transition_id, transition_display_name, workflow_revision_seen,
    actor, state, commentary, output_values_json, created_at_unix_ms
) VALUES (
    'transition-pending-approval-migration',
    'task-pending-approval-migration',
    'run-pending-approval-migration',
    'placement-pending-approval-migration',
    'agent',
    'Agent',
    'done',
    'Done',
    1,
    'agent',
    'pending_approval',
    '',
    '{"summary":"done"}',
    ?
)`, now+3)
	execSeed(t, db, "pending approval edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-pending-approval-migration',
    'transition-pending-approval-migration',
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
    '{"context_mode":"new_session","context_source":{"kind":"immediate_source"},"context_resolution_frozen":true}'
)`)
	execSeed(t, db, "delete mutable approval graph", `
DELETE FROM workflow_transition_groups
WHERE id = 'group-done'`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var sourceNodeID, sourceSessionID string
	var sourceSchedulingState sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT node_id, session_id, scheduling_state
FROM task_current_nodes
WHERE task_id = 'task-pending-approval-migration'`).Scan(
		&sourceNodeID,
		&sourceSessionID,
		&sourceSchedulingState,
	); err != nil {
		t.Fatalf("query pending approval source current node: %v", err)
	}
	if sourceNodeID != "node-agent" ||
		sourceSessionID != "550e8400-e29b-41d4-a716-446655440002" ||
		sourceSchedulingState.Valid {
		t.Fatalf(
			"pending approval source current node = node=%q session=%q scheduling=%+v",
			sourceNodeID,
			sourceSessionID,
			sourceSchedulingState,
		)
	}

	var (
		approvalID, approvalSourceTaskID, approvalSourceNodeID, approvalSourceSessionID, materializedValues string
		workflowVersion, createdAt                                                                          int64
		transitionWorkflowID, transitionGroupID, transitionSourceNodeID, transitionID, sourceDisplayName    string
		branchKey, targetNodeID, targetDisplayName, targetInputs, targetPriorValues                         string
		targetSessionID, targetSchedulingState                                                              string
		edgeID, edgeKey, edgeTargetNodeID, edgeContextMode, edgeContextSourceKind                           string
		edgeRequiresApproval                                                                                int
		resolutionSessionID                                                                                 string
	)
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    approval.id,
    approval.source_task_id,
    approval.source_node_id,
    approval.source_session_id,
    approval.workflow_version,
    approval.materialized_values_json,
    approval.created_at_unix_ms,
    json_extract(approval.transition_snapshot_json, '$.workflow_id'),
    json_extract(approval.transition_snapshot_json, '$.id'),
    json_extract(approval.transition_snapshot_json, '$.source_node_id'),
    json_extract(approval.transition_snapshot_json, '$.transition_id'),
    json_extract(approval.transition_snapshot_json, '$.source_display_name'),
    branch.transition_branch_key,
    json_extract(branch.target_snapshot_json, '$.node_id'),
    json_extract(branch.target_snapshot_json, '$.display_name'),
    json_extract(branch.target_snapshot_json, '$.current_input_values'),
    json_extract(branch.target_snapshot_json, '$.prior_node_values'),
    COALESCE(json_extract(branch.target_snapshot_json, '$.session_id'), ''),
    COALESCE(json_extract(branch.target_snapshot_json, '$.scheduling_state'), ''),
    json_extract(branch.effective_edge_configuration_json, '$.id'),
    json_extract(branch.effective_edge_configuration_json, '$.key'),
    json_extract(branch.effective_edge_configuration_json, '$.target_node_id'),
    json_extract(branch.effective_edge_configuration_json, '$.context_mode'),
    json_extract(branch.effective_edge_configuration_json, '$.context_source.kind'),
    json_extract(branch.effective_edge_configuration_json, '$.requires_approval'),
    COALESCE(json_extract(branch.context_source_resolution_json, '$.session_id'), '')
FROM task_pending_approvals approval
JOIN task_pending_approval_branches branch ON branch.approval_id = approval.id
WHERE approval.source_task_id = 'task-pending-approval-migration'`).Scan(
		&approvalID,
		&approvalSourceTaskID,
		&approvalSourceNodeID,
		&approvalSourceSessionID,
		&workflowVersion,
		&materializedValues,
		&createdAt,
		&transitionWorkflowID,
		&transitionGroupID,
		&transitionSourceNodeID,
		&transitionID,
		&sourceDisplayName,
		&branchKey,
		&targetNodeID,
		&targetDisplayName,
		&targetInputs,
		&targetPriorValues,
		&targetSessionID,
		&targetSchedulingState,
		&edgeID,
		&edgeKey,
		&edgeTargetNodeID,
		&edgeContextMode,
		&edgeContextSourceKind,
		&edgeRequiresApproval,
		&resolutionSessionID,
	); err != nil {
		t.Fatalf("query migrated pending approval snapshot: %v", err)
	}
	if _, err := workflow.ParseApprovalID(approvalID); err != nil {
		t.Fatalf("migrated approval id %q: %v", approvalID, err)
	}
	if approvalSourceTaskID != "task-pending-approval-migration" ||
		approvalSourceNodeID != "node-agent" ||
		approvalSourceSessionID != "550e8400-e29b-41d4-a716-446655440002" ||
		workflowVersion != 1 ||
		materializedValues != `{"summary":"done"}` ||
		createdAt != now+3 ||
		transitionWorkflowID != "workflow-1" ||
		transitionGroupID != "transition-pending-approval-migration" ||
		transitionSourceNodeID != "node-agent" ||
		transitionID != "done" ||
		sourceDisplayName != "Agent" ||
		branchKey != "done" ||
		targetNodeID != "node-done" ||
		targetDisplayName != "Done" ||
		targetInputs != "{}" ||
		targetPriorValues != "{}" ||
		targetSessionID != "" ||
		targetSchedulingState != "" ||
		edgeID != "transition-edge-pending-approval-migration" ||
		edgeKey != "done" ||
		edgeTargetNodeID != "node-done" ||
		edgeContextMode != "new_session" ||
		edgeContextSourceKind != "immediate_source" ||
		edgeRequiresApproval != 1 ||
		resolutionSessionID != "" {
		t.Fatalf(
			"migrated pending approval = id=%q source=%q/%q/%q version=%d values=%q created=%d transition=%q/%q/%q/%q/%q branch=%q target=%q/%q/%q/%q/%q/%q edge=%q/%q/%q/%q/%q/%d resolution=%q",
			approvalID,
			approvalSourceTaskID,
			approvalSourceNodeID,
			approvalSourceSessionID,
			workflowVersion,
			materializedValues,
			createdAt,
			transitionWorkflowID,
			transitionGroupID,
			transitionSourceNodeID,
			transitionID,
			sourceDisplayName,
			branchKey,
			targetNodeID,
			targetDisplayName,
			targetInputs,
			targetPriorValues,
			targetSessionID,
			targetSchedulingState,
			edgeID,
			edgeKey,
			edgeTargetNodeID,
			edgeContextMode,
			edgeContextSourceKind,
			edgeRequiresApproval,
			resolutionSessionID,
		)
	}
}

func TestOpenRejectsPendingApprovalWithoutCurrentSource(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-orphan-approval-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-orphan-approval-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-orphan-approval-migration", "link-1", 1, "APR-2", now, now)
	execSeed(t, db, "pending approval transition", `
INSERT INTO task_transitions (
    id, task_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms
) VALUES (
    'transition-orphan-approval-migration',
    'task-orphan-approval-migration',
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
)`, now)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	_, err = Open(root)
	if err == nil {
		t.Fatal("Open unexpectedly accepted a pending Approval without a Current Node source")
	}
	if !strings.Contains(err.Error(), "task-orphan-approval-migration") ||
		!strings.Contains(err.Error(), "transition-orphan-approval-migration") {
		t.Fatalf("Open error = %v, want Task and transition diagnostic", err)
	}
}

func TestOpenProjectsNormalizesCanceledTaskToCanonicalDone(t *testing.T) {
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
SELECT node_id, scheduling_state
FROM task_current_nodes
WHERE task_id = 'task-canceled-done-migration'`).Scan(&nodeID, &schedulingState); err != nil {
		t.Fatalf("query normalized canceled current node: %v", err)
	}
	if nodeID != "node-done" || schedulingState.Valid {
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
SELECT session.task_id, association.node_id, association.associated_at_unix_ms
FROM sessions session
JOIN session_workflow_node_associations association ON association.session_id = session.id
WHERE session.id = '550e8400-e29b-41d4-a716-446655440008'`).Scan(&taskID, &associationNodeID, &associatedAt); err != nil {
		t.Fatalf("query normalized canceled Session association: %v", err)
	}
	if taskID != "task-canceled-done-migration" ||
		associationNodeID != "node-agent" ||
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
    (SELECT node_id FROM task_current_nodes WHERE task_id = 'task-canceled-approval-migration'),
    (SELECT COUNT(*) FROM task_pending_approvals WHERE source_task_id = 'task-canceled-approval-migration'),
    (SELECT COUNT(*) FROM task_active_fanouts WHERE task_id = 'task-canceled-approval-migration')`).Scan(
		&nodeID,
		&approvalCount,
		&fanoutCount,
	); err != nil {
		t.Fatalf("query normalized canceled approval aggregate: %v", err)
	}
	if nodeID != "node-done" || approvalCount != 0 || fanoutCount != 0 {
		t.Fatalf("normalized canceled approval aggregate = node=%q approvals=%d fanouts=%d", nodeID, approvalCount, fanoutCount)
	}
}

func TestOpenProjectsCanceledTaskWithoutCanonicalDonePreservesUniqueActiveTerminal(t *testing.T) {
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
SELECT node_id
FROM task_current_nodes
WHERE task_id = 'task-canceled-terminal-migration'`).Scan(&nodeID); err != nil {
		t.Fatalf("query normalized selected terminal current node: %v", err)
	}
	if nodeID != "node-done" {
		t.Fatalf("normalized selected terminal current node = %q, want node-done", nodeID)
	}
}

func TestOpenProjectsCanceledTaskWithoutCanonicalDoneWithNonterminalLegacyCandidate(t *testing.T) {
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

	assertMigratedCurrentNodeID(t, store, "task-canceled-invalid-terminal-migration", "node-done")
}

func TestOpenProjectsCanceledTaskWithoutCanonicalDoneWithoutLegacyCandidate(t *testing.T) {
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

	assertMigratedCurrentNodeID(t, store, "task-canceled-missing-terminal-migration", "node-done")
}

func TestOpenProjectsCanceledTaskWithoutCanonicalDoneWithAmbiguousLegacyCandidates(t *testing.T) {
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
VALUES ('node-terminal-alternate', 'workflow-1', 'alternate', 'terminal', 'Alternate', '[]')`)
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

	assertMigratedCurrentNodeID(t, store, "task-canceled-ambiguous-terminal-migration", "node-done")
}

func TestOpenProjectsCanceledTaskWithoutCanonicalDoneWithForeignLegacyCandidate(t *testing.T) {
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

	assertMigratedCurrentNodeID(t, store, "task-canceled-foreign-terminal-migration", "node-done")
}

func assertMigratedCurrentNodeID(t *testing.T, store *Store, taskID, wantNodeID string) {
	t.Helper()

	var gotNodeID string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT node_id
FROM task_current_nodes
WHERE task_id = ?`, taskID).Scan(&gotNodeID); err != nil {
		t.Fatalf("query migrated current node for task %q: %v", taskID, err)
	}
	if gotNodeID != wantNodeID {
		t.Fatalf("migrated current node for task %q = %q, want %q", taskID, gotNodeID, wantNodeID)
	}
}

func TestOpenOmitsInvalidCanceledTaskWithoutCanonicalDoneAndRetainsNeutralSession(t *testing.T) {
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

func TestOpenProjectsMigratesActiveParallelFanout(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-parallel-migration", "task-parallel-migration", now)
	execSeed(t, db, "runnable parallel branch", `
DELETE FROM task_runs
WHERE id = 'run-parallel-b'`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var fanoutCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM task_active_fanouts
WHERE task_id = 'task-parallel-migration'`).Scan(&fanoutCount); err != nil {
		t.Fatalf("count migrated active fanout: %v", err)
	}
	if fanoutCount != 1 {
		t.Fatalf("migrated active fanout count = %d, want 1", fanoutCount)
	}

	rows, err := store.db.QueryContext(t.Context(), `
SELECT
    branch.transition_branch_key,
    branch.arrival_state,
    current_node.node_id,
    current_node.scheduling_state
FROM task_active_fanout_branches branch
JOIN task_current_nodes current_node
    ON current_node.task_id = branch.task_id
   AND current_node.transition_branch_key = branch.transition_branch_key
WHERE branch.task_id = 'task-parallel-migration'
ORDER BY branch.transition_branch_key`)
	if err != nil {
		t.Fatalf("query migrated parallel branches: %v", err)
	}
	defer func() { _ = rows.Close() }()
	type branchState struct {
		arrivalState    string
		currentNodeID   string
		schedulingState string
	}
	branches := map[string]branchState{}
	for rows.Next() {
		var key string
		var state branchState
		if err := rows.Scan(&key, &state.arrivalState, &state.currentNodeID, &state.schedulingState); err != nil {
			t.Fatalf("scan migrated parallel branch: %v", err)
		}
		branches[key] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated parallel branches: %v", err)
	}
	want := map[string]branchState{
		"split_a": {arrivalState: "pending", currentNodeID: "node-branch-a", schedulingState: "interrupted"},
		"split_b": {arrivalState: "pending", currentNodeID: "node-branch-b", schedulingState: "interrupted"},
	}
	if !reflect.DeepEqual(branches, want) {
		t.Fatalf("migrated parallel branches = %+v, want %+v", branches, want)
	}
}

func TestOpenProjectsRetainsParallelSessionBranchAssociation(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	const sessionID = "550e8400-e29b-41d4-a716-446655440007"
	seedLegacyParallelMigrationFixture(
		t,
		db,
		"project-parallel-session-association-migration",
		"task-parallel-session-association-migration",
		now,
	)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-parallel-session-association-migration",
		"workspace-parallel-session-association-migration",
		sessionID,
		now,
	)
	execSeed(t, db, "parallel branch session", `
UPDATE task_runs
SET session_id = ?
WHERE id = 'run-parallel-a'`, sessionID)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var taskID, nodeID, branchKey string
	var associatedAt int64
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    session.task_id,
    association.node_id,
    association.transition_branch_key,
    association.associated_at_unix_ms
FROM sessions session
JOIN session_workflow_node_associations association ON association.session_id = session.id
WHERE session.id = ?`, sessionID).Scan(&taskID, &nodeID, &branchKey, &associatedAt); err != nil {
		t.Fatalf("query migrated parallel Session association: %v", err)
	}
	if taskID != "task-parallel-session-association-migration" ||
		nodeID != "node-branch-a" ||
		branchKey != "split_a" ||
		associatedAt != now+4 {
		t.Fatalf(
			"migrated parallel Session association = task=%q node=%q branch=%q associated_at=%d",
			taskID,
			nodeID,
			branchKey,
			associatedAt,
		)
	}
}

func TestOpenProjectsDiscardsCompletedParallelHistory(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-completed-parallel-history", "task-completed-parallel-history", now)
	execSeed(t, db, "complete parallel history", `
UPDATE task_node_placements
SET state = 'completed', updated_at_unix_ms = ?
WHERE id IN ('placement-parallel-a', 'placement-parallel-b');
UPDATE task_runs
SET completed_at_unix_ms = ?, updated_at_unix_ms = ?
WHERE id IN ('run-parallel-a', 'run-parallel-b');
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-completed-parallel-terminal',
    'task-completed-parallel-history',
    'node-done',
    'active',
    ?,
    ?
)`, now+5, now+5, now+5, now+5)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	var fanoutCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    (SELECT node_id FROM task_current_nodes WHERE task_id = 'task-completed-parallel-history'),
    (SELECT COUNT(*) FROM task_active_fanouts WHERE task_id = 'task-completed-parallel-history')`).Scan(&nodeID, &fanoutCount); err != nil {
		t.Fatalf("query discarded completed parallel history: %v", err)
	}
	if nodeID != "node-done" || fanoutCount != 0 {
		t.Fatalf("discarded completed parallel history = node=%q fanouts=%d", nodeID, fanoutCount)
	}
}

func TestOpenProjectsDiscardsCanceledParallelFanout(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-canceled-parallel-migration", "task-canceled-parallel-migration", now)
	execSeed(t, db, "canceled task", `
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-parallel-migration'`, now+5)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	var fanoutCount, approvalCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    (SELECT node_id FROM task_current_nodes WHERE task_id = 'task-canceled-parallel-migration'),
    (SELECT COUNT(*) FROM task_active_fanouts WHERE task_id = 'task-canceled-parallel-migration'),
    (SELECT COUNT(*) FROM task_pending_approvals WHERE source_task_id = 'task-canceled-parallel-migration')`).Scan(
		&nodeID,
		&fanoutCount,
		&approvalCount,
	); err != nil {
		t.Fatalf("query normalized canceled parallel aggregate: %v", err)
	}
	if nodeID != "node-done" || fanoutCount != 0 || approvalCount != 0 {
		t.Fatalf("normalized canceled parallel aggregate = node=%q fanouts=%d approvals=%d", nodeID, fanoutCount, approvalCount)
	}
}

func TestOpenProjectsDiscardsCanceledParallelPendingApproval(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-canceled-parallel-approval-migration", "task-canceled-parallel-approval-migration", now)
	execSeed(t, db, "approval edge", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES ('group-canceled-branch-a-done', 'node-branch-a', 'done', 'Done');
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, requires_approval,
    context_mode, input_bindings_json, output_requirements_json
) VALUES (
    'edge-canceled-branch-a-done',
    'group-canceled-branch-a-done',
    'done',
    'node-done',
    1,
    'new_session',
    '[]',
    '[]'
)`)
	execSeed(t, db, "waiting branch approval", `
UPDATE task_node_placements
SET state = 'waiting_approval', updated_at_unix_ms = ?
WHERE id = 'placement-parallel-a';
UPDATE task_runs
SET completed_at_unix_ms = ?, updated_at_unix_ms = ?
WHERE id = 'run-parallel-a';
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms
) VALUES (
    'transition-canceled-parallel-approval-a',
    'task-canceled-parallel-approval-migration',
    'run-parallel-a',
    'placement-parallel-a',
    'branch_a',
    'Branch A',
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
    'transition-edge-canceled-parallel-approval-a',
    'transition-canceled-parallel-approval-a',
    'edge-canceled-branch-a-done',
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
);
UPDATE tasks
SET canceled_at_unix_ms = ?
WHERE id = 'task-canceled-parallel-approval-migration'`, now+5, now+5, now+5, now+6)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID string
	var currentNodeCount, approvalCount, fanoutCount, attentionCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    (SELECT node_id FROM task_current_nodes WHERE task_id = 'task-canceled-parallel-approval-migration'),
    (SELECT COUNT(*) FROM task_current_nodes WHERE task_id = 'task-canceled-parallel-approval-migration'),
    (SELECT COUNT(*) FROM task_pending_approvals WHERE source_task_id = 'task-canceled-parallel-approval-migration'),
    (SELECT COUNT(*) FROM task_active_fanouts WHERE task_id = 'task-canceled-parallel-approval-migration'),
    (SELECT COUNT(*) FROM workflow_attention_candidates WHERE task_id = 'task-canceled-parallel-approval-migration')`).Scan(
		&nodeID,
		&currentNodeCount,
		&approvalCount,
		&fanoutCount,
		&attentionCount,
	); err != nil {
		t.Fatalf("query normalized canceled parallel approval aggregate: %v", err)
	}
	if nodeID != "node-done" ||
		currentNodeCount != 1 ||
		approvalCount != 0 ||
		fanoutCount != 0 ||
		attentionCount != 0 {
		t.Fatalf(
			"normalized canceled parallel approval aggregate = node=%q current_nodes=%d approvals=%d fanouts=%d attention=%d",
			nodeID,
			currentNodeCount,
			approvalCount,
			fanoutCount,
			attentionCount,
		)
	}
}

func TestOpenProjectsMigratesPartialParallelJoinArrival(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-parallel-arrival-migration", "task-parallel-arrival-migration", now)
	execSeed(t, db, "join node", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, join_input_providers_json, output_fields_json
) VALUES (
    'node-join',
    'workflow-1',
    'join',
    'join',
    'Join',
    '[]',
    '[]'
)`)
	execSeed(t, db, "join edges", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES
    ('group-branch-a-join', 'node-branch-a', 'join', 'Join'),
    ('group-branch-b-join', 'node-branch-b', 'join', 'Join');
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode, input_bindings_json, output_requirements_json
) VALUES
    ('edge-branch-a-join', 'group-branch-a-join', 'join_a', 'node-join', 'new_session', '[]', '[]'),
    ('edge-branch-b-join', 'group-branch-b-join', 'join_b', 'node-join', 'new_session', '[]', '[]')`)
	execSeed(t, db, "completed branch", `
UPDATE task_node_placements
SET state = 'completed', updated_at_unix_ms = ?
WHERE id = 'placement-parallel-a';
UPDATE task_runs
SET completed_at_unix_ms = ?, updated_at_unix_ms = ?
WHERE id = 'run-parallel-a'`, now+5, now+5, now+5)
	execSeed(t, db, "join arrival transition", `
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (
    'transition-parallel-arrival-a',
    'task-parallel-arrival-migration',
    'run-parallel-a',
    'placement-parallel-a',
    'branch_a',
    'Branch A',
    'join',
    'Join',
    1,
    'agent',
    'applied',
    '',
    '{"joined":"a"}',
    ?,
    ?
);
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (
    'transition-edge-parallel-arrival-a',
    'transition-parallel-arrival-a',
    'edge-branch-a-join',
    'join_a',
    'node-join',
    'join',
    'Join',
    'join',
    'applied',
    'new_session',
    0,
    '[]',
    '[]',
    '{}'
)`, now+5, now+5)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rows, err := store.db.QueryContext(t.Context(), `
SELECT transition_branch_key, arrival_state, arrival_values_json
FROM task_active_fanout_branches
WHERE task_id = 'task-parallel-arrival-migration'
ORDER BY transition_branch_key`)
	if err != nil {
		t.Fatalf("query migrated parallel join arrivals: %v", err)
	}
	defer func() { _ = rows.Close() }()
	type arrival struct {
		state  string
		values sql.NullString
	}
	arrivals := map[string]arrival{}
	for rows.Next() {
		var key string
		var item arrival
		if err := rows.Scan(&key, &item.state, &item.values); err != nil {
			t.Fatalf("scan migrated parallel join arrival: %v", err)
		}
		arrivals[key] = item
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated parallel join arrivals: %v", err)
	}
	want := map[string]arrival{
		"split_a": {state: "arrived", values: sql.NullString{String: `{"joined":"a"}`, Valid: true}},
		"split_b": {state: "pending"},
	}
	if !reflect.DeepEqual(arrivals, want) {
		t.Fatalf("migrated parallel join arrivals = %+v, want %+v", arrivals, want)
	}

	var currentNodeID, currentBranchKey string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT node_id, transition_branch_key
FROM task_current_nodes
WHERE task_id = 'task-parallel-arrival-migration'`).Scan(&currentNodeID, &currentBranchKey); err != nil {
		t.Fatalf("query remaining current branch: %v", err)
	}
	if currentNodeID != "node-branch-b" || currentBranchKey != "split_b" {
		t.Fatalf("remaining current branch = node=%q branch=%q", currentNodeID, currentBranchKey)
	}
}

func TestOpenProjectsMigratesPendingApprovalOnParallelBranch(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-parallel-approval-migration", "task-parallel-approval-migration", now)
	execSeed(t, db, "approval edge", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES ('group-branch-a-done', 'node-branch-a', 'done', 'Done');
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, requires_approval,
    context_mode, input_bindings_json, output_requirements_json
) VALUES (
    'edge-branch-a-done',
    'group-branch-a-done',
    'done',
    'node-done',
    1,
    'new_session',
    '[]',
    '[]'
)`)
	execSeed(t, db, "waiting branch approval", `
UPDATE task_node_placements
SET state = 'waiting_approval', updated_at_unix_ms = ?
WHERE id = 'placement-parallel-a';
UPDATE task_runs
SET completed_at_unix_ms = ?, updated_at_unix_ms = ?
WHERE id = 'run-parallel-a';
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms
) VALUES (
    'transition-parallel-approval-a',
    'task-parallel-approval-migration',
    'run-parallel-a',
    'placement-parallel-a',
    'branch_a',
    'Branch A',
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
    'transition-edge-parallel-approval-a',
    'transition-parallel-approval-a',
    'edge-branch-a-done',
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
)`, now+5, now+5, now+5)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var sourceNodeID, sourceBranchKey string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT source_node_id, source_transition_branch_key
FROM task_pending_approvals
WHERE source_task_id = 'task-parallel-approval-migration'`).Scan(&sourceNodeID, &sourceBranchKey); err != nil {
		t.Fatalf("query migrated parallel pending approval: %v", err)
	}
	if sourceNodeID != "node-branch-a" || sourceBranchKey != "split_a" {
		t.Fatalf("migrated parallel pending approval source = node=%q branch=%q", sourceNodeID, sourceBranchKey)
	}

	var targetBranchKey string
	if err := store.db.QueryRowContext(t.Context(), `
SELECT json_extract(branch.target_snapshot_json, '$.transition_branch_key')
FROM task_pending_approval_branches branch
JOIN task_pending_approvals approval ON approval.id = branch.approval_id
WHERE approval.source_task_id = 'task-parallel-approval-migration'`).Scan(&targetBranchKey); err != nil {
		t.Fatalf("query migrated parallel approval target: %v", err)
	}
	if targetBranchKey != "split_a" {
		t.Fatalf("migrated parallel approval target branch = %q, want split_a", targetBranchKey)
	}

	rows, err := store.db.QueryContext(t.Context(), `
SELECT node_id, transition_branch_key, scheduling_state
FROM task_current_nodes
WHERE task_id = 'task-parallel-approval-migration'
ORDER BY transition_branch_key`)
	if err != nil {
		t.Fatalf("query migrated parallel approval current nodes: %v", err)
	}
	defer func() { _ = rows.Close() }()
	type currentNodeState struct {
		nodeID     string
		scheduling sql.NullString
	}
	currentNodes := map[string]currentNodeState{}
	for rows.Next() {
		var branch string
		var state currentNodeState
		if err := rows.Scan(&state.nodeID, &branch, &state.scheduling); err != nil {
			t.Fatalf("scan migrated parallel approval current node: %v", err)
		}
		currentNodes[branch] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated parallel approval current nodes: %v", err)
	}
	if branch := currentNodes["split_a"]; branch.nodeID != "node-branch-a" || branch.scheduling.Valid {
		t.Fatalf("pending approval branch current node = %+v", branch)
	}
	if branch := currentNodes["split_b"]; branch.nodeID != "node-branch-b" || !branch.scheduling.Valid || branch.scheduling.String != "interrupted" {
		t.Fatalf("sibling branch current node = %+v", branch)
	}
}

func TestOpenProjectsMigratesSeveralPendingApprovalsOnParallelBranches(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	seedLegacyParallelMigrationFixture(t, db, "project-parallel-approvals-migration", "task-parallel-approvals-migration", now)
	for _, branch := range []struct {
		key       string
		nodeID    string
		placement string
		runID     string
	}{
		{"a", "node-branch-a", "placement-parallel-a", "run-parallel-a"},
		{"b", "node-branch-b", "placement-parallel-b", "run-parallel-b"},
	} {
		execSeed(t, db, "approval transition group", `
INSERT INTO workflow_transition_groups (id, source_node_id, transition_id, display_name)
VALUES (?, ?, 'done', 'Done')`,
			"group-parallel-"+branch.key+"-done",
			branch.nodeID,
		)
		execSeed(t, db, "approval edge", `
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, requires_approval,
    context_mode, input_bindings_json, output_requirements_json
) VALUES (?, ?, 'done', ?, 1, 'new_session', '[]', '[]')`,
			"edge-parallel-"+branch.key+"-done",
			"group-parallel-"+branch.key+"-done",
			branch.nodeID,
		)
		execSeed(t, db, "waiting branch placement", `
UPDATE task_node_placements SET state = 'waiting_approval' WHERE id = ?`, branch.placement)
		execSeed(t, db, "completed branch run", `
UPDATE task_runs SET completed_at_unix_ms = ?, updated_at_unix_ms = ? WHERE id = ?`,
			now+5,
			now+5,
			branch.runID,
		)
		execSeed(t, db, "pending branch transition", `
INSERT INTO task_transitions (
    id, task_id, source_run_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms
) VALUES (?, 'task-parallel-approvals-migration', ?, ?, ?, ?, 'done', 'Done', 1, 'agent', 'pending_approval', '', '{}', ?)`,
			"transition-parallel-"+branch.key+"-approval",
			branch.runID,
			branch.placement,
			"branch_"+branch.key,
			"Branch "+strings.ToUpper(branch.key),
			now+5,
		)
		execSeed(t, db, "pending branch transition edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    state, context_mode, requires_approval, input_bindings_json, output_requirements_json, metadata_json
) VALUES (?, ?, ?, 'done', 'node-done', 'done', 'Done', 'terminal', 'pending', 'new_session', 1, '[]', '[]', '{}')`,
			"transition-edge-parallel-"+branch.key+"-approval",
			"transition-parallel-"+branch.key+"-approval",
			"edge-parallel-"+branch.key+"-done",
		)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var approvalCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM task_pending_approvals
WHERE source_task_id = 'task-parallel-approvals-migration'`).Scan(&approvalCount); err != nil {
		t.Fatalf("count migrated parallel approvals: %v", err)
	}
	if approvalCount != 2 {
		t.Fatalf("migrated parallel approval count = %d, want 2", approvalCount)
	}
}

func seedLegacyParallelMigrationFixture(t *testing.T, db *sql.DB, projectID, taskID string, now int64) {
	t.Helper()
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES (?, 'Project', ?, ?, '{}')`, projectID, now, now)
	seedWorkflowGraph(t, db, projectID, now)
	execSeed(t, db, "branch nodes", `
INSERT INTO workflow_nodes (
    id, workflow_id, node_key, kind, display_name, subagent_role, prompt_template, output_fields_json
) VALUES (
    'node-branch-a',
    'workflow-1',
    'branch_a',
    'agent',
    'Branch A',
    'coder',
    'A.',
    '[]'
), (
    'node-branch-b',
    'workflow-1',
    'branch_b',
    'agent',
    'Branch B',
    'coder',
    'B.',
    '[]'
)`)
	execSeed(t, db, "fanout edges", `
DELETE FROM workflow_edges
WHERE id = 'edge-done-1';
INSERT INTO workflow_edges (
    id, transition_group_id, edge_key, target_node_id, context_mode, input_bindings_json, output_requirements_json
) VALUES (
    'edge-split-a',
    'group-done',
    'split_a',
    'node-branch-a',
    'new_session',
    '[]',
    '[]'
), (
    'edge-split-b',
    'group-done',
    'split_b',
    'node-branch-b',
    'new_session',
    '[]',
    '[]'
)`)
	execSeed(t, db, "task", workflowSeedTaskSQL, taskID, "link-1", 1, "PAR-1", now, now)
	execSeed(t, db, "fanout source placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'placement-parallel-source',
    ?,
    'node-agent',
    'completed',
    ?,
    ?
)`, taskID, now, now+1)
	execSeed(t, db, "fanout transition", `
INSERT INTO task_transitions (
    id, task_id, source_placement_id, source_node_key, source_node_display_name,
    transition_id, transition_display_name, workflow_revision_seen, actor, state,
    commentary, output_values_json, created_at_unix_ms, applied_at_unix_ms
) VALUES (
    'transition-parallel-split',
    ?,
    'placement-parallel-source',
    'agent',
    'Agent',
    'done',
    'Done',
    1,
    'agent',
    'applied',
    '',
    '{}',
    ?,
    ?
)`, taskID, now+2, now+2)
	for _, branch := range []struct {
		placementID string
		nodeID      string
		edgeID      string
		edgeKey     string
		runID       string
	}{
		{"placement-parallel-a", "node-branch-a", "edge-split-a", "split_a", "run-parallel-a"},
		{"placement-parallel-b", "node-branch-b", "edge-split-b", "split_b", "run-parallel-b"},
	} {
		execSeed(t, db, "parallel placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, parallel_batch_transition_id, parallel_branch_edge_id,
    created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, ?, 'active', 'transition-parallel-split', ?, ?, ?)`,
			branch.placementID,
			taskID,
			branch.nodeID,
			branch.edgeID,
			now+3,
			now+3,
		)
		execSeed(t, db, "parallel run", `
INSERT INTO task_runs (
    id, placement_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, 1, ?, ?)`, branch.runID, branch.placementID, now+3, now+4)
		execSeed(t, db, "fanout transition edge", `
INSERT INTO task_transition_edges (
    id, task_transition_id, workflow_edge_id, edge_key,
    target_node_id, target_node_key, target_node_display_name, target_node_kind,
    target_placement_id, state, context_mode, requires_approval,
    input_bindings_json, output_requirements_json, metadata_json
) VALUES (?, 'transition-parallel-split', ?, ?, ?, ?, ?, 'agent', ?, 'applied', 'new_session', 0, '[]', '[]', '{}')`,
			"transition-edge-"+branch.edgeKey,
			branch.edgeID,
			branch.edgeKey,
			branch.nodeID,
			branch.edgeKey,
			"Branch "+string(branch.edgeKey[len(branch.edgeKey)-1]),
			branch.placementID,
		)
	}
}

func TestOpenProjectsLegacyActiveScriptRunToInterruptedCurrentNode(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	runUpdatedAt := now + 9
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-active-script-migration', 'Project', ?, ?, '{}')`, now, now)
	seedLegacyWorkflowSession(
		t,
		db,
		"project-active-script-migration",
		"workspace-active-script-migration",
		"550e8400-e29b-41d4-a716-446655440006",
		now,
	)
	seedWorkflowGraph(t, db, "project-active-script-migration", now)
	execSeed(t, db, "script node", `
UPDATE workflow_nodes
SET kind = 'script', script_path = 'scripts/migrate'
WHERE id = 'node-agent'`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-active-script-migration", "link-1", 1, "SCR-1", now, now)
	execSeed(t, db, "script placement", workflowSeedPlacementSQL, "placement-active-script-migration", "task-active-script-migration", "node-agent", now, now)
	execSeed(t, db, "active script run", `
INSERT INTO task_runs (
    id,
    placement_id,
    session_id,
    workflow_revision_seen,
    created_at_unix_ms,
    updated_at_unix_ms
) VALUES (
    'run-active-script-migration',
    'placement-active-script-migration',
    '550e8400-e29b-41d4-a716-446655440006',
    1,
    ?,
    ?
)`, now, runUpdatedAt)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var nodeID, schedulingState, interruptionReason, interruptionDetail string
	var interruptedAt int64
	var sessionID sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT
    node_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms,
    session_id
FROM task_current_nodes
WHERE task_id = 'task-active-script-migration'`).Scan(
		&nodeID,
		&schedulingState,
		&interruptionReason,
		&interruptionDetail,
		&interruptedAt,
		&sessionID,
	); err != nil {
		t.Fatalf("query projected active script current node: %v", err)
	}
	if nodeID != "node-agent" ||
		schedulingState != "interrupted" ||
		interruptionReason != "server_restart" ||
		interruptionDetail != `{"code":"workflow.execution.restarted","fields":{"operation":"recovery"}}` ||
		interruptedAt != runUpdatedAt ||
		sessionID.Valid {
		t.Fatalf(
			"projected active script current node = node=%q scheduling=%q reason=%q detail=%q interrupted_at=%d session=%+v",
			nodeID,
			schedulingState,
			interruptionReason,
			interruptionDetail,
			interruptedAt,
			sessionID,
		)
	}

	var taskID sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT task_id
FROM sessions
WHERE id = '550e8400-e29b-41d4-a716-446655440006'`).Scan(&taskID); err != nil {
		t.Fatalf("query projected active script Session owner: %v", err)
	}
	if taskID.Valid {
		t.Fatalf("projected active script Session owner = %q, want workflow-neutral", taskID.String)
	}
	var associationCount int
	if err := store.db.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM session_workflow_node_associations
WHERE session_id = '550e8400-e29b-41d4-a716-446655440006'`).Scan(&associationCount); err != nil {
		t.Fatalf("count projected active script Session associations: %v", err)
	}
	if associationCount != 0 {
		t.Fatalf("projected active script Session association count = %d, want 0", associationCount)
	}
}

func TestOpenProjectsPreservesLegacyInterruptedAgentRun(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	interruptedAt := now + 5
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-interrupted-agent-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-interrupted-agent-migration", now)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-interrupted-agent-migration", "link-1", 1, "INT-1", now, now)
	execSeed(t, db, "agent placement", workflowSeedPlacementSQL, "placement-interrupted-agent-migration", "task-interrupted-agent-migration", "node-agent", now, now)
	execSeed(t, db, "interrupted agent run", `
INSERT INTO task_runs (
    id,
    placement_id,
    workflow_revision_seen,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json
) VALUES (
    'run-interrupted-agent-migration',
    'placement-interrupted-agent-migration',
    1,
    ?,
    ?,
    ?,
    ?,
    'user_interrupt',
    '{"code":"workflow.execution.interrupted","fields":{"operation":"interrupt"}}'
)`, now, now+9, now+1, interruptedAt)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var schedulingState, interruptionReason, interruptionDetail string
	var projectedInterruptedAt int64
	if err := store.db.QueryRowContext(t.Context(), `
SELECT scheduling_state, interruption_reason, interruption_detail_json, interrupted_at_unix_ms
FROM task_current_nodes
WHERE task_id = 'task-interrupted-agent-migration'`).Scan(
		&schedulingState,
		&interruptionReason,
		&interruptionDetail,
		&projectedInterruptedAt,
	); err != nil {
		t.Fatalf("query projected interrupted agent current node: %v", err)
	}
	if schedulingState != "interrupted" ||
		interruptionReason != "user_interrupt" ||
		interruptionDetail != `{"code":"workflow.execution.interrupted","fields":{"operation":"interrupt"}}` ||
		projectedInterruptedAt != interruptedAt {
		t.Fatalf(
			"projected interrupted agent current node = scheduling=%q reason=%q detail=%q interrupted_at=%d",
			schedulingState,
			interruptionReason,
			interruptionDetail,
			projectedInterruptedAt,
		)
	}
}

func TestOpenProjectsPreservesLegacyInterruptedScriptRun(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	interruptedAt := now + 6
	execSeed(t, db, "project", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-interrupted-script-migration', 'Project', ?, ?, '{}')`, now, now)
	seedWorkflowGraph(t, db, "project-interrupted-script-migration", now)
	execSeed(t, db, "script node", `
UPDATE workflow_nodes
SET kind = 'script', script_path = 'scripts/migrate'
WHERE id = 'node-agent'`)
	execSeed(t, db, "task", workflowSeedTaskSQL, "task-interrupted-script-migration", "link-1", 1, "INT-2", now, now)
	execSeed(t, db, "script placement", workflowSeedPlacementSQL, "placement-interrupted-script-migration", "task-interrupted-script-migration", "node-agent", now, now)
	execSeed(t, db, "interrupted script run", `
INSERT INTO task_runs (
    id,
    placement_id,
    workflow_revision_seen,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    interrupted_at_unix_ms,
    interruption_reason,
    interruption_detail_json
) VALUES (
    'run-interrupted-script-migration',
    'placement-interrupted-script-migration',
    1,
    ?,
    ?,
    ?,
    ?,
    'script_failure',
    '{"code":"workflow.execution.script_failure","fields":{"operation":"script"}}'
)`, now, now+10, now+1, interruptedAt)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 58 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var schedulingState, interruptionReason, interruptionDetail string
	var projectedInterruptedAt int64
	if err := store.db.QueryRowContext(t.Context(), `
SELECT scheduling_state, interruption_reason, interruption_detail_json, interrupted_at_unix_ms
FROM task_current_nodes
WHERE task_id = 'task-interrupted-script-migration'`).Scan(
		&schedulingState,
		&interruptionReason,
		&interruptionDetail,
		&projectedInterruptedAt,
	); err != nil {
		t.Fatalf("query projected interrupted script current node: %v", err)
	}
	if schedulingState != "interrupted" ||
		interruptionReason != "script_failure" ||
		interruptionDetail != `{"code":"workflow.execution.script_failure","fields":{"operation":"script"}}` ||
		projectedInterruptedAt != interruptedAt {
		t.Fatalf(
			"projected interrupted script current node = scheduling=%q reason=%q detail=%q interrupted_at=%d",
			schedulingState,
			interruptionReason,
			interruptionDetail,
			projectedInterruptedAt,
		)
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

func TestOpenMigratesLegacyEmptyParentSessionIDToNullPreviousSession(t *testing.T) {
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
	execSeed(t, db, "workflow", `
INSERT INTO workflows (id, name, version, created_at_unix_ms, updated_at_unix_ms)
VALUES ('workflow-parent-null', 'Workflow', 1, ?, ?)`, now, now)
	execSeed(t, db, "workflow node", `
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, sort_order)
VALUES ('node-parent-null', 'workflow-parent-null', 'agent', 'agent', 'Agent', 0)`)
	execSeed(t, db, "project workflow link", `
INSERT INTO project_workflow_links (id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('link-parent-null', 'project-parent-null', 'workflow-parent-null', ?, ?)`, now, now)
	execSeed(t, db, "task", `
INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id,
    title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (
    'task-parent-null', 'link-parent-null', 1,
    1, 'PAR-1', 'Task', 'Task body', ?, ?, '{}'
)`, now, now)
	execSeed(t, db, "task node placement", `
INSERT INTO task_node_placements (
    id, task_id, node_id, state, created_at_unix_ms, updated_at_unix_ms
) VALUES ('placement-parent-null', 'task-parent-null', 'node-parent-null', 'active', ?, ?)`, now, now)
	execSeed(t, db, "task run", `
INSERT INTO task_runs (
    id, placement_id, session_id, workflow_revision_seen, created_at_unix_ms, updated_at_unix_ms
) VALUES ('run-parent-null', 'placement-parent-null', 'session-parent-null', 1, ?, ?)`, now, now)
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
	var taskRunSessionID sql.NullString
	if err := store.db.QueryRowContext(t.Context(), `
SELECT session_id
FROM task_runs
WHERE id = 'run-parent-null'
`).Scan(&taskRunSessionID); err != nil {
		t.Fatalf("query migrated task run: %v", err)
	}
	if !taskRunSessionID.Valid || taskRunSessionID.String != "session-parent-null" {
		t.Fatalf("migrated task run session id = %+v, want session-parent-null", taskRunSessionID)
	}
}

func TestOpenMigratesLegacySessionParentToTypedProvenance(t *testing.T) {
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
	if err := registerMetadataSQLiteCollations(); err != nil {
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
