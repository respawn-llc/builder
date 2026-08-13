package metadata

import (
	"testing"

	"core/server/metadata/sqlitegen"
)

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
	workflowID := workflowTestID(t, "1")
	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{name: "pending approvals", run: func() error {
			_, err := q.DeleteWorkflowTaskPendingApprovalsByWorkflowID(t.Context(), workflowID)
			return err
		}},
		{name: "current nodes", run: func() error {
			_, err := q.DeleteWorkflowTaskCurrentNodesByWorkflowID(t.Context(), workflowID)
			return err
		}},
		{name: "task comments", run: func() error {
			_, err := q.DeleteWorkflowTaskCommentsByWorkflowID(t.Context(), workflowID)
			return err
		}},
		{name: "tasks", run: func() error {
			_, err := q.DeleteWorkflowTasksByWorkflowID(t.Context(), workflowID)
			return err
		}},
		{name: "default project links", run: func() error {
			_, err := q.ClearDeletedWorkflowDefaultProjectLinks(t.Context(), sqlitegen.ClearDeletedWorkflowDefaultProjectLinksParams{
				UpdatedAtUnixMs: now,
				WorkflowID:      workflowID,
			})
			return err
		}},
		{name: "project workflow links", run: func() error {
			_, err := q.DeleteProjectWorkflowLinksByWorkflowID(t.Context(), workflowID)
			return err
		}},
	} {
		if err := operation.run(); err != nil {
			_ = tx.Rollback()
			t.Fatalf("delete workflow %s: %v", operation.name, err)
		}
	}
	deleted, err := q.DeleteWorkflowByID(t.Context(), workflowID)
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
