package metadata

import (
	"context"
	"database/sql"
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
