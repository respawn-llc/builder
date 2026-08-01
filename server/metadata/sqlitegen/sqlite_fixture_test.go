package sqlitegen

import (
	"database/sql"
	"testing"
)

func openSQLiteFixture(t *testing.T, dataSourceName string) *sql.DB {
	t.Helper()
	if err := RegisterSQLiteExtensions(); err != nil {
		t.Fatalf("register metadata SQLite extensions: %v", err)
	}
	db, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}
