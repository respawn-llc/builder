package metadata

import "testing"

func TestTaskSearchDocumentTriggersCreateAndSynchronizeCanonicalSources(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	insertTaskSearchTestTask(t, store.db, "task-mutation", 1, "KNT-1", "task create title koala", "task create body llama", now)
	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-mutation', 'task-mutation', 'comment create otter', 'user', 'operator', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert task comment: %v", err)
	}
	assertTaskSearchSourceSearchable(t, store.db, "create title koala", "title", "task-mutation")
	assertTaskSearchSourceSearchable(t, store.db, "create body llama", "body", "task-mutation")
	assertTaskSearchSourceSearchable(t, store.db, "create otter", "comment", "comment-mutation")

	if _, err := store.db.Exec(`UPDATE tasks SET title = 'task update title mongoose', body = 'task update body narwhal' WHERE id = 'task-mutation'`); err != nil {
		t.Fatalf("update task sources: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE task_comments SET body = 'comment update puffin' WHERE id = 'comment-mutation'`); err != nil {
		t.Fatalf("update Comment source: %v", err)
	}
	for _, query := range []string{"create title koala", "create body llama", "create otter"} {
		assertTaskSearchSourceNotSearchable(t, store.db, query)
	}
	assertTaskSearchSourceSearchable(t, store.db, "update title mongoose", "title", "task-mutation")
	assertTaskSearchSourceSearchable(t, store.db, "update body narwhal", "body", "task-mutation")
	assertTaskSearchSourceSearchable(t, store.db, "update puffin", "comment", "comment-mutation")

	if _, err := store.db.Exec(`DELETE FROM task_comments WHERE id = 'comment-mutation'`); err != nil {
		t.Fatalf("delete Comment: %v", err)
	}
	assertTaskSearchSourceNotSearchable(t, store.db, "update puffin")
	if _, err := store.db.Exec(`DELETE FROM tasks WHERE id = 'task-mutation'`); err != nil {
		t.Fatalf("delete Task: %v", err)
	}
	for _, query := range []string{"update title mongoose", "update body narwhal"} {
		assertTaskSearchSourceNotSearchable(t, store.db, query)
	}
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchMappingRejectsInvalidOrDuplicateCanonicalSources(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	insertTaskSearchTestTask(t, store.db, "task-1", 1, "KNT-1", "title", "body", now)
	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-1', 'task-1', 'comment', 'user', 'operator', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert Comment: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO task_search_documents (source_kind, task_id, comment_id) VALUES ('title', 'task-1', 'comment-1')`,
		`INSERT INTO task_search_documents (source_kind, task_id) VALUES ('comment', 'task-1')`,
		`INSERT INTO task_search_documents (source_kind, comment_id) VALUES ('body', 'comment-1')`,
		`INSERT INTO task_search_documents (source_kind, task_id) VALUES ('title', 'task-1')`,
		`INSERT INTO task_search_documents (source_kind, task_id) VALUES ('body', 'task-1')`,
		`INSERT INTO task_search_documents (source_kind, comment_id) VALUES ('comment', 'comment-1')`,
	} {
		assertTaskSearchConstraint(t, store.db, statement)
	}
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchIndexFailuresRollBackCanonicalWrites(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	insertTaskSearchTestTask(t, store.db, "task-stable", 1, "KNT-1", "stable title", "stable body", now)
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
		t.Fatal("Task create succeeded despite mapping failure")
	}
	assertTaskSearchTaskAbsent(t, store.db, "task-mapping-failure")
	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-mapping-failure', 'task-stable', 'mapping failure comment', 'user', 'operator', ?, ?)`, now, now); err == nil {
		t.Fatal("Comment create succeeded despite mapping failure")
	}
	assertTaskSearchCommentAbsent(t, store.db, "comment-mapping-failure")
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchFTSFailuresRollBackCanonicalMutations(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	insertTaskSearchTestTask(t, store.db, "task-fts-failure", 1, "KNT-1", "fts original title", "fts original body", now)
	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-fts-failure', 'task-fts-failure', 'fts original comment', 'user', 'operator', ?, ?)`, now, now); err != nil {
		t.Fatalf("create FTS failure Comment: %v", err)
	}
	for _, mutation := range []struct {
		name      string
		statement string
	}{
		{name: "Task title update", statement: `UPDATE tasks SET title = 'fts changed title' WHERE id = 'task-fts-failure'`},
		{name: "Task body update", statement: `UPDATE tasks SET body = 'fts changed body' WHERE id = 'task-fts-failure'`},
		{name: "Comment body update", statement: `UPDATE task_comments SET body = 'fts changed comment' WHERE id = 'comment-fts-failure'`},
		{name: "Comment delete", statement: `DELETE FROM task_comments WHERE id = 'comment-fts-failure'`},
		{name: "Task delete", statement: `DELETE FROM tasks WHERE id = 'task-fts-failure'`},
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
				t.Fatalf("%s succeeded despite missing FTS", mutation.name)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatalf("rollback injected FTS failure: %v", err)
			}
			for _, query := range []string{"original title", "original body", "original comment"} {
				assertTaskSearchSourceSearchable(t, store.db, query, "", "")
			}
			assertTaskSearchInvariants(t, store.db)
		})
	}
}
