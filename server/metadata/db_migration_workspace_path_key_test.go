package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestWorkspacePathKeyMigrationLeavesPreExistingRowsNull(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 69)
	if err != nil {
		t.Fatalf("open version 69 database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if columnExists(t, db, "workspaces", "managed_worktree_path_key") {
		t.Fatal("workspace path-key column exists before migration 70")
	}

	const now = int64(1)
	if _, err := db.Exec(`
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-path-key-migration', 'Project', ?, ?, '{}')`,
		now, now,
	); err != nil {
		t.Fatalf("insert pre-migration project: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO workspaces (id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms)
VALUES ('workspace-path-key-migration', 'project-path-key-migration', '/tmp/path-key-migration', '{}', ?, ?)`,
		now, now,
	); err != nil {
		t.Fatalf("insert pre-migration workspace: %v", err)
	}

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 70); err != nil {
		t.Fatalf("apply workspace path-key migration: %v", err)
	}
	var key sql.NullString
	if err := db.QueryRowContext(t.Context(), `
SELECT managed_worktree_path_key
FROM workspaces
WHERE id = 'workspace-path-key-migration'`).Scan(&key); err != nil {
		t.Fatalf("read migrated workspace path key: %v", err)
	}
	if key.Valid {
		t.Fatalf("migrated pre-existing workspace path key = %q, want NULL", key.String)
	}
	if _, err := db.Exec(`
UPDATE workspaces
SET managed_worktree_path_key = ''
WHERE id = 'workspace-path-key-migration'`); err == nil {
		t.Fatal("workspace path-key migration accepted an empty key")
	}
}
