package metadata

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"core/server/tasksearchtext"

	"github.com/pressly/goose/v3"
)

type taskSearchCanonicalSource struct {
	text string
}

func TestTaskSearchSchemaBackfillsCanonicalSourceDocuments(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, object := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "task_search_documents"},
		{kind: "view", name: "task_search_content"},
		{kind: "table", name: "task_search_fts"},
	} {
		var count int
		if err := store.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_schema WHERE type = ? AND name = ?`,
			object.kind,
			object.name,
		).Scan(&count); err != nil {
			t.Fatalf("inspect task-search %s %s: %v", object.kind, object.name, err)
		}
		if count != 1 {
			t.Fatalf("task-search %s %s count = %d, want 1", object.kind, object.name, count)
		}
	}
	assertTaskSearchIndexCatalog(t, store.db, []string{
		"task_search_documents_comment_unique",
		"task_search_documents_task_body_unique",
		"task_search_documents_task_title_unique",
	})
	assertTaskSearchTriggerCatalog(t, store.db, []string{
		"task_search_comment_body_after_update",
		"task_search_comment_body_before_update",
		"task_search_comment_delete",
		"task_search_comment_insert",
		"task_search_document_delete",
		"task_search_document_insert",
		"task_search_task_body_after_update",
		"task_search_task_body_before_update",
		"task_search_task_delete",
		"task_search_task_insert",
		"task_search_task_title_after_update",
		"task_search_task_title_before_update",
	})
	rows, err := store.db.Query(`PRAGMA foreign_key_list('task_search_documents')`)
	if err != nil {
		t.Fatalf("list task-search mapping foreign keys: %v", err)
	}
	defer func() { _ = rows.Close() }()
	foreignKeys := map[string]string{}
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan task-search mapping foreign key: %v", err)
		}
		foreignKeys[from] = table + ":" + onDelete
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task-search mapping foreign keys: %v", err)
	}
	for column, want := range map[string]string{
		"task_id":    "tasks:CASCADE",
		"comment_id": "task_comments:CASCADE",
	} {
		if got := foreignKeys[column]; got != want {
			t.Fatalf("task-search mapping foreign key %s = %q, want %q", column, got, want)
		}
	}
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchMigrationBackfillsLegacyTaskAndCommentDocuments(t *testing.T) {
	legacy, root := openVersion59TaskSearchFixture(t)
	if err := legacy.Close(); err != nil {
		t.Fatalf("close version 59 metadata database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated metadata database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	assertTaskSearchInvariants(t, store.db)
	for _, search := range []struct {
		query      string
		sourceKind string
		wantID     string
	}{
		{query: "title one zebra", sourceKind: "title", wantID: "task-legacy-search-one"},
		{query: "body two wombat", sourceKind: "body", wantID: "task-legacy-search-two"},
		{query: "comment one vulture", sourceKind: "comment", wantID: "comment-legacy-search-one"},
		{query: "comment two urchin", sourceKind: "comment", wantID: "comment-legacy-search-two"},
	} {
		assertTaskSearchSourceSearchable(t, store.db, search.query, search.sourceKind, search.wantID)
	}
}

func TestTaskSearchMigrationFailureRollsBackSchemaAndBackfill(t *testing.T) {
	legacy, root := openVersion59TaskSearchFixture(t)
	migrations := taskSearchMigrationsWithForcedFailure(t)
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		legacy,
		migrations,
		goose.WithLogger(goose.NopLogger()),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatalf("create failing task-search migration provider: %v", err)
	}
	if _, err := provider.UpTo(context.Background(), 60); err == nil {
		t.Fatal("task-search migration unexpectedly succeeded despite forced trailing failure")
	}
	assertNoTaskSearchSchemaObjects(t, legacy)
	assertTaskSearchLegacySourcesRemain(t, legacy)
	if err := legacy.Close(); err != nil {
		t.Fatalf("close failed task-search migration database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open database after failed task-search migration: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchDocumentTriggersCreateCanonicalSources(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTask(t, store, binding.ProjectID, "KNT-1")
	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-1', 'task-1', 'comment body', 'user', 'operator', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert task comment: %v", err)
	}

	rows, err := store.db.Query(`
SELECT source_kind, task_id, comment_id
FROM task_search_documents
ORDER BY document_id ASC`)
	if err != nil {
		t.Fatalf("list task-search documents: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var kind string
		var taskID, commentID sql.NullString
		if err := rows.Scan(&kind, &taskID, &commentID); err != nil {
			t.Fatalf("scan task-search document: %v", err)
		}
		got = append(got, kind+":"+taskID.String+":"+commentID.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task-search documents: %v", err)
	}
	slices.Sort(got)
	want := []string{
		"body:task-1:",
		"comment::comment-1",
		"title:task-1:",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("task-search documents = %v, want %v", got, want)
	}
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchMappingRejectsNonCanonicalOrDuplicateSources(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowTask(t, store, binding.ProjectID, "KNT-1")
	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-1', 'task-1', 'comment body', 'user', 'operator', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert task comment: %v", err)
	}

	for _, statement := range []string{
		`INSERT INTO task_search_documents (source_kind, task_id, comment_id)
VALUES ('title', 'task-1', 'comment-1')`,
		`INSERT INTO task_search_documents (source_kind, task_id)
VALUES ('comment', 'task-1')`,
		`INSERT INTO task_search_documents (source_kind, comment_id)
VALUES ('body', 'comment-1')`,
		`INSERT INTO task_search_documents (source_kind, task_id)
VALUES ('title', 'task-1')`,
		`INSERT INTO task_search_documents (source_kind, task_id)
VALUES ('body', 'task-1')`,
		`INSERT INTO task_search_documents (source_kind, comment_id)
VALUES ('comment', 'comment-1')`,
	} {
		assertSQLiteConstraint(t, store.db, statement)
	}
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchSourceMutationsSynchronizeFTS(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	insertTaskSearchTestTask(t, store.db, "task-mutation", 1, "KNT-1", "task create title koala", "task create body llama", now)
	assertTaskSearchSourceSearchable(t, store.db, "create title koala", "title", "task-mutation")
	assertTaskSearchSourceSearchable(t, store.db, "create body llama", "body", "task-mutation")
	assertTaskSearchInvariants(t, store.db)

	if _, err := store.db.Exec(`UPDATE tasks SET title = 'task update title mongoose' WHERE id = 'task-mutation'`); err != nil {
		t.Fatalf("update task title: %v", err)
	}
	assertTaskSearchSourceNotSearchable(t, store.db, "create title koala")
	assertTaskSearchSourceSearchable(t, store.db, "update title mongoose", "title", "task-mutation")
	assertTaskSearchInvariants(t, store.db)

	if _, err := store.db.Exec(`UPDATE tasks SET body = 'task update body narwhal' WHERE id = 'task-mutation'`); err != nil {
		t.Fatalf("update task body: %v", err)
	}
	assertTaskSearchSourceNotSearchable(t, store.db, "create body llama")
	assertTaskSearchSourceSearchable(t, store.db, "update body narwhal", "body", "task-mutation")
	assertTaskSearchInvariants(t, store.db)

	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-mutation', 'task-mutation', 'comment create otter', 'user', 'operator', ?, ?)`, now, now); err != nil {
		t.Fatalf("create task comment: %v", err)
	}
	assertTaskSearchSourceSearchable(t, store.db, "create otter", "comment", "comment-mutation")
	assertTaskSearchInvariants(t, store.db)

	if _, err := store.db.Exec(`UPDATE task_comments SET body = 'comment update puffin' WHERE id = 'comment-mutation'`); err != nil {
		t.Fatalf("update task comment: %v", err)
	}
	assertTaskSearchSourceNotSearchable(t, store.db, "create otter")
	assertTaskSearchSourceSearchable(t, store.db, "update puffin", "comment", "comment-mutation")
	assertTaskSearchInvariants(t, store.db)

	if _, err := store.db.Exec(`DELETE FROM task_comments WHERE id = 'comment-mutation'`); err != nil {
		t.Fatalf("delete task comment: %v", err)
	}
	assertTaskSearchSourceNotSearchable(t, store.db, "update puffin")
	assertTaskSearchInvariants(t, store.db)

	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-task-delete', 'task-mutation', 'comment task delete quokka', 'user', 'operator', ?, ?)`, now, now); err != nil {
		t.Fatalf("create task-delete comment: %v", err)
	}
	assertTaskSearchSourceSearchable(t, store.db, "task delete quokka", "comment", "comment-task-delete")
	if _, err := store.db.Exec(`DELETE FROM tasks WHERE id = 'task-mutation'`); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	for _, query := range []string{
		"update title mongoose",
		"update body narwhal",
		"task delete quokka",
	} {
		assertTaskSearchSourceNotSearchable(t, store.db, query)
	}
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchMappingFailureRollsBackCanonicalCreates(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	insertTaskSearchTestTask(t, store.db, "task-stable", 1, "KNT-1", "stable task title", "stable task body", now)
	if _, err := store.db.Exec(`CREATE TRIGGER test_task_search_mapping_failure
BEFORE INSERT ON task_search_documents
BEGIN
    SELECT RAISE(ABORT, 'forced task-search mapping failure');
END`); err != nil {
		t.Fatalf("create mapping failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.Exec(`DROP TRIGGER IF EXISTS test_task_search_mapping_failure`)
	})

	if _, err := store.db.Exec(`INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body,
    created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES ('task-mapping-failure', 'link-1', 1, 2, 'KNT-2', 'mapping failure title', 'mapping failure body', ?, ?, '{}')`, now, now); err == nil {
		t.Fatal("task creation succeeded despite injected mapping failure")
	}
	assertTaskSearchTaskAbsent(t, store.db, "task-mapping-failure")

	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-mapping-failure', 'task-stable', 'mapping failure comment', 'user', 'operator', ?, ?)`, now, now); err == nil {
		t.Fatal("comment creation succeeded despite injected mapping failure")
	}
	assertTaskSearchCommentAbsent(t, store.db, "comment-mapping-failure")
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchFTSFailureRollsBackCanonicalMutations(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	insertTaskSearchTestTask(t, store.db, "task-fts-failure", 1, "KNT-1", "fts original title", "fts original body", now)
	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-fts-failure', 'task-fts-failure', 'fts original comment', 'user', 'operator', ?, ?)`, now, now); err != nil {
		t.Fatalf("create FTS failure task comment: %v", err)
	}
	for _, mutation := range []struct {
		name          string
		statement     string
		verifySources []string
		absentSources []string
	}{
		{
			name:          "task title update",
			statement:     `UPDATE tasks SET title = 'fts changed title' WHERE id = 'task-fts-failure'`,
			verifySources: []string{"original title"},
			absentSources: []string{"changed title"},
		},
		{
			name:          "task body update",
			statement:     `UPDATE tasks SET body = 'fts changed body' WHERE id = 'task-fts-failure'`,
			verifySources: []string{"original body"},
			absentSources: []string{"changed body"},
		},
		{
			name:          "comment body update",
			statement:     `UPDATE task_comments SET body = 'fts changed comment' WHERE id = 'comment-fts-failure'`,
			verifySources: []string{"original comment"},
			absentSources: []string{"changed comment"},
		},
		{
			name:          "comment delete",
			statement:     `DELETE FROM task_comments WHERE id = 'comment-fts-failure'`,
			verifySources: []string{"original comment"},
		},
		{
			name:          "task delete",
			statement:     `DELETE FROM tasks WHERE id = 'task-fts-failure'`,
			verifySources: []string{"original title", "original body", "original comment"},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			tx, err := store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatalf("begin transaction: %v", err)
			}
			if _, err := tx.Exec(`DROP TABLE task_search_fts`); err != nil {
				t.Fatalf("drop FTS table: %v", err)
			}
			if _, err := tx.Exec(mutation.statement); err == nil {
				t.Fatalf("%s succeeded despite injected FTS failure", mutation.name)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatalf("roll back injected FTS failure: %v", err)
			}
			for _, query := range mutation.verifySources {
				assertTaskSearchSourceSearchable(t, store.db, query, "", "")
			}
			for _, query := range mutation.absentSources {
				assertTaskSearchSourceNotSearchable(t, store.db, query)
			}
			assertTaskSearchInvariants(t, store.db)
		})
	}
}

