package metadata

import (
	"testing"

	"github.com/google/uuid"
)

func TestChatSettingsTaskIdentityForSessionReturnsRetainedPairAndOmitsAbsentTask(t *testing.T) {
	store := openInMemoryMetadataTestStore(t, t.TempDir())
	projectID := "project-chat-settings-task"
	workflowID := uuid.New()
	linkID := "project-workflow-link-chat-settings-task"
	taskID := "task-" + uuid.NewString()
	sessionID := uuid.NewString()
	sessionWithoutTaskID := uuid.NewString()

	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO projects (
		id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json, project_key
	) VALUES (?, 'Chat Settings', 1, 1, '{}', 'KENT')`, projectID); err != nil {
		t.Fatalf("seed Project: %v", err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO workflows (
		id, name, description, version, created_at_unix_ms, updated_at_unix_ms
	) VALUES (?, 'Chat Settings', '', 1, 1, 1)`, workflowID[:]); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO project_workflow_links (
		id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms
	) VALUES (?, ?, ?, 1, 1)`, linkID, projectID, workflowID[:]); err != nil {
		t.Fatalf("seed Project Workflow Link: %v", err)
	}
	if _, err := store.db.ExecContext(t.Context(), `INSERT INTO tasks (
		id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id,
		title, body, source_url, created_at_unix_ms, updated_at_unix_ms, metadata_json
	) VALUES (?, ?, 1, 1, 'KENT-416', 'Chat Settings', '', '', 1, 1, '{}')`, taskID, linkID); err != nil {
		t.Fatalf("seed Task: %v", err)
	}
	for _, input := range []struct {
		sessionID string
		taskID    *string
	}{
		{sessionID: sessionID, taskID: &taskID},
		{sessionID: sessionWithoutTaskID},
	} {
		if _, err := store.db.ExecContext(t.Context(), `INSERT INTO sessions (
			id, project_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms, task_id
		) VALUES (?, ?, ?, 1, 1, ?)`, input.sessionID, projectID, input.sessionID, input.taskID); err != nil {
			t.Fatalf("seed Session %s: %v", input.sessionID, err)
		}
	}

	identity, err := store.ChatSettingsTaskIdentityForSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("ChatSettingsTaskIdentityForSession retained Task: %v", err)
	}
	if identity == nil || identity.TaskID != taskID || identity.TaskShortID != "KENT-416" {
		t.Fatalf("retained Task identity = %+v, want %s / KENT-416", identity, taskID)
	}

	identity, err = store.ChatSettingsTaskIdentityForSession(t.Context(), sessionWithoutTaskID)
	if err != nil {
		t.Fatalf("ChatSettingsTaskIdentityForSession absent Task: %v", err)
	}
	if identity != nil {
		t.Fatalf("absent Task identity = %+v, want nil", identity)
	}

	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM tasks WHERE id = ?`, taskID); err != nil {
		t.Fatalf("delete Task: %v", err)
	}
	identity, err = store.ChatSettingsTaskIdentityForSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("ChatSettingsTaskIdentityForSession deleted Task: %v", err)
	}
	if identity != nil {
		t.Fatalf("deleted Task identity = %+v, want nil", identity)
	}
}
