package metadata

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"core/server/metadata/sqlitegen"
	"core/shared/tasksearchtext"

	"github.com/pressly/goose/v3"
)

type taskSearchCanonicalSource struct {
	text string
}

var taskSearchTriggerNames = []string{
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
}

func TestTaskSearchSchemaExposesTheRequiredOperationalContract(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	assertTaskSearchSchemaObjects(t, store.db)
	assertTaskSearchIndexCatalog(t, store.db, []string{
		"task_search_documents_comment_unique",
		"task_search_documents_task_body_unique",
		"task_search_documents_task_title_unique",
	})
	assertTaskSearchTriggerCatalog(t, store.db, taskSearchTriggerNames)
	assertTaskSearchInvariants(t, store.db)
	failures, err := store.Queries().ListTaskSearchSchemaContractFailures(t.Context())
	if err != nil {
		t.Fatalf("ListTaskSearchSchemaContractFailures: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("task-search schema contract failures = %v", failures)
	}
}

func TestTaskSearchMigrationCompletesFromEveryPublishedCheckpoint(t *testing.T) {
	for _, checkpoint := range []int64{59, 60, 61, 62} {
		t.Run(fmt.Sprintf("version_%d", checkpoint), func(t *testing.T) {
			legacy, root := openVersion59TaskSearchFixture(t)
			if checkpoint > 59 {
				provider, err := newMetadataMigrationProvider(legacy)
				if err != nil {
					t.Fatalf("create migration provider: %v", err)
				}
				if _, err := provider.UpTo(t.Context(), checkpoint); err != nil {
					t.Fatalf("advance to migration %d: %v", checkpoint, err)
				}
			}
			if err := legacy.Close(); err != nil {
				t.Fatalf("close version %d metadata database: %v", checkpoint, err)
			}

			store, err := Open(root)
			if err != nil {
				t.Fatalf("Open from version %d: %v", checkpoint, err)
			}
			t.Cleanup(func() { _ = store.Close() })

			assertTaskSearchSchemaObjects(t, store.db)
			assertTaskSearchIndexCatalog(t, store.db, []string{
				"task_search_documents_comment_unique",
				"task_search_documents_task_body_unique",
				"task_search_documents_task_title_unique",
			})
			assertTaskSearchTriggerCatalog(t, store.db, taskSearchTriggerNames)
			assertTaskSearchInvariants(t, store.db)
			assertTaskSearchLegacySourcesSearchable(t, store.db)
		})
	}
}

func assertTaskSearchLegacySourcesSearchable(t *testing.T, db *sql.DB) {
	t.Helper()
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
		assertTaskSearchSourceSearchable(t, db, search.query, search.sourceKind, search.wantID)
	}
}

func TestTaskSearchMigration61IsAtomic(t *testing.T) {
	legacy, _ := openVersion59TaskSearchFixture(t)
	t.Cleanup(func() { _ = legacy.Close() })
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		legacy,
		taskSearchMigrationsWithForcedFailure(t),
		goose.WithLogger(goose.NopLogger()),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatalf("create failing task-search migration provider: %v", err)
	}
	if _, err := provider.UpTo(context.Background(), 61); err == nil {
		t.Fatal("task-search migration unexpectedly succeeded despite forced trailing failure")
	}
	version, err := provider.GetDBVersion(t.Context())
	if err != nil {
		t.Fatalf("read version after failed task-search migration: %v", err)
	}
	if version != 60 {
		t.Fatalf("version after failed task-search migration = %d, want 60", version)
	}
	assertNoTaskSearchSchemaObjects(t, legacy)
	assertTaskSearchLegacySourcesRemain(t, legacy)
}

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

