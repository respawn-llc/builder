package testsetup

import (
	"context"
	"testing"
	"time"

	"core/server/metadata"
	"core/shared/runtimeids"

	"github.com/google/uuid"
)

// BindSessionToWorkflowTask creates the smallest valid workflow Task aggregate
// needed to test direct Session workflow ownership.
func BindSessionToWorkflowTask(t testing.TB, store *metadata.Store, projectID string, sessionID string) string {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	workflowID := runtimeids.NewWorkflowID()
	linkID := uuid.NewString()
	taskID := uuid.NewString()
	if _, err := store.DB().ExecContext(context.Background(), `
INSERT INTO workflows (id, name, description, version, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, 'Test workflow', '', 1, ?, ?)`,
		workflowID,
		now,
		now,
	); err != nil {
		t.Fatalf("create workflow task test workflow: %v", err)
	}
	if _, err := store.DB().ExecContext(context.Background(), `
INSERT INTO project_workflow_links (id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, ?, ?, ?, ?)`,
		linkID,
		projectID,
		workflowID,
		now,
		now,
	); err != nil {
		t.Fatalf("create workflow task test link: %v", err)
	}
	if _, err := store.DB().ExecContext(context.Background(), `
INSERT INTO tasks (
    id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id,
    title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (?, ?, 1, ?, ?, 'Test task', '', ?, ?, '{}')`,
		taskID,
		linkID,
		now,
		"TEST-"+taskID,
		now,
		now,
	); err != nil {
		t.Fatalf("create workflow task test task: %v", err)
	}
	if _, err := store.DB().ExecContext(context.Background(), `
UPDATE sessions
SET task_id = ?
WHERE id = ?`,
		taskID,
		sessionID,
	); err != nil {
		t.Fatalf("bind test session to workflow task: %v", err)
	}
	return taskID
}
