package metadata

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
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
		{pragma: "busy_timeout", want: int64(15000)},
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

func TestMetadataSQLitePoolAcquisitionHonorsContextCancellation(t *testing.T) {
	db := openLatestMetadataTestDatabase(t)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	connection, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("acquire only pooled connection: %v", err)
	}
	defer func() { _ = connection.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := db.Conn(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pool acquisition error = %v, want context canceled", err)
	}
}

func TestMetadataSQLiteWritableAndReadOnlyDSNsConfigureFifteenSecondBusyTimeout(t *testing.T) {
	for name, build := range map[string]func(string) (string, error){
		"writable":  metadataSQLiteDSN,
		"read-only": metadataSQLiteReadOnlyDSN,
	} {
		t.Run(name, func(t *testing.T) {
			dsn, err := build("/tmp/kent-metadata.sqlite3")
			if err != nil {
				t.Fatalf("build DSN: %v", err)
			}
			parsed, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("parse DSN: %v", err)
			}
			pragmas := parsed.Query()["_pragma"]
			if !slices.Contains(pragmas, "busy_timeout(15000)") {
				t.Fatalf("DSN pragmas = %v, want busy_timeout(15000)", pragmas)
			}
		})
	}
}

func TestMetadataSQLiteDSNNormalizesWindowsPaths(t *testing.T) {
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
