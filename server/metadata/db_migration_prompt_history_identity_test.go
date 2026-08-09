package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestPromptHistoryIdentityMigrationDropsOldRowsAndDraftRecovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 76)
	if err != nil {
		t.Fatalf("open version 76 database: %v", err)
	}
	execSeed(t, db, "legacy prompt history and Draft Recovery", `
INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-prompt-cut', 'Project', 1, 1, '{}');
INSERT INTO workspaces (
    id, project_id, canonical_root_path, git_metadata_json, created_at_unix_ms, updated_at_unix_ms
) VALUES ('workspace-prompt-cut', 'project-prompt-cut', '/workspace-prompt-cut', '{}', 1, 1);
INSERT INTO sessions (
    id, project_id, workspace_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms, metadata_json
) VALUES (
    'session-prompt-cut',
    'project-prompt-cut',
    'workspace-prompt-cut',
    'sessions/session-prompt-cut',
    1,
    1,
    '{"conversation_established":true,"input_draft_recovery_buffers":[{"kind":"queued_input","text":"legacy","client_request_id":"legacy-request"}]}'
);
INSERT INTO session_prompt_history_entries (session_id, source_id, text, created_at_unix_ms)
VALUES ('session-prompt-cut', 'legacy-request', 'legacy prompt', 1);
`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 76 database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if columnExists(t, store.db, "session_prompt_history_entries", "source_id") {
		t.Fatal("prompt history source_id survived destructive migration")
	}
	if indexExists(t, store.db, "session_prompt_history_entries_source_idx") {
		t.Fatal("prompt history source identity index survived destructive migration")
	}
	var promptCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_prompt_history_entries`).Scan(&promptCount); err != nil {
		t.Fatalf("count migrated prompt history: %v", err)
	}
	if promptCount != 0 {
		t.Fatalf("migrated prompt history rows = %d, want approved data loss", promptCount)
	}
	var (
		conversationEstablished int
		recoveryType            sql.NullString
	)
	if err := store.db.QueryRow(`
SELECT
    json_extract(metadata_json, '$.conversation_established'),
    json_type(metadata_json, '$.input_draft_recovery_buffers')
FROM sessions
WHERE id = 'session-prompt-cut'
`).Scan(&conversationEstablished, &recoveryType); err != nil {
		t.Fatalf("read migrated session metadata: %v", err)
	}
	if conversationEstablished != 1 || recoveryType.Valid {
		t.Fatalf("migrated metadata conversation=%d recovery=%+v, want unrelated facts preserved and recovery discarded", conversationEstablished, recoveryType)
	}
}