func openVersion59TaskSearchFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	legacy, err := openDatabaseAtVersionForTest(t, root, dbPath, 59)
	if err != nil {
		t.Fatalf("open version 59 metadata database: %v", err)
	}
	assertNoTaskSearchSchemaObjects(t, legacy)
	now := int64(1)
	execSeed(t, legacy, "legacy project", `INSERT INTO projects (
    id, display_name, project_key, next_task_seq, created_at_unix_ms, updated_at_unix_ms
) VALUES ('project-legacy-search', 'Legacy search', 'LEG', 3, ?, ?)`, now, now)
	seedWorkflowGraph(t, legacy, "project-legacy-search", now)
	for _, task := range []struct {
		id      string
		seq     int
		shortID string
		title   string
		body    string
	}{
		{id: "task-legacy-search-one", seq: 1, shortID: "LEG-1", title: "legacy title one zebra", body: "legacy body one yak"},
		{id: "task-legacy-search-two", seq: 2, shortID: "LEG-2", title: "legacy title two xerus", body: "legacy body two wombat"},
	} {
		execSeed(t, legacy, "legacy task", `INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body,
    created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (?, 'link-1', 1, ?, ?, ?, ?, ?, ?, '{}')`,
			task.id, task.seq, task.shortID, task.title, task.body, now, now,
		)
	}
	for _, comment := range []struct {
		id     string
		taskID string
		body   string
	}{
		{id: "comment-legacy-search-one", taskID: "task-legacy-search-one", body: "legacy comment one vulture"},
		{id: "comment-legacy-search-two", taskID: "task-legacy-search-two", body: "legacy comment two urchin"},
	} {
		execSeed(t, legacy, "legacy task comment", `INSERT INTO task_comments (
    id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms
) VALUES (?, ?, ?, 'user', 'operator', ?, ?)`, comment.id, comment.taskID, comment.body, now, now)
	}
	return legacy, root
}

