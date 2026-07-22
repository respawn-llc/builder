package metadata

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"os"
	"path/filepath"
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
