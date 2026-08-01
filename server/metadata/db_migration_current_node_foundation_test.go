package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCurrentNodeExecutionHistoryCutoverMigrationRollsBackOnLateFailureAndRejectsDown(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 58)
	if err != nil {
		t.Fatalf("open version 58 db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
CREATE VIEW workflow_run_history_cutover_fault AS
SELECT canceled_at_unix_ms
FROM tasks`); err != nil {
		t.Fatalf("create late cutover fault: %v", err)
	}

	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 60); err == nil {
		t.Fatal("hard cutover unexpectedly succeeded with a later DDL fault")
	}
	version, err := provider.GetDBVersion(t.Context())
	if err != nil {
		t.Fatalf("read version after failed cutover: %v", err)
	}
	if version != 59 {
		t.Fatalf("version after failed cutover = %d, want independent migration 59 only", version)
	}
	if !tableExists(t, db, "task_runs") || !columnExists(t, db, "tasks", "canceled_at_unix_ms") {
		t.Fatal("failed hard cutover left destructive schema changes behind")
	}
	for _, relation := range []string{
		"task_current_nodes",
		"task_active_fanouts",
		"task_active_fanout_branches",
		"task_pending_approvals",
		"task_pending_approval_branches",
		"session_workflow_node_associations",
	} {
		if tableExists(t, db, relation) {
			t.Fatalf("failed hard cutover retained replacement relation %q", relation)
		}
	}
	if columnExists(t, db, "sessions", "task_id") {
		t.Fatal("failed hard cutover retained direct Session ownership storage")
	}

	if _, err := db.Exec(`DROP VIEW workflow_run_history_cutover_fault`); err != nil {
		t.Fatalf("drop late cutover fault: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 60); err != nil {
		t.Fatalf("apply hard cutover after fault removal: %v", err)
	}
	version, err = provider.GetDBVersion(t.Context())
	if err != nil {
		t.Fatalf("read version after cutover: %v", err)
	}
	if version != 60 {
		t.Fatalf("version after successful cutover = %d, want 60", version)
	}
	if _, err := provider.Down(t.Context()); err == nil {
		t.Fatal("irreversible hard cutover unexpectedly rolled back")
	}
	version, err = provider.GetDBVersion(t.Context())
	if err != nil {
		t.Fatalf("read version after rejected down: %v", err)
	}
	if version != 60 {
		t.Fatalf("version after rejected down = %d, want 60", version)
	}
	if tableExists(t, db, "task_runs") || columnExists(t, db, "tasks", "canceled_at_unix_ms") {
		t.Fatal("rejected down restored or changed the irreversible cutover schema")
	}
	for _, relation := range []string{
		"task_current_nodes",
		"task_active_fanouts",
		"task_active_fanout_branches",
		"task_pending_approvals",
		"task_pending_approval_branches",
		"session_workflow_node_associations",
	} {
		if !tableExists(t, db, relation) {
			t.Fatalf("rejected down removed replacement relation %q", relation)
		}
	}
	if !columnExists(t, db, "sessions", "task_id") {
		t.Fatal("rejected down removed direct Session ownership storage")
	}
}

func TestMetadataMigrationIrreversibleMarkersAreRegistered(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtPathWithoutMigrationsForTest(root, dbPath)
	if err != nil {
		t.Fatalf("open database without migrations: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var registered int
	if err := db.QueryRow(`
SELECT COUNT(*)
FROM pragma_function_list
WHERE name IN (
    'kent_workflow_run_history_cutover_is_irreversible',
    'kent_current_node_prior_transition_parameters_are_irreversible',
    'kent_workflow_session_agent_role_backfill_is_irreversible'
)`).Scan(&registered); err != nil {
		t.Fatalf("list irreversible migration marker functions: %v", err)
	}
	if registered != 3 {
		t.Fatalf("registered irreversible migration markers = %d, want 3", registered)
	}
}

func TestSessionCategoryMigrationAddsNullableConstrainedIndexedStorage(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if !columnExists(t, store.db, "sessions", "category") {
		t.Fatal("sessions.category is missing")
	}
	if !indexExists(t, store.db, "sessions_visible_category_recency_idx") {
		t.Fatal("sessions_visible_category_recency_idx is missing")
	}

	var partial int
	rows, err := store.db.Query(`PRAGMA index_list('sessions')`)
	if err != nil {
		t.Fatalf("list session indexes: %v", err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var sequence int
		var name, origin string
		var unique int
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan session index: %v", err)
		}
		if name == "sessions_visible_category_recency_idx" {
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate session indexes: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close session indexes: %v", err)
	}
	if !found || partial != 1 {
		t.Fatalf("visible category recency index found=%v partial=%d, want true/1", found, partial)
	}

	indexRows, err := store.db.Query(`PRAGMA index_xinfo('sessions_visible_category_recency_idx')`)
	if err != nil {
		t.Fatalf("inspect session category index: %v", err)
	}
	defer func() { _ = indexRows.Close() }()
	type indexedColumn struct {
		name string
		desc int
	}
	var columns []indexedColumn
	for indexRows.Next() {
		var sequence, columnID, desc, key int
		var name sql.NullString
		var collation string
		if err := indexRows.Scan(&sequence, &columnID, &name, &desc, &collation, &key); err != nil {
			t.Fatalf("scan session category index column: %v", err)
		}
		if key == 1 && name.Valid {
			columns = append(columns, indexedColumn{name: name.String, desc: desc})
		}
	}
	if err := indexRows.Err(); err != nil {
		t.Fatalf("iterate session category index columns: %v", err)
	}
	wantColumns := []indexedColumn{
		{name: "project_id"},
		{name: "category"},
		{name: "updated_at_unix_ms", desc: 1},
		{name: "id", desc: 1},
	}
	if len(columns) != len(wantColumns) {
		t.Fatalf("index columns = %+v, want %+v", columns, wantColumns)
	}
	for index := range wantColumns {
		if columns[index] != wantColumns[index] {
			t.Fatalf("index column %d = %+v, want %+v", index, columns[index], wantColumns[index])
		}
	}
}

func TestSessionCategoryStorageRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)
	if _, err := store.db.Exec(`UPDATE sessions SET category = 'worker' WHERE id = ?`, sess.Meta().SessionID); err == nil {
		t.Fatal("sessions.category accepted an invalid value")
	}
}

func TestSessionCategoryMigrationPreservesLegacyRowsAsNull(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 53)
	if err != nil {
		t.Fatalf("open version 53 db: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	workspaceRoot := t.TempDir()
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys for legacy seed: %v", err)
	}
	execSeed(t, db, "legacy workspace", `INSERT INTO workspaces (id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms)
VALUES ('workspace-category-legacy', 'project-category-legacy', ?, '{}', ?, ?)`, workspaceRoot, now, now)
	execSeed(t, db, "legacy project", `INSERT INTO projects (id, display_name, project_key, next_task_seq, primary_workspace_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('project-category-legacy', 'Legacy category', 'LEG', 1, 'workspace-category-legacy', ?, ?)`, now, now)
	execSeed(t, db, "legacy session", `INSERT INTO sessions (
    id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms
) VALUES (
    'session-category-legacy',
    'project-category-legacy',
    'workspace-category-legacy',
    'projects/project-category-legacy/sessions/session-category-legacy',
    ?,
    ?
)`, now, now)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys after legacy seed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 53 db: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var category sql.NullString
	if err := store.db.QueryRow(`SELECT category FROM sessions WHERE id = 'session-category-legacy'`).Scan(&category); err != nil {
		t.Fatalf("scan migrated category: %v", err)
	}
	if category.Valid {
		t.Fatalf("legacy category = %q, want SQL NULL", category.String)
	}
	record, err := store.ResolvePersistedSession(t.Context(), "session-category-legacy")
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.Category != nil {
		t.Fatalf("legacy resolved category = %v, want absent", record.Meta.Category)
	}
}
