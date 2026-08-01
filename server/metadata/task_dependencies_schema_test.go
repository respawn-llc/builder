package metadata

import (
	"database/sql"
	"io/fs"
	"slices"
	"strings"
	"testing"
)

func TestOpenCreatesTaskDependencySchema(t *testing.T) {
	t.Parallel()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if !tableExists(t, store.db, "task_dependencies") {
		t.Fatal("task_dependencies table does not exist")
	}
	columns := dependencyTableColumns(t, store.db, "task_dependencies")
	if got, want := columns, []string{"blocker_task_id", "blocked_task_id"}; !equalStrings(got, want) {
		t.Fatalf("task_dependencies columns = %v, want %v", got, want)
	}

	foreignKeys := foreignKeyColumns(t, store.db, "task_dependencies")
	if got, want := foreignKeys, []string{"blocker_task_id", "blocked_task_id"}; !sameStrings(got, want) {
		t.Fatalf("task_dependencies foreign-key columns = %v, want %v", got, want)
	}
	for _, foreignKey := range foreignKeyDetails(t, store.db, "task_dependencies") {
		if foreignKey.Table != "tasks" || foreignKey.OnDelete != "CASCADE" {
			t.Fatalf("task_dependencies foreign key = %+v, want tasks with CASCADE", foreignKey)
		}
	}

	if got, want := indexColumns(t, store.db, "task_dependencies_reverse_idx"), []string{"blocked_task_id", "blocker_task_id"}; !equalStrings(got, want) {
		t.Fatalf("reverse index columns = %v, want %v", got, want)
	}

	seedProjectAndTasksForDependencySchema(t, store.db)
	if _, err := store.db.Exec(`
		INSERT INTO task_dependencies (blocker_task_id, blocked_task_id)
		VALUES ('task-a', 'task-b'), ('task-a', 'task-c')
	`); err != nil {
		t.Fatalf("insert dependency rows: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO task_dependencies (blocker_task_id, blocked_task_id)
		VALUES ('task-a', 'task-b')
	`); err == nil {
		t.Fatal("duplicate ordered dependency pair unexpectedly succeeded")
	}
	if _, err := store.db.Exec(`DELETE FROM tasks WHERE id = 'task-a'`); err != nil {
		t.Fatalf("delete blocker task: %v", err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_dependencies`).Scan(&remaining); err != nil {
		t.Fatalf("count dependency rows after cascade: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("dependency rows after task cascade = %d, want 0", remaining)
	}

	migration, err := fs.ReadFile(migrationsFS, "migrations/00066_task_dependencies.up.sql")
	if err != nil {
		t.Fatalf("read dependency migration: %v", err)
	}
	for _, forbidden := range []string{
		"CREATE TRIGGER",
		"CHECK",
		"reciprocal",
		"cardinality",
		"project",
	} {
		if strings.Contains(strings.ToLower(string(migration)), strings.ToLower(forbidden)) {
			t.Fatalf("dependency migration contains business-invariant mechanism %q", forbidden)
		}
	}
}

type taskDependencyForeignKey struct {
	Table    string
	OnDelete string
}

func foreignKeyDetails(t *testing.T, db *sql.DB, table string) []taskDependencyForeignKey {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_list(` + quoteSQLiteIdentifier(table) + `)`)
	if err != nil {
		t.Fatalf("list foreign keys for %s: %v", table, err)
	}
	defer rows.Close()

	var result []taskDependencyForeignKey
	for rows.Next() {
		var id, sequence int
		var referencedTable, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &referencedTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign key for %s: %v", table, err)
		}
		result = append(result, taskDependencyForeignKey{Table: referencedTable, OnDelete: onDelete})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign keys for %s: %v", table, err)
	}
	return result
}

func foreignKeyColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_list(` + quoteSQLiteIdentifier(table) + `)`)
	if err != nil {
		t.Fatalf("list foreign keys for %s: %v", table, err)
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var id, sequence int
		var referencedTable, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &referencedTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign key for %s: %v", table, err)
		}
		result = append(result, from)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign keys for %s: %v", table, err)
	}
	return result
}

func dependencyTableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + quoteSQLiteIdentifier(table) + `)`)
	if err != nil {
		t.Fatalf("inspect columns for %s: %v", table, err)
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan columns for %s: %v", table, err)
		}
		result = append(result, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns for %s: %v", table, err)
	}
	return result
}

func indexColumns(t *testing.T, db *sql.DB, index string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA index_info(` + quoteSQLiteIdentifier(index) + `)`)
	if err != nil {
		t.Fatalf("inspect index %s: %v", index, err)
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var sequence, columnNumber int
		var name string
		if err := rows.Scan(&sequence, &columnNumber, &name); err != nil {
			t.Fatalf("scan index %s: %v", index, err)
		}
		result = append(result, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index %s: %v", index, err)
	}
	return result
}

func quoteSQLiteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameStrings(left, right []string) bool {
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return equalStrings(left, right)
}

func seedProjectAndTasksForDependencySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	now := int64(1)
	execSeed(t, db, "project", `INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms)
VALUES ('project-dependencies', 'Project', ?, ?)`, now, now)
	seedWorkflowGraph(t, db, "project-dependencies", now)
	for index, taskID := range []string{"task-a", "task-b", "task-c"} {
		execSeed(t, db, "task", workflowSeedTaskSQL, taskID, "link-1", index+1, taskID, now, now)
	}
}
