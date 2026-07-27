package metadata

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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

func TestOpenConfiguresSQLitePragmasThroughPathSafeDSN(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestOpenSupportsConcurrentIndependentDatabases(t *testing.T) {
	t.Parallel()
	const databaseCount = 8
	roots := make([]string, databaseCount)
	for index := range roots {
		roots[index] = t.TempDir()
	}

	start := make(chan struct{})
	errs := make(chan error, databaseCount)
	var workers sync.WaitGroup
	workers.Add(databaseCount)
	for _, root := range roots {
		go func() {
			defer workers.Done()
			<-start
			store, err := Open(root)
			if err == nil {
				err = store.Close()
			}
			errs <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Open: %v", err)
		}
	}
}

func TestMetadataSQLiteDSNNormalizesWindowsPaths(t *testing.T) {
	t.Parallel()
	dsn := metadataSQLiteDSN(`C:\Users\Nek\kent db\main ? #.sqlite3`)
	if !strings.HasPrefix(dsn, "file:///C:/Users/Nek/kent%20db/main%20%3F%20%23.sqlite3?") {
		t.Fatalf("dsn = %q, want file URL with normalized Windows drive path", dsn)
	}
	if !strings.Contains(dsn, "_pragma=foreign_keys%281%29") {
		t.Fatalf("dsn = %q, want pragma query values preserved", dsn)
	}
}

func TestOpenAllowsDatabaseAtRemovedMigrationVersion(t *testing.T) {
	t.Parallel()
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

func openDatabaseAtVersionForTest(t *testing.T, root string, dbPath string, version int64) (*sql.DB, error) {
	t.Helper()
	db, err := openDatabaseAtPathWithoutMigrationsForTest(root, dbPath)
	if err != nil {
		return nil, err
	}
	provider, err := newMetadataMigrationProvider(db)
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
	if err := registerMetadataSQLiteFunctions(); err != nil {
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
