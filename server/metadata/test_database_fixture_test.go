package metadata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"fmt"
	"sync/atomic"
	"testing"

	"core/server/metadata/sqlitegen"
)

var metadataTestDatabaseSequence atomic.Uint64

//go:embed testdata/latest_schema.sql
var latestMetadataTestSchema []byte

func openInMemoryMetadataTestStore(t *testing.T, persistenceRoot string) *Store {
	t.Helper()
	db := openLatestMetadataTestDatabase(t)
	store := &Store{
		persistenceRoot: persistenceRoot,
		db:              db,
		queries:         sqlitegen.New(db),
	}
	if err := store.BackfillProjectKeys(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("backfill in-memory metadata project keys: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close in-memory metadata store: %v", err)
		}
	})
	return store
}

func openLatestMetadataTestDatabase(t testing.TB) *sql.DB {
	t.Helper()
	db := openEmptyMetadataTestDatabase(t)
	if _, err := db.Exec(string(executableLatestMetadataTestSchema())); err != nil {
		_ = db.Close()
		t.Fatalf("create latest in-memory metadata schema: %v", err)
	}
	db.SetMaxOpenConns(metadataSQLiteConnectionPoolSize)
	db.SetMaxIdleConns(metadataSQLiteConnectionPoolSize)
	return db
}

func executableLatestMetadataTestSchema() []byte {
	lines := bytes.Split(latestMetadataTestSchema, []byte{'\n'})
	executable := make([][]byte, 0, len(lines))
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("CREATE TABLE 'task_search_fts_")) ||
			bytes.HasPrefix(line, []byte("CREATE TABLE 'task_search_short_id_fts_")) {
			continue
		}
		executable = append(executable, line)
	}
	return bytes.Join(executable, []byte{'\n'})
}

func openEmptyMetadataTestDatabase(t testing.TB) *sql.DB {
	t.Helper()
	if err := registerMetadataSQLiteCollations(); err != nil {
		t.Fatalf("register metadata SQLite extensions: %v", err)
	}
	dsn := fmt.Sprintf(
		"file:kent-metadata-test-%d?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(%d)",
		metadataTestDatabaseSequence.Add(1),
		metadataSQLiteBusyTimeoutMilliseconds,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open in-memory metadata database: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func TestAllMetadataMigrationsMatchLatestInMemorySchema(t *testing.T) {
	migrated := openEmptyMetadataTestDatabase(t)
	t.Cleanup(func() { _ = migrated.Close() })
	if err := runMigrations(migrated); err != nil {
		t.Fatalf("run full metadata migration chain in memory: %v", err)
	}

	migratedSchema := metadataTestSchema(t, migrated)
	if !bytes.Equal(migratedSchema, latestMetadataTestSchema) {
		t.Fatalf(
			"full migration schema does not match testdata/latest_schema.sql; regenerate it with ./scripts/dump-metadata-schema.sh\nwant_sha256=%x\ngot_sha256=%x",
			sha256Bytes(latestMetadataTestSchema),
			sha256Bytes(migratedSchema),
		)
	}
	var integrity string
	if err := migrated.QueryRowContext(t.Context(), "PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("migration integrity check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("migration integrity check = %q, want ok", integrity)
	}
	rows, err := migrated.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("migration foreign-key check: %v", err)
	}
	if rows.Next() {
		_ = rows.Close()
		t.Fatal("migration foreign-key check reported violations")
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close migration foreign-key check: %v", err)
	}
}

func metadataTestSchema(t testing.TB, db *sql.DB) []byte {
	t.Helper()
	rows, err := db.Query(`
SELECT type, name, CAST(sql AS TEXT)
FROM sqlite_schema
WHERE sql IS NOT NULL
  AND name != 'sqlite_sequence'
ORDER BY
  CASE type
    WHEN 'table' THEN 0
    WHEN 'view' THEN 1
    WHEN 'index' THEN 2
    WHEN 'trigger' THEN 3
  END,
  name`)
	if err != nil {
		t.Fatalf("query metadata test schema: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var schema bytes.Buffer
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			t.Fatalf("scan metadata test schema: %v", err)
		}
		if schema.Len() > 0 {
			schema.WriteByte('\n')
		}
		schema.WriteString(ddl)
		schema.WriteString(";\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate metadata test schema: %v", err)
	}
	return schema.Bytes()
}

func sha256Bytes(value []byte) [32]byte {
	return sha256.Sum256(value)
}