func TestTaskSearchWorkflowDeletionRemovesOnlyDeletedSources(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	seedWorkflowGraphForProject(t, store.db, binding.ProjectID, now, "2")
	insertTaskSearchTestTask(t, store.db, "task-workflow-deleted", 1, "KNT-1", "workflow delete title raven", "workflow delete body salmon", now)
	insertTaskSearchTestTaskForLink(t, store.db, "task-workflow-survives", "link-2", 2, "KNT-2", "workflow survive title tiger", "workflow survive body urchin", now)
	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-workflow-deleted', 'task-workflow-deleted', 'workflow delete comment viper', 'user', 'operator', ?, ?)`, now, now); err != nil {
		t.Fatalf("create workflow deletion comment: %v", err)
	}
	assertTaskSearchInvariants(t, store.db)

	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin workflow deletion transaction: %v", err)
	}
	q := store.Queries().WithTx(tx)
	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{name: "pending approvals", run: func() error {
			_, err := q.DeleteWorkflowTaskPendingApprovalsByWorkflowID(t.Context(), "workflow-1")
			return err
		}},
		{name: "current nodes", run: func() error {
			_, err := q.DeleteWorkflowTaskCurrentNodesByWorkflowID(t.Context(), "workflow-1")
			return err
		}},
		{name: "task comments", run: func() error {
			_, err := q.DeleteWorkflowTaskCommentsByWorkflowID(t.Context(), "workflow-1")
			return err
		}},
		{name: "tasks", run: func() error {
			_, err := q.DeleteWorkflowTasksByWorkflowID(t.Context(), "workflow-1")
			return err
		}},
		{name: "default project links", run: func() error {
			_, err := q.ClearDeletedWorkflowDefaultProjectLinks(t.Context(), sqlitegen.ClearDeletedWorkflowDefaultProjectLinksParams{
				UpdatedAtUnixMs: now,
				WorkflowID:      "workflow-1",
			})
			return err
		}},
		{name: "project workflow links", run: func() error {
			_, err := q.DeleteProjectWorkflowLinksByWorkflowID(t.Context(), "workflow-1")
			return err
		}},
	} {
		if err := operation.run(); err != nil {
			_ = tx.Rollback()
			t.Fatalf("delete workflow %s: %v", operation.name, err)
		}
	}
	deleted, err := q.DeleteWorkflowByID(t.Context(), "workflow-1")
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("delete workflow: %v", err)
	}
	if deleted != 1 {
		_ = tx.Rollback()
		t.Fatalf("deleted workflow rows = %d, want 1", deleted)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit workflow deletion: %v", err)
	}

	for _, query := range []string{
		"delete title raven",
		"delete body salmon",
		"delete comment viper",
	} {
		assertTaskSearchSourceNotSearchable(t, store.db, query)
	}
	assertTaskSearchSourceSearchable(t, store.db, "survive title tiger", "title", "task-workflow-survives")
	assertTaskSearchSourceSearchable(t, store.db, "survive body urchin", "body", "task-workflow-survives")
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchProjectDeletionCascadesOnlyDeletedSources(t *testing.T) {
	store, _, binding := newMetadataTestStore(t)
	now := int64(1)
	seedWorkflowGraph(t, store.db, binding.ProjectID, now)
	insertTaskSearchTestTask(t, store.db, "task-project-deleted", 1, "KNT-1", "project delete title walrus", "project delete body xerus", now)
	if _, err := store.db.Exec(`
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('comment-project-deleted', 'task-project-deleted', 'project delete comment yak', 'user', 'operator', ?, ?)`, now, now); err != nil {
		t.Fatalf("create project deletion comment: %v", err)
	}
	other, err := store.CreateProjectForWorkspace(t.Context(), t.TempDir(), "Task search survivor")
	if err != nil {
		t.Fatalf("create survivor project: %v", err)
	}
	seedWorkflowGraphForProject(t, store.db, other.ProjectID, now, "2")
	insertTaskSearchTestTaskForLink(t, store.db, "task-project-survives", "link-2", 1, "SUR-1", "project survive title zebra", "project survive body antelope", now)
	assertTaskSearchInvariants(t, store.db)

	tx, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin project deletion transaction: %v", err)
	}
	q := store.Queries().WithTx(tx)
	if _, err := q.DeleteProjectTaskPendingApprovals(t.Context(), binding.ProjectID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("delete project task pending approvals: %v", err)
	}
	if err := q.DeleteProjectTasks(t.Context(), binding.ProjectID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("delete project tasks: %v", err)
	}
	deleted, err := q.DeleteProject(t.Context(), binding.ProjectID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("delete project: %v", err)
	}
	if deleted != 1 {
		_ = tx.Rollback()
		t.Fatalf("deleted project rows = %d, want 1", deleted)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit project deletion: %v", err)
	}

	var remainingLinks int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM project_workflow_links WHERE project_id = ?`, binding.ProjectID).Scan(&remainingLinks); err != nil {
		t.Fatalf("count deleted Project workflow links: %v", err)
	}
	if remainingLinks != 0 {
		t.Fatalf("deleted Project workflow links = %d, want 0 after foreign-key cascade", remainingLinks)
	}
	for _, query := range []string{
		"delete title walrus",
		"delete body xerus",
		"delete comment yak",
	} {
		assertTaskSearchSourceNotSearchable(t, store.db, query)
	}
	assertTaskSearchSourceSearchable(t, store.db, "survive title zebra", "title", "task-project-survives")
	assertTaskSearchSourceSearchable(t, store.db, "survive body antelope", "body", "task-project-survives")
	assertTaskSearchInvariants(t, store.db)
}

func TestTaskSearchSourceTableRebuildFailsContractAndRollsBackToHealthySchema(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		missingTrigger string
		rebuild        func(*sql.Tx) error
	}{
		{
			name:           "tasks",
			missingTrigger: "task_search_task_insert",
			rebuild: func(tx *sql.Tx) error {
				if _, err := tx.Exec(`CREATE TABLE tasks_rebuilt AS SELECT * FROM tasks`); err != nil {
					return err
				}
				if _, err := tx.Exec(`DROP TABLE tasks`); err != nil {
					return err
				}
				_, err := tx.Exec(`ALTER TABLE tasks_rebuilt RENAME TO tasks`)
				return err
			},
		},
		{
			name:           "task comments",
			missingTrigger: "task_search_comment_insert",
			rebuild: func(tx *sql.Tx) error {
				if _, err := tx.Exec(`CREATE TABLE task_comments_rebuilt AS SELECT * FROM task_comments`); err != nil {
					return err
				}
				if _, err := tx.Exec(`DROP TABLE task_comments`); err != nil {
					return err
				}
				_, err := tx.Exec(`ALTER TABLE task_comments_rebuilt RENAME TO task_comments`)
				return err
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			conn, err := store.db.Conn(t.Context())
			if err != nil {
				t.Fatalf("acquire SQLite connection: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			if _, err := conn.ExecContext(t.Context(), `PRAGMA foreign_keys = OFF`); err != nil {
				t.Fatalf("disable foreign keys for %s rebuild: %v", testCase.name, err)
			}
			if _, err := conn.ExecContext(t.Context(), `PRAGMA legacy_alter_table = ON`); err != nil {
				t.Fatalf("enable legacy table rebuild mode for %s: %v", testCase.name, err)
			}
			tx, err := conn.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatalf("begin %s rebuild: %v", testCase.name, err)
			}
			if err := testCase.rebuild(tx); err != nil {
				_ = tx.Rollback()
				t.Fatalf("rebuild %s: %v", testCase.name, err)
			}
			var triggerCount int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'trigger' AND name = ?`, testCase.missingTrigger).Scan(&triggerCount); err != nil {
				_ = tx.Rollback()
				t.Fatalf("inspect rebuilt %s trigger: %v", testCase.name, err)
			}
			if triggerCount != 0 {
				_ = tx.Rollback()
				t.Fatalf("rebuild %s retained %s, want contract failure", testCase.name, testCase.missingTrigger)
			}
			failures, err := store.Queries().WithTx(tx).ListTaskSearchSchemaContractFailures(t.Context())
			if err != nil {
				_ = tx.Rollback()
				t.Fatalf("validate rebuilt %s schema contract: %v", testCase.name, err)
			}
			if len(failures) == 0 {
				_ = tx.Rollback()
				t.Fatalf("rebuilt %s unexpectedly passed task-search schema contract", testCase.name)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatalf("roll back %s rebuild: %v", testCase.name, err)
			}
			if _, err := conn.ExecContext(t.Context(), `PRAGMA foreign_keys = ON`); err != nil {
				t.Fatalf("restore foreign keys after %s rebuild: %v", testCase.name, err)
			}
			if _, err := conn.ExecContext(t.Context(), `PRAGMA legacy_alter_table = OFF`); err != nil {
				t.Fatalf("restore table rebuild mode after %s: %v", testCase.name, err)
			}
			assertTaskSearchTriggerCatalog(t, store.db, taskSearchTriggerNames)
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
		execSeed(t, legacy, "legacy Task", `INSERT INTO tasks (
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
		execSeed(t, legacy, "legacy Task Comment", `INSERT INTO task_comments (
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
		if entry.Name() == "00061_task_search_index.up.sql" {
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

func assertTaskSearchSchemaObjects(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, object := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "task_search_documents"},
		{kind: "table", name: "task_search_fts"},
		{kind: "view", name: "task_search_content"},
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = ? AND name = ?`, object.kind, object.name).Scan(&count); err != nil {
			t.Fatalf("inspect task-search %s %s: %v", object.kind, object.name, err)
		}
		if count != 1 {
			t.Fatalf("task-search %s %s count = %d, want 1", object.kind, object.name, count)
		}
	}
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
	for _, source := range []struct {
		id   string
		text string
	}{
		{id: "task-legacy-search-one", text: "legacy title one zebra"},
		{id: "task-legacy-search-two", text: "legacy title two xerus"},
	} {
		var title string
		if err := db.QueryRow(`SELECT title FROM tasks WHERE id = ?`, source.id).Scan(&title); err != nil {
			t.Fatalf("read legacy Task %s: %v", source.id, err)
		}
		if title != source.text {
			t.Fatalf("legacy Task %s title = %q, want %q", source.id, title, source.text)
		}
	}
	for _, source := range []struct {
		id   string
		text string
	}{
		{id: "comment-legacy-search-one", text: "legacy comment one vulture"},
		{id: "comment-legacy-search-two", text: "legacy comment two urchin"},
	} {
		var body string
		if err := db.QueryRow(`SELECT body FROM task_comments WHERE id = ?`, source.id).Scan(&body); err != nil {
			t.Fatalf("read legacy Comment %s: %v", source.id, err)
		}
		if body != source.text {
			t.Fatalf("legacy Comment %s body = %q, want %q", source.id, body, source.text)
		}
	}
}

func insertTaskSearchTestTask(t *testing.T, db *sql.DB, id string, sequence int, shortID string, title string, body string, now int64) {
	t.Helper()
	insertTaskSearchTestTaskForLink(t, db, id, "link-1", sequence, shortID, title, body, now)
}

func insertTaskSearchTestTaskForLink(t *testing.T, db *sql.DB, id string, linkID string, sequence int, shortID string, title string, body string, now int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body,
    created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, '{}')`, id, linkID, sequence, shortID, title, body, now, now); err != nil {
		t.Fatalf("insert Task %s: %v", id, err)
	}
}

func assertTaskSearchTaskAbsent(t *testing.T, db *sql.DB, taskID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id = ?`, taskID).Scan(&count); err != nil {
		t.Fatalf("count Task %s: %v", taskID, err)
	}
	if count != 0 {
		t.Fatalf("Task %s count = %d, want 0", taskID, count)
	}
}

func assertTaskSearchCommentAbsent(t *testing.T, db *sql.DB, commentID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_comments WHERE id = ?`, commentID).Scan(&count); err != nil {
		t.Fatalf("count Comment %s: %v", commentID, err)
	}
	if count != 0 {
		t.Fatalf("Comment %s count = %d, want 0", commentID, count)
	}
}

func assertTaskSearchConstraint(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err == nil {
		t.Fatalf("expected Task-search constraint failure for %s", statement)
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
	out := map[string]taskSearchCanonicalSource{}
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
	count := 0
	var text string
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