func taskSearchMigrationsWithForcedFailure(t *testing.T) fs.FS {
	t.Helper()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("list metadata migrations: %v", err)
	}
	migrations := fstest.MapFS{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := "migrations/" + entry.Name()
		data, err := fs.ReadFile(migrationsFS, path)
		if err != nil {
			t.Fatalf("read metadata migration %s: %v", entry.Name(), err)
		}
		if entry.Name() == "00060_task_search_index.up.sql" {
			data = append(data, []byte("\nSELECT * FROM task_search_forced_migration_failure;\n")...)
		}
		migrations[path] = &fstest.MapFile{Data: data}
	}
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatalf("create task-search migration filesystem: %v", err)
	}
	return sub
}

func assertNoTaskSearchSchemaObjects(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*)
FROM sqlite_schema
WHERE name >= ?
  AND name < ?`, "task_search_", "task_search`").Scan(&count); err != nil {
		t.Fatalf("count task-search schema objects: %v", err)
	}
	if count != 0 {
		t.Fatalf("task-search schema object count = %d, want 0", count)
	}
}

func assertTaskSearchLegacySourcesRemain(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, task := range []struct {
		id    string
		title string
		body  string
	}{
		{id: "task-legacy-search-one", title: "legacy title one zebra", body: "legacy body one yak"},
		{id: "task-legacy-search-two", title: "legacy title two xerus", body: "legacy body two wombat"},
	} {
		var title, body string
		if err := db.QueryRow(`SELECT title, body FROM tasks WHERE id = ?`, task.id).Scan(&title, &body); err != nil {
			t.Fatalf("read %s after failed task-search migration: %v", task.id, err)
		}
		if title != task.title || body != task.body {
			t.Fatalf("task %s after failed task-search migration = title:%q body:%q, want title:%q body:%q", task.id, title, body, task.title, task.body)
		}
	}
	for _, comment := range []struct {
		id   string
		body string
	}{
		{id: "comment-legacy-search-one", body: "legacy comment one vulture"},
		{id: "comment-legacy-search-two", body: "legacy comment two urchin"},
	} {
		var body string
		if err := db.QueryRow(`SELECT body FROM task_comments WHERE id = ?`, comment.id).Scan(&body); err != nil {
			t.Fatalf("read %s after failed task-search migration: %v", comment.id, err)
		}
		if body != comment.body {
			t.Fatalf("comment %s after failed task-search migration = %q, want %q", comment.id, body, comment.body)
		}
	}
}

