package metadata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

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
	dsn, err := metadataSQLiteDSN(`C:\Users\Nek\kent db\main ? #.sqlite3`)
	if err != nil {
		t.Fatalf("metadataSQLiteDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "file:///C:/Users/Nek/kent%20db/main%20%3F%20%23.sqlite3?") {
		t.Fatalf("dsn = %q, want file URL with normalized Windows drive path", dsn)
	}
	if !strings.Contains(dsn, "_pragma=foreign_keys%281%29") {
		t.Fatalf("dsn = %q, want pragma query values preserved", dsn)
	}
}

func TestOpenRejectsUnsupportedMetadataDatabaseWithoutMutation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		recordedVersion *int64
		wantVersion     int64
	}{
		{name: "unversioned", wantVersion: 0},
		{name: "version zero", recordedVersion: new(int64), wantVersion: 0},
		{name: "version 34", recordedVersion: int64Pointer(34), wantVersion: 34},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			dbPath := filepath.Join(root, "db", "main.sqlite3")
			createUnsupportedMetadataDatabase(t, dbPath, test.recordedVersion)
			before := metadataDatabaseDigest(t, dbPath)

			store, err := OpenAtPath(root, dbPath)
			if store != nil {
				_ = store.Close()
				t.Fatal("unsupported metadata database unexpectedly opened")
			}
			var unsupported *UnsupportedMetadataVersionError
			if !errors.As(err, &unsupported) {
				t.Fatalf("OpenAtPath error = %v, want UnsupportedMetadataVersionError", err)
			}
			if unsupported.DatabasePath != dbPath ||
				unsupported.CurrentVersion != test.wantVersion ||
				unsupported.MinimumVersion != 35 {
				t.Fatalf("unsupported metadata error = %+v", unsupported)
			}
			if after := metadataDatabaseDigest(t, dbPath); after != before {
				t.Fatalf("unsupported metadata database changed: before=%x after=%x", before, after)
			}

			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatalf("reopen rejected database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			var journalMode string
			if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
				t.Fatalf("read rejected database journal mode: %v", err)
			}
			if journalMode != "wal" {
				t.Fatalf("rejected database journal mode = %q, want wal", journalMode)
			}
			var marker string
			if err := db.QueryRow(`SELECT value FROM metadata_floor_probe WHERE id = 1`).Scan(&marker); err != nil {
				t.Fatalf("read rejection marker: %v", err)
			}
			if marker != "preserve" {
				t.Fatalf("rejection marker = %q, want preserve", marker)
			}
			if test.recordedVersion == nil {
				if tableExists(t, db, "goose_db_version") {
					t.Fatal("rejection created Goose version storage")
				}
				return
			}
			var latest int64
			if err := db.QueryRow(`
SELECT MAX(version_id)
FROM goose_db_version
WHERE is_applied = 1`).Scan(&latest); err != nil {
				t.Fatalf("read rejected migration version: %v", err)
			}
			if latest != test.wantVersion {
				t.Fatalf("rejected migration version = %d, want %d", latest, test.wantVersion)
			}
		})
	}
}

func TestOpenUpgradesVersion35DatabaseWithHistoricalLedger(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 35)
	if err != nil {
		t.Fatalf("open version 35 database: %v", err)
	}
	for version := int64(1); version < 35; version++ {
		if _, err := db.Exec(`
INSERT INTO goose_db_version (version_id, is_applied)
SELECT ?, 1
WHERE NOT EXISTS (
    SELECT 1
    FROM goose_db_version
    WHERE version_id = ?
      AND is_applied = 1
)`, version, version); err != nil {
			t.Fatalf("record historical migration version %d: %v", version, err)
		}
	}
	if _, err := db.Exec(`
INSERT INTO projects (
    id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (
    'project-v35', 'Version 35', 1, 1, '{}'
);
INSERT INTO workspaces (
    id, project_id, canonical_root_path, git_metadata_json,
    created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'workspace-v35', 'project-v35', '/workspace-v35', '{}', 1, 1
);
UPDATE projects
SET primary_workspace_id = 'workspace-v35'
WHERE id = 'project-v35';
INSERT INTO sessions (
    id, project_id, workspace_id, artifact_relpath, name,
    created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'session-v35', 'project-v35', 'workspace-v35',
    'sessions/session-v35', 'Preserved session', 1, 1
)`); err != nil {
		t.Fatalf("seed version 35 data: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 35 database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("upgrade version 35 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var projectName, sessionName string
	if err := store.db.QueryRow(`SELECT display_name FROM projects WHERE id = 'project-v35'`).Scan(&projectName); err != nil {
		t.Fatalf("read upgraded project: %v", err)
	}
	if err := store.db.QueryRow(`SELECT name FROM sessions WHERE id = 'session-v35'`).Scan(&sessionName); err != nil {
		t.Fatalf("read upgraded session: %v", err)
	}
	if projectName != "Version 35" || sessionName != "Preserved session" {
		t.Fatalf("upgraded data = %q/%q", projectName, sessionName)
	}
	provider, err := newMetadataMigrationProvider(store.db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	version, err := provider.GetDBVersion(t.Context())
	if err != nil {
		t.Fatalf("read upgraded migration version: %v", err)
	}
	if version != 79 {
		t.Fatalf("upgraded migration version = %d, want 79", version)
	}
}

func createUnsupportedMetadataDatabase(t *testing.T, dbPath string, recordedVersion *int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("create unsupported database directory: %v", err)
	}
	dsn, err := metadataSQLiteDSN(dbPath)
	if err != nil {
		t.Fatalf("build unsupported database DSN: %v", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open unsupported database: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE metadata_floor_probe (
    id INTEGER PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO metadata_floor_probe (id, value) VALUES (1, 'preserve')`); err != nil {
		_ = db.Close()
		t.Fatalf("create unsupported database marker: %v", err)
	}
	if recordedVersion != nil {
		if _, err := db.Exec(`
CREATE TABLE goose_db_version (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id INTEGER NOT NULL,
    is_applied INTEGER NOT NULL,
    tstamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
			_ = db.Close()
			t.Fatalf("create Goose version storage: %v", err)
		}
		for version := int64(0); version <= *recordedVersion; version++ {
			if _, err := db.Exec(
				`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`,
				version,
			); err != nil {
				_ = db.Close()
				t.Fatalf("record migration version %d: %v", version, err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close unsupported database: %v", err)
	}
}

func metadataDatabaseDigest(t *testing.T, dbPath string) [sha256.Size]byte {
	t.Helper()
	contents, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read metadata database: %v", err)
	}
	return sha256.Sum256(contents)
}

func int64Pointer(value int64) *int64 {
	return &value
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
	dsn, err := metadataSQLiteDSN(dbPath)
	if err != nil {
		return nil, fmt.Errorf("metadataSQLiteDSN: %w", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	return db, nil
}
