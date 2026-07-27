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
	if _, err := store.db.Exec(`INSERT INTO task_search_fts(task_search_fts) VALUES ('integrity-check')`); err != nil {
		t.Fatalf("run task-search FTS integrity check: %v", err)
	}
}
