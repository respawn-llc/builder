package metadata

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceChatDraftCutoverDeletesLegacyDraftAndPreservesSessionState(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 88)
	if err != nil {
		t.Fatalf("open version 88 database: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	workspaceRoot := filepath.Join(root, "workspace")
	execSeed(t, db, "project", `
INSERT INTO projects (
    id,
    display_name,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
) VALUES ('project-chat-draft-cutover', 'Project', ?, ?, '{}')`,
		now,
		now,
	)
	execSeed(t, db, "workspace Chat draft", `
INSERT INTO workspaces (
    id,
    project_id,
    canonical_root_path,
    git_metadata_json,
    created_at_unix_ms,
    updated_at_unix_ms,
    chat_draft_json
) VALUES (
    'workspace-chat-draft-cutover',
    'project-chat-draft-cutover',
    ?,
    '{}',
    ?,
    ?,
    '{"message":"discard legacy workspace draft"}'
)`,
		workspaceRoot,
		now,
		now,
	)
	execSeed(t, db, "ordinary Session draft and settings", `
INSERT INTO sessions (
    id,
    project_id,
    workspace_id,
    artifact_relpath,
    input_draft,
    category,
    created_at_unix_ms,
    updated_at_unix_ms,
    metadata_json
) VALUES (
    '8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e',
    'project-chat-draft-cutover',
    'workspace-chat-draft-cutover',
    'projects/project-chat-draft-cutover/sessions/8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e',
    'preserve ordinary Session draft',
    'main',
    ?,
    ?,
    '{"chat_settings":{"supervisor":"all","thinking":"high","fast":false,"questions":false,"auto_compaction":false}}'
)`,
		now,
		now,
	)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 88 database: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("upgrade metadata database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if columnExists(t, store.db, "workspaces", "chat_draft_json") {
		t.Fatal("legacy workspace Chat draft column survived cutover")
	}
	var inputDraft, supervisor, thinking string
	var fast, questions, autoCompaction bool
	if err := store.db.QueryRow(`
SELECT
    input_draft,
    json_extract(metadata_json, '$.chat_settings.supervisor'),
    json_extract(metadata_json, '$.chat_settings.thinking'),
    json_extract(metadata_json, '$.chat_settings.fast'),
    json_extract(metadata_json, '$.chat_settings.questions'),
    json_extract(metadata_json, '$.chat_settings.auto_compaction')
FROM sessions
WHERE id = '8b0a92d4-18f8-4b5f-9b66-b8ac0f3f987e'`,
	).Scan(
		&inputDraft,
		&supervisor,
		&thinking,
		&fast,
		&questions,
		&autoCompaction,
	); err != nil {
		t.Fatalf("read preserved ordinary Session state: %v", err)
	}
	if inputDraft != "preserve ordinary Session draft" ||
		supervisor != "all" ||
		thinking != "high" ||
		fast ||
		questions ||
		autoCompaction {
		t.Fatalf(
			"ordinary Session state = draft=%q supervisor=%q thinking=%q fast=%t questions=%t auto_compaction=%t",
			inputDraft,
			supervisor,
			thinking,
			fast,
			questions,
			autoCompaction,
		)
	}
}

func TestWorkspaceChatDraftCutoverFailurePreventsMetadataStartup(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "db", "main.sqlite3")
	db, err := openDatabaseAtVersionForTest(t, root, dbPath, 88)
	if err != nil {
		t.Fatalf("open version 88 database: %v", err)
	}
	execSeed(t, db, "migration blocker", `
CREATE VIEW workspace_chat_draft_migration_blocker AS
SELECT chat_draft_json
FROM workspaces`)
	if err := db.Close(); err != nil {
		t.Fatalf("close version 88 database: %v", err)
	}

	store, err := Open(root)
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("metadata startup succeeded despite failed workspace Chat draft cutover")
	}
	if store != nil {
		t.Fatal("failed metadata startup returned a Store")
	}
	var migrationErr *WorkspaceChatDraftCutoverMigrationError
	if !errors.As(err, &migrationErr) {
		t.Fatalf("metadata startup error = %v, want typed migration failure", err)
	}

	unmigrated, err := openDatabaseAtPathWithoutMigrationsForTest(root, dbPath)
	if err != nil {
		t.Fatalf("reopen failed migration database: %v", err)
	}
	t.Cleanup(func() { _ = unmigrated.Close() })
	version, err := readMetadataVersion(unmigrated)
	if err != nil {
		t.Fatalf("read failed migration version: %v", err)
	}
	if version != 88 {
		t.Fatalf("failed migration version = %d, want 88", version)
	}
	if !columnExists(t, unmigrated, "workspaces", "chat_draft_json") {
		t.Fatal("failed migration partially removed workspace Chat draft schema")
	}
}
