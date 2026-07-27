package metadata

import "testing"

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
	for _, index := range []string{
		"task_search_documents_task_title_unique",
		"task_search_documents_task_body_unique",
		"task_search_documents_comment_unique",
	} {
		if !indexExists(t, store.db, index) {
			t.Fatalf("task-search uniqueness index %s is missing", index)
		}
	}
	for _, trigger := range []string{
		"task_search_document_insert",
		"task_search_document_delete",
		"task_search_task_insert",
		"task_search_comment_insert",
	} {
		var count int
		if err := store.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'trigger' AND name = ?`,
			trigger,
		).Scan(&count); err != nil {
			t.Fatalf("inspect task-search trigger %s: %v", trigger, err)
		}
		if count != 1 {
			t.Fatalf("task-search trigger %s count = %d, want 1", trigger, count)
		}
	}
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
	if _, err := store.db.Exec(`INSERT INTO task_search_fts(task_search_fts) VALUES ('integrity-check')`); err != nil {
		t.Fatalf("run task-search FTS integrity check: %v", err)
	}
}
