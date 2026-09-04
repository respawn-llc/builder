package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// AcquireWriteLock holds a transaction-level write lock on the metadata
// database until the returned release function is called.
func AcquireWriteLock(t testing.TB, persistenceRoot string) func() {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(persistenceRoot, "db", "main.sqlite3"))
	if err != nil {
		t.Fatalf("open SQLite metadata database: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatalf("open SQLite metadata connection: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatalf("acquire SQLite metadata write lock: %v", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
			_ = db.Close()
		})
	}
}