func insertTaskSearchTestTask(t *testing.T, db *sql.DB, id string, sequence int, shortID string, title string, body string, now int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body,
    created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (?, 'link-1', 1, ?, ?, ?, ?, ?, ?, '{}')`, id, sequence, shortID, title, body, now, now); err != nil {
		t.Fatalf("insert task-search task %s: %v", id, err)
	}
}

func assertTaskSearchTaskAbsent(t *testing.T, db *sql.DB, taskID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id = ?`, taskID).Scan(&count); err != nil {
		t.Fatalf("count task %s: %v", taskID, err)
	}
	if count != 0 {
		t.Fatalf("task %s count = %d, want 0", taskID, count)
	}
}

func assertTaskSearchCommentAbsent(t *testing.T, db *sql.DB, commentID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_comments WHERE id = ?`, commentID).Scan(&count); err != nil {
		t.Fatalf("count task comment %s: %v", commentID, err)
	}
	if count != 0 {
		t.Fatalf("task comment %s count = %d, want 0", commentID, count)
	}
}

func assertTaskSearchIndexCatalog(t *testing.T, db *sql.DB, want []string) {
	t.Helper()
	rows, err := db.Query(`SELECT name, [unique], partial
FROM pragma_index_list('task_search_documents')
WHERE name >= ?
  AND name < ?
ORDER BY name`, "task_search_documents_", "task_search_documentt")
	if err != nil {
		t.Fatalf("list task-search indexes: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var name string
		var unique, partial int
		if err := rows.Scan(&name, &unique, &partial); err != nil {
			t.Fatalf("scan task-search index: %v", err)
		}
		if unique != 1 || partial != 1 {
			t.Fatalf("task-search index %s metadata = unique:%d partial:%d, want unique partial index", name, unique, partial)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task-search indexes: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("task-search index catalog = %v, want %v", got, want)
	}
}

func assertTaskSearchTriggerCatalog(t *testing.T, db *sql.DB, want []string) {
	t.Helper()
	rows, err := db.Query(`SELECT name
FROM sqlite_schema
WHERE type = 'trigger'
  AND name >= ?
  AND name < ?
ORDER BY name`, "task_search_", "task_search`")
	if err != nil {
		t.Fatalf("list task-search triggers: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan task-search trigger: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task-search triggers: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("task-search trigger catalog = %v, want %v", got, want)
	}
}

func assertTaskSearchInvariants(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO task_search_fts(task_search_fts) VALUES ('integrity-check')`); err != nil {
		t.Fatalf("run task-search FTS integrity check: %v", err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("run foreign-key check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table string
		var rowID int64
		var parent string
		var foreignKey int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKey); err != nil {
			t.Fatalf("scan foreign-key violation: %v", err)
		}
		t.Fatalf("foreign-key violation: table=%s rowid=%d parent=%s foreign_key=%d", table, rowID, parent, foreignKey)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign-key check: %v", err)
	}

	canonical := queryTaskSearchCanonicalSources(t, db)
	mappings := queryTaskSearchMappings(t, db)
	content := queryTaskSearchContent(t, db)
	ftsRowIDs := queryTaskSearchFTSRowIDs(t, db)

	if len(canonical) != len(mappings) || len(canonical) != len(content) || len(canonical) != len(ftsRowIDs) {
		t.Fatalf("task-search source counts = canonical:%d mapping:%d content:%d fts:%d, want one-to-one", len(canonical), len(mappings), len(content), len(ftsRowIDs))
	}
	for identity, source := range canonical {
		documentID, ok := mappings[identity]
		if !ok {
			t.Fatalf("canonical source %s has no mapping", identity)
		}
		if got, ok := content[documentID]; !ok || got != source.text {
			t.Fatalf("canonical source %s content = %q, want %q", identity, got, source.text)
		}
		if !ftsRowIDs[documentID] {
			t.Fatalf("canonical source %s document id %d is absent from FTS", identity, documentID)
		}
	}
}

func queryTaskSearchCanonicalSources(t *testing.T, db *sql.DB) map[string]taskSearchCanonicalSource {
	t.Helper()
	rows, err := db.Query(`SELECT 'title:' || id, title FROM tasks
UNION ALL
SELECT 'body:' || id, body FROM tasks
UNION ALL
SELECT 'comment:' || id, body FROM task_comments`)
	if err != nil {
		t.Fatalf("list canonical task-search sources: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]taskSearchCanonicalSource)
	for rows.Next() {
		var identity, text string
		if err := rows.Scan(&identity, &text); err != nil {
			t.Fatalf("scan canonical task-search source: %v", err)
		}
		if _, exists := out[identity]; exists {
			t.Fatalf("duplicate canonical task-search source identity %s", identity)
		}
		out[identity] = taskSearchCanonicalSource{text: text}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate canonical task-search sources: %v", err)
	}
	return out
}

func queryTaskSearchMappings(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT document_id, source_kind, task_id, comment_id
FROM task_search_documents`)
	if err != nil {
		t.Fatalf("list task-search mappings: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var documentID int64
		var sourceKind string
		var taskID, commentID sql.NullString
		if err := rows.Scan(&documentID, &sourceKind, &taskID, &commentID); err != nil {
			t.Fatalf("scan task-search mapping: %v", err)
		}
		identity := taskSearchSourceIdentity(t, sourceKind, taskID, commentID)
		if prior, exists := out[identity]; exists {
			t.Fatalf("duplicate mapping for source %s: document ids %d and %d", identity, prior, documentID)
		}
		out[identity] = documentID
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task-search mappings: %v", err)
	}
	return out
}

func queryTaskSearchContent(t *testing.T, db *sql.DB) map[int64]string {
	t.Helper()
	rows, err := db.Query(`SELECT document_id, title, body, comment FROM task_search_content`)
	if err != nil {
		t.Fatalf("list task-search content: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var documentID int64
		var title, body, comment sql.NullString
		if err := rows.Scan(&documentID, &title, &body, &comment); err != nil {
			t.Fatalf("scan task-search content: %v", err)
		}
		text := taskSearchSparseText(t, documentID, title, body, comment)
		if _, exists := out[documentID]; exists {
			t.Fatalf("duplicate task-search content document id %d", documentID)
		}
		out[documentID] = text
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task-search content: %v", err)
	}
	return out
}

func queryTaskSearchFTSRowIDs(t *testing.T, db *sql.DB) map[int64]bool {
	t.Helper()
	rows, err := db.Query(`SELECT rowid FROM task_search_fts`)
	if err != nil {
		t.Fatalf("list task-search FTS row ids: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]bool{}
	for rows.Next() {
		var documentID int64
		if err := rows.Scan(&documentID); err != nil {
			t.Fatalf("scan task-search FTS row id: %v", err)
		}
		if out[documentID] {
			t.Fatalf("duplicate task-search FTS row id %d", documentID)
		}
		out[documentID] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task-search FTS row ids: %v", err)
	}
	return out
}

func taskSearchSourceIdentity(t *testing.T, sourceKind string, taskID sql.NullString, commentID sql.NullString) string {
	t.Helper()
	switch sourceKind {
	case "title", "body":
		if !taskID.Valid || commentID.Valid {
			t.Fatalf("task-search %s mapping has task_id=%+v comment_id=%+v, want only task id", sourceKind, taskID, commentID)
		}
		return sourceKind + ":" + taskID.String
	case "comment":
		if taskID.Valid || !commentID.Valid {
			t.Fatalf("task-search comment mapping has task_id=%+v comment_id=%+v, want only comment id", taskID, commentID)
		}
		return sourceKind + ":" + commentID.String
	default:
		t.Fatalf("unknown task-search source kind %q", sourceKind)
		return ""
	}
}

func taskSearchSparseText(t *testing.T, documentID int64, title sql.NullString, body sql.NullString, comment sql.NullString) string {
	t.Helper()
	values := []sql.NullString{title, body, comment}
	var text string
	count := 0
	for _, value := range values {
		if value.Valid {
			count++
			text = value.String
		}
	}
	if count != 1 {
		t.Fatalf("task-search content document id %d has %d populated text columns, want 1", documentID, count)
	}
	return text
}

func assertTaskSearchSourceSearchable(t *testing.T, db *sql.DB, query string, wantSourceKind string, wantSourceID string) {
	t.Helper()
	rows, err := db.Query(`SELECT document.source_kind, document.task_id, document.comment_id
FROM task_search_documents document
JOIN task_search_fts ON task_search_fts.rowid = document.document_id
WHERE task_search_fts MATCH ?`, taskSearchCandidateExpression(t, query))
	if err != nil {
		t.Fatalf("search task-search FTS for %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var sources []struct {
		kind      string
		taskID    sql.NullString
		commentID sql.NullString
	}
	for rows.Next() {
		var source struct {
			kind      string
			taskID    sql.NullString
			commentID sql.NullString
		}
		if err := rows.Scan(&source.kind, &source.taskID, &source.commentID); err != nil {
			t.Fatalf("scan task-search FTS result for %q: %v", query, err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate task-search FTS result for %q: %v", query, err)
	}
	if len(sources) != 1 {
		t.Fatalf("task-search FTS result count for %q = %d, want 1", query, len(sources))
	}
	if wantSourceKind == "" {
		return
	}
	source := sources[0]
	if source.kind != wantSourceKind {
		t.Fatalf("task-search FTS source kind for %q = %q, want %q", query, source.kind, wantSourceKind)
	}
	if (source.kind == "comment" && (!source.commentID.Valid || source.commentID.String != wantSourceID)) ||
		(source.kind != "comment" && (!source.taskID.Valid || source.taskID.String != wantSourceID)) {
		t.Fatalf("task-search FTS source for %q = task:%+v comment:%+v, want %s:%s", query, source.taskID, source.commentID, wantSourceKind, wantSourceID)
	}
}

func assertTaskSearchSourceNotSearchable(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*)
FROM task_search_fts
WHERE task_search_fts MATCH ?`, taskSearchCandidateExpression(t, query)).Scan(&count); err != nil {
		t.Fatalf("search task-search FTS for absent source %q: %v", query, err)
	}
	if count != 0 {
		t.Fatalf("task-search FTS result count for absent source %q = %d, want 0", query, count)
	}
}

func taskSearchCandidateExpression(t *testing.T, query string) string {
	t.Helper()
	matcher, err := tasksearchtext.NewLiteralMatcher(query, tasksearchtext.LiteralCaseInsensitive)
	if err != nil {
		t.Fatalf("create literal matcher for task-search FTS query %q: %v", query, err)
	}
	return matcher.CandidateExpression()
}
