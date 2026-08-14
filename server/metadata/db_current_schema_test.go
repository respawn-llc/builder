package metadata

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlitedriver "modernc.org/sqlite"
)

func TestLatestMetadataFixtureConfiguresEightConnectionSQLitePool(t *testing.T) {
	db := openLatestMetadataTestDatabase(t)
	t.Cleanup(func() { _ = db.Close() })

	if got := db.Stats().MaxOpenConnections; got != metadataSQLiteConnectionPoolSize {
		t.Fatalf("max open connections = %d, want %d", got, metadataSQLiteConnectionPoolSize)
	}
	connections := make([]*sql.Conn, 0, metadataSQLiteConnectionPoolSize)
	for range metadataSQLiteConnectionPoolSize {
		connection, err := db.Conn(t.Context())
		if err != nil {
			t.Fatalf("acquire pooled connection: %v", err)
		}
		connections = append(connections, connection)
		requireInMemoryMetadataSQLitePragmas(t, connection)
	}
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			t.Fatalf("return pooled connection: %v", err)
		}
	}
}

type metadataSQLitePragmaQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func requireInMemoryMetadataSQLitePragmas(t testing.TB, queryer metadataSQLitePragmaQueryer) {
	t.Helper()
	for _, test := range []struct {
		pragma string
		want   any
	}{
		{pragma: "foreign_keys", want: int64(1)},
		{pragma: "journal_mode", want: "memory"},
		{pragma: "synchronous", want: int64(1)},
		{pragma: "busy_timeout", want: int64(5000)},
	} {
		switch want := test.want.(type) {
		case int64:
			var got int64
			if err := queryer.QueryRowContext(t.Context(), "PRAGMA "+test.pragma).Scan(&got); err != nil {
				t.Fatalf("PRAGMA %s: %v", test.pragma, err)
			}
			if got != want {
				t.Fatalf("%s = %d, want %d", test.pragma, got, want)
			}
		case string:
			var got string
			if err := queryer.QueryRowContext(t.Context(), "PRAGMA "+test.pragma).Scan(&got); err != nil {
				t.Fatalf("PRAGMA %s: %v", test.pragma, err)
			}
			if got != want {
				t.Fatalf("%s = %q, want %q", test.pragma, got, want)
			}
		}
	}
}

func TestMetadataSQLiteDSNNormalizesWindowsPaths(t *testing.T) {
	dsn, err := metadataSQLiteDSN(`C:\Users\Nek\kent db\main ? #.sqlite3`, false)
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

func TestActivatedMetadataSQLiteDSNRequiresExistingFile(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "missing.sqlite3")
	dsn, err := metadataSQLiteDSN(databasePath, true)
	if err != nil {
		t.Fatalf("metadataSQLiteDSN: %v", err)
	}
	connector, err := sqlitedriver.NewConnector(dsn)
	if err != nil {
		t.Fatalf("create SQLite connector: %v", err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err == nil {
		t.Fatal("activated metadata connection unexpectedly created a missing database")
	}
	if _, err := os.Stat(databasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database stat error = %v, want os.ErrNotExist", err)
	}
}

func TestMetadataStartupCreatesThenActivatesExistingFileOnlyPool(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtPath(root, databasePath)
	if err != nil {
		t.Fatalf("openDatabaseAtPath fresh startup: %v", err)
	}
	if _, err := os.Stat(databasePath); err != nil {
		_ = db.Close()
		t.Fatalf("fresh startup database stat: %v", err)
	}
	db.SetMaxIdleConns(0)
	if err := os.Remove(databasePath); err != nil {
		_ = db.Close()
		t.Fatalf("remove activated database: %v", err)
	}
	if err := db.Ping(); err == nil {
		_ = db.Close()
		t.Fatal("activated pool unexpectedly reopened a missing database")
	}
	if _, err := os.Stat(databasePath); !errors.Is(err, os.ErrNotExist) {
		_ = db.Close()
		t.Fatalf("activated pool recreated database; stat error = %v", err)
	}
	_ = db.Close()
}
